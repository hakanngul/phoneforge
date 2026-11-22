package iossim

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory is the host-side memory use of one booted simulator: the process tree
// under the device's launchd_sim.
//
// RSSMB is the sum of resident sizes. It over-counts shared dyld-cache pages
// and drops when the host compresses an idle simulator, so it is only a rough
// guide (a stock iOS 27 simulator sums to >20 GB of RSS). FootprintMB is the
// sum of phys_footprint from footprint(1) (dirty + compressed memory) and is
// the number slimming is judged by; it is 0 when footprint is unavailable.
type Memory struct {
	UDID        string  `json:"udid"`
	RSSMB       float64 `json:"rssMB"`
	FootprintMB float64 `json:"footprintMB"`
	ProcCount   int     `json:"procCount"`
}

// MB is the headline number: footprint when known, else RSS.
func (m Memory) MB() float64 {
	if m.FootprintMB > 0 {
		return m.FootprintMB
	}
	return m.RSSMB
}

// HostProc is one row of `ps -axo pid=,ppid=,rss=,command=`.
type HostProc struct {
	PID, PPID int
	RSSKB     int64
	Command   string
}

// HostRunner executes a host command and returns stdout. Replaceable in tests.
type HostRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecHost is the real HostRunner. Non-zero exits still return stdout so
// footprint(1) can report partial results when a process has exited.
func ExecHost(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	return out.Bytes(), err
}

func nextField(s string) (field, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// ParsePS parses `ps -axo pid=,ppid=,rss=,command=` output.
func ParsePS(data []byte) []HostProc {
	var out []HostProc
	for _, line := range strings.Split(string(data), "\n") {
		a, r := nextField(line)
		b, r := nextField(r)
		c, r := nextField(r)
		cmd := strings.TrimSpace(r)
		pid, e1 := strconv.Atoi(a)
		ppid, e2 := strconv.Atoi(b)
		rss, e3 := strconv.ParseInt(c, 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || cmd == "" {
			continue
		}
		out = append(out, HostProc{PID: pid, PPID: ppid, RSSKB: rss, Command: cmd})
	}
	return out
}

// SimTree returns the launchd_sim process for udid and all its descendants
// (root first). ok is false when the simulator has no launchd_sim.
func SimTree(procs []HostProc, udid string) (tree []HostProc, ok bool) {
	var root *HostProc
	for i := range procs {
		p := &procs[i]
		if strings.HasPrefix(p.Command, "launchd_sim ") && strings.Contains(p.Command, "/"+udid+"/") {
			root = p
			break
		}
	}
	if root == nil {
		return nil, false
	}
	kids := map[int][]int{}
	for i, p := range procs {
		kids[p.PPID] = append(kids[p.PPID], i)
	}
	seen := map[int]bool{root.PID: true}
	tree = []HostProc{*root}
	for q := 0; q < len(tree); q++ {
		for _, i := range kids[tree[q].PID] {
			if c := procs[i]; !seen[c.PID] {
				seen[c.PID] = true
				tree = append(tree, c)
			}
		}
	}
	return tree, true
}

var (
	fpSummaryRe = regexp.MustCompile(`Summary Footprint:\s+(\d+) B`)
	fpProcRe    = regexp.MustCompile(`(?m)^\S.*\[\d+\]: .*Footprint:\s+(\d+) B`)
)

// ParseFootprint extracts the total footprint in bytes from `footprint -f bytes
// --noCategories pid...` output (a "Summary Footprint" line for several pids,
// a single per-process line for one).
func ParseFootprint(data []byte) (int64, bool) {
	if m := fpSummaryRe.FindSubmatch(data); m != nil {
		n, _ := strconv.ParseInt(string(m[1]), 10, 64)
		return n, true
	}
	var total int64
	found := false
	for _, m := range fpProcRe.FindAllSubmatch(data, -1) {
		n, _ := strconv.ParseInt(string(m[1]), 10, 64)
		total += n
		found = true
	}
	return total, found
}

// Measure sums RSS and footprint over the simulator's host process tree. It
// does not need sudo (the simulator runs as the current user).
func Measure(ctx context.Context, run HostRunner, udid string) (Memory, error) {
	if runtime.GOOS != "darwin" {
		return Memory{}, ErrMacOnly
	}
	b, err := run(ctx, "ps", "-axo", "pid=,ppid=,rss=,command=")
	if err != nil && len(b) == 0 {
		return Memory{}, fmt.Errorf("ps: %w", err)
	}
	return measureFrom(ctx, run, udid, ParsePS(b))
}

func measureFrom(ctx context.Context, run HostRunner, udid string, procs []HostProc) (Memory, error) {
	tree, ok := SimTree(procs, udid)
	if !ok {
		return Memory{}, ErrNotBooted
	}
	m := Memory{UDID: udid, ProcCount: len(tree)}
	args := []string{"-f", "bytes", "--noCategories"}
	var rss int64
	for _, p := range tree {
		rss += p.RSSKB
		args = append(args, strconv.Itoa(p.PID))
	}
	m.RSSMB = float64(rss) / 1024
	if out, _ := run(ctx, "footprint", args...); len(out) > 0 {
		if n, ok := ParseFootprint(out); ok {
			m.FootprintMB = float64(n) / (1 << 20)
		}
	}
	return m, nil
}

// MeasureSettled waits settle, then takes samples measurements interval apart
// and returns the one with the median headline value (so the result is a real
// observation, with its process count).
func MeasureSettled(ctx context.Context, run HostRunner, udid string, settle time.Duration, samples int, interval time.Duration) (Memory, error) {
	if samples < 1 {
		samples = 1
	}
	if err := sleepCtx(ctx, settle); err != nil {
		return Memory{}, err
	}
	var all []Memory
	for i := 0; i < samples; i++ {
		if i > 0 {
			if err := sleepCtx(ctx, interval); err != nil {
				return Memory{}, err
			}
		}
		m, err := Measure(ctx, run, udid)
		if err != nil {
			return Memory{}, err
		}
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].MB() < all[j].MB() })
	return all[len(all)/2], nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
