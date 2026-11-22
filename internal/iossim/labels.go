package iossim

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pocketlab/internal/iossim/slim"
)

// LabelInfo is one launchd label inside a simulator joined with the memory of
// its process (the hidden `pocketlab sim slim <udid> --discover` output and the
// input for building profiles).
type LabelInfo struct {
	Label       string   `json:"label"`
	Domain      string   `json:"domain"` // user | system
	PID         int      `json:"pid,omitempty"`
	RSSMB       float64  `json:"rssMB"`
	FootprintMB float64  `json:"footprintMB"`
	Groups      []string `json:"groups,omitempty"` // profile groups that target it
	Kept        bool     `json:"kept,omitempty"`   // on the hard keep list
	Disabled    bool     `json:"disabled,omitempty"`
}

var fpPerPIDRe = regexp.MustCompile(`\[(\d+)\]: .*Footprint:\s+(\d+) B`)

// ParseFootprintPerPID maps pid to bytes from footprint output.
func ParseFootprintPerPID(data []byte) map[int]int64 {
	out := map[int]int64{}
	for _, m := range fpPerPIDRe.FindAllSubmatch(data, -1) {
		pid, _ := strconv.Atoi(string(m[1]))
		n, _ := strconv.ParseInt(string(m[2]), 10, 64)
		out[pid] = n
	}
	return out
}

// ParseDomainServices returns pid->label rows from the `services = {` block of
// `launchctl print <domain>` output.
func ParseDomainServices(data []byte) []struct {
	PID   int
	Label string
} {
	var out []struct {
		PID   int
		Label string
	}
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.TrimSpace(line) == "services = {":
			in = true
		case in && strings.TrimRight(line, " \t") == "\t}":
			return out
		case in:
			f := strings.Fields(line)
			if len(f) >= 3 {
				pid, _ := strconv.Atoi(f[0])
				out = append(out, struct {
					PID   int
					Label string
				}{pid, f[2]})
			}
		}
	}
	return out
}

// Discover lists the launchd labels of a booted simulator (user domain via
// `launchctl list`, plus the system domain from `print system`) joined with
// per-process RSS and footprint, ranked by footprint.
func Discover(ctx context.Context, run Runner, host HostRunner, udid, runtimeID string) ([]LabelInfo, error) {
	sp := func(args ...string) ([]byte, error) {
		return run(ctx, append([]string{"spawn", udid, "launchctl"}, args...)...)
	}
	uidOut, err := sp("manageruid")
	if err != nil {
		return nil, err
	}
	uid := strings.TrimSpace(string(uidOut))
	listOut, err := sp("list")
	if err != nil {
		return nil, err
	}
	sysOut, _ := sp("print", "system")
	disOut, _ := sp("print-disabled", "user/"+uid)
	disabled := slim.ParseDisabled(disOut)

	rows := map[string]*LabelInfo{}
	var order []string
	add := func(domain string, pid int, label string) {
		if _, ok := rows[label]; ok {
			return
		}
		rows[label] = &LabelInfo{Label: label, Domain: domain, PID: pid, Kept: slim.IsKept(label), Disabled: disabled[label]}
		order = append(order, label)
	}
	for i, line := range strings.Split(string(listOut), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || (i == 0 && f[0] == "PID") {
			continue
		}
		pid := 0
		if f[0] != "-" {
			pid, _ = strconv.Atoi(f[0])
		}
		add("user", pid, f[2])
	}
	for _, s := range ParseDomainServices(sysOut) {
		add("system", s.PID, s.Label)
	}
	if p, err := slim.Load(); err == nil {
		g := p.LabelGroups(runtimeID)
		for l, r := range rows {
			r.Groups = g[l]
		}
	}
	// Join memory: RSS from ps, footprint from footprint(1) over all pids.
	if psOut, err := host(ctx, "ps", "-axo", "pid=,ppid=,rss=,command="); err == nil || len(psOut) > 0 {
		rss := map[int]int64{}
		for _, p := range ParsePS(psOut) {
			rss[p.PID] = p.RSSKB
		}
		var pids []string
		for _, r := range rows {
			if r.PID > 0 {
				r.RSSMB = float64(rss[r.PID]) / 1024
				pids = append(pids, strconv.Itoa(r.PID))
			}
		}
		if len(pids) > 0 {
			sort.Strings(pids)
			args := append([]string{"-f", "bytes", "--noCategories"}, pids...)
			if fo, _ := host(ctx, "footprint", args...); len(fo) > 0 {
				fp := ParseFootprintPerPID(fo)
				for _, r := range rows {
					r.FootprintMB = float64(fp[r.PID]) / (1 << 20)
				}
			}
		}
	}
	out := make([]LabelInfo, 0, len(order))
	for _, l := range order {
		out = append(out, *rows[l])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FootprintMB != b.FootprintMB {
			return a.FootprintMB > b.FootprintMB
		}
		if a.RSSMB != b.RSSMB {
			return a.RSSMB > b.RSSMB
		}
		return a.Label < b.Label
	})
	return out, nil
}
