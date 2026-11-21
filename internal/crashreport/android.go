package crashreport

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const androidTimeout = 30 * time.Second

// runFn runs adb with args (serial already applied) and returns stdout.
type runFn func(ctx context.Context, args ...string) (string, error)

type androidProvider struct {
	run runFn
}

func newAndroidProvider(adb, serial string) Provider {
	return &androidProvider{run: func(ctx context.Context, args ...string) (string, error) {
		full := append([]string{"-s", serial}, args...)
		cmd := exec.CommandContext(ctx, adb, full...)
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return string(out), fmt.Errorf("adb %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
			}
			return string(out), fmt.Errorf("adb %s: %w", strings.Join(args, " "), err)
		}
		return string(out), nil
	}}
}

type entry struct {
	Report
	Text string
}

func (p *androidProvider) List(ctx context.Context) ([]Report, error) {
	es, err := p.collect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Report, len(es))
	for i, e := range es {
		out[i] = e.Report
	}
	return out, nil
}

func (p *androidProvider) Get(ctx context.Context, name string) (string, error) {
	es, err := p.collect(ctx)
	if err != nil {
		return "", err
	}
	for _, e := range es {
		if e.Name == name {
			return e.Text, nil
		}
	}
	return "", ErrNotFound
}

// Delete is unsupported: Android crash data is read from system buffers.
func (p *androidProvider) Delete(ctx context.Context, name string) error {
	return errors.New("crashreport: Android crash reports cannot be deleted (they live in system log buffers)")
}

// collect gathers entries from every readable source; it only fails when all
// sources fail.
func (p *androidProvider) collect(ctx context.Context) ([]entry, error) {
	ctx, cancel := context.WithTimeout(ctx, androidTimeout)
	defer cancel()
	loc := p.deviceZone(ctx)
	var all []entry
	var errs []string
	ok := 0

	if out, err := p.run(ctx, "logcat", "-b", "crash", "-d", "-v", "threadtime", "-v", "year"); err != nil {
		errs = append(errs, err.Error())
	} else {
		ok++
		all = append(all, ParseLogcatCrashes(out, loc, time.Now())...)
	}
	for _, tag := range []string{"data_app_crash", "data_app_native_crash", "data_app_anr"} {
		out, err := p.run(ctx, "shell", "dumpsys", "dropbox", "--print", tag)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ok++
		all = append(all, ParseDropbox(out, loc)...)
	}
	all = append(all, p.tombstones(ctx, loc)...)
	if ok == 0 {
		return nil, fmt.Errorf("android crash sources unavailable: %s", strings.Join(errs, "; "))
	}
	return all, nil
}

// deviceZone returns the device's UTC offset as a fixed zone (host local on failure).
func (p *androidProvider) deviceZone(ctx context.Context) *time.Location {
	out, err := p.run(ctx, "shell", "date", "+%z")
	z := strings.TrimSpace(out)
	if err != nil || len(z) != 5 {
		return time.Local
	}
	h, e1 := strconv.Atoi(z[1:3])
	m, e2 := strconv.Atoi(z[3:5])
	if e1 != nil || e2 != nil {
		return time.Local
	}
	off := h*3600 + m*60
	if z[0] == '-' {
		off = -off
	}
	return time.FixedZone(z, off)
}

func (p *androidProvider) tombstones(ctx context.Context, loc *time.Location) []entry {
	out, err := p.run(ctx, "shell", "ls", "/data/tombstones")
	if err != nil || strings.Contains(out, "Permission denied") {
		return nil // needs root on most devices
	}
	var es []entry
	for _, f := range strings.Fields(out) {
		if !strings.HasPrefix(f, "tombstone_") || strings.HasSuffix(f, ".pb") || len(es) >= 30 {
			continue
		}
		txt, err := p.run(ctx, "shell", "head", "-c", "200000", "/data/tombstones/"+f)
		if err != nil {
			continue
		}
		r := Report{Name: "tombstone-" + f, Platform: "android", Source: "tombstone", Kind: "crash", Size: len(txt)}
		for _, line := range strings.Split(txt, "\n") {
			switch {
			case strings.HasPrefix(line, "Timestamp: "):
				r.Timestamp = parseTSIn(strings.TrimSpace(strings.TrimPrefix(line, "Timestamp: ")), loc)
			case strings.HasPrefix(line, "Cmdline: "):
				r.Process = strings.TrimSpace(strings.TrimPrefix(line, "Cmdline: "))
			case strings.HasPrefix(line, "signal "):
				r.Signal = signalFrom(line)
			}
		}
		r.BundleID = r.Process
		es = append(es, entry{r, txt})
	}
	return es
}

func parseTSIn(s string, loc *time.Location) time.Time {
	for _, l := range []string{"2006-01-02 15:04:05.999999999-0700", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

var (
	logLineRe = regexp.MustCompile(`^(?:(\d{4})-)?(\d\d)-(\d\d) (\d\d):(\d\d):(\d\d)\.(\d{3})\s+(\d+)\s+(\d+)\s+([VDIWEFS])\s+(.*?)\s*:\s?(.*)$`)
	sigRe     = regexp.MustCompile(`signal \d+ \((SIG[A-Z0-9]+)`)
	javaExcRe = regexp.MustCompile(`^([A-Za-z_][\w.$]*(?:Exception|Error|Throwable))\b`)
	procRe    = regexp.MustCompile(`Process:\s*([^,\s]+)`)
	fatalSig  = regexp.MustCompile(`Fatal signal \d+ \((SIG[A-Z0-9]+)\).*>>> (\S+) <<<`)
)

func signalFrom(s string) string {
	if m := sigRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

type logLine struct {
	t        time.Time
	pid, tag string
	msg, raw string
}

func parseLogLine(line string, loc *time.Location, now time.Time) (logLine, bool) {
	m := logLineRe.FindStringSubmatch(line)
	if m == nil {
		return logLine{}, false
	}
	year := now.In(loc).Year()
	if m[1] != "" {
		year, _ = strconv.Atoi(m[1])
	}
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	hh, _ := strconv.Atoi(m[4])
	mi, _ := strconv.Atoi(m[5])
	ss, _ := strconv.Atoi(m[6])
	ms, _ := strconv.Atoi(m[7])
	t := time.Date(year, time.Month(mo), d, hh, mi, ss, ms*int(time.Millisecond), loc)
	if m[1] == "" && t.After(now.Add(24*time.Hour)) {
		t = t.AddDate(-1, 0, 0) // logcat without year: crash from late last year
	}
	return logLine{t: t, pid: m[8], tag: m[11], msg: m[12], raw: line}, true
}

// ParseLogcatCrashes splits `logcat -b crash -d` output into one entry per
// Java FATAL EXCEPTION / native tombstone / libc fatal signal.
func ParseLogcatCrashes(out string, loc *time.Location, now time.Time) []entry {
	if loc == nil {
		loc = time.Local
	}
	type group struct {
		start  logLine
		lines  []string
		native bool
	}
	var groups []*group
	var cur *group
	for _, raw := range strings.Split(out, "\n") {
		raw = strings.TrimRight(raw, "\r")
		l, ok := parseLogLine(raw, loc, now)
		if !ok {
			continue
		}
		startJava := l.tag == "AndroidRuntime" && strings.HasPrefix(l.msg, "FATAL EXCEPTION")
		startNative := l.tag == "DEBUG" && strings.HasPrefix(l.msg, "*** ***")
		startLibc := l.tag == "libc" && strings.HasPrefix(l.msg, "Fatal signal")
		switch {
		case startJava || startNative || startLibc:
			cur = &group{start: l, native: !startJava}
			groups = append(groups, cur)
		case cur != nil && l.pid == cur.start.pid && (l.tag == cur.start.tag || (cur.native && l.tag == "DEBUG")):
		default:
			if !(startLibc) {
				cur = nil
			}
			continue
		}
		cur.lines = append(cur.lines, raw)
	}
	var es []entry
	seen := map[string]int{}
	for _, g := range groups {
		text := strings.Join(g.lines, "\n")
		r := Report{Platform: "android", Source: "logcat", Kind: "crash", Timestamp: g.start.t, Size: len(text)}
		for _, line := range g.lines {
			l, _ := parseLogLine(line, loc, now)
			msg := l.msg
			if m := procRe.FindStringSubmatch(msg); m != nil && r.Process == "" {
				r.Process = m[1]
			}
			if strings.HasPrefix(msg, "Cmdline: ") && r.Process == "" {
				r.Process = strings.TrimSpace(strings.TrimPrefix(msg, "Cmdline: "))
			}
			if r.ExceptionType == "" && l.tag == "AndroidRuntime" {
				if m := javaExcRe.FindStringSubmatch(msg); m != nil {
					r.ExceptionType = m[1]
				}
			}
			if r.Signal == "" {
				r.Signal = signalFrom(msg)
			}
			if m := fatalSig.FindStringSubmatch(msg); m != nil && r.Process == "" {
				r.Process = m[2]
			}
		}
		r.BundleID = strings.SplitN(r.Process, ":", 2)[0]
		r.App = r.Process
		r.Name = "logcat-" + g.start.t.Format("20060102T150405.000") + "-" + g.start.pid
		if n := seen[r.Name]; n > 0 {
			r.Name += "-" + strconv.Itoa(n)
		}
		seen[r.Name]++
		es = append(es, entry{r, text})
	}
	return es
}

var dropHeaderRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) (\S+) \((\w+), (\d+) bytes\)`)

// ParseDropbox parses `dumpsys dropbox --print <tag>` output.
func ParseDropbox(out string, loc *time.Location) []entry {
	if loc == nil {
		loc = time.Local
	}
	var es []entry
	var cur *entry
	var body []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
		cur.Size = len(cur.Text)
		es = append(es, *cur)
		cur, body = nil, nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := dropHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			t, _ := time.ParseInLocation("2006-01-02 15:04:05", m[1], loc)
			kind := "crash"
			if strings.Contains(m[2], "anr") {
				kind = "anr"
			}
			cur = &entry{Report: Report{
				Name: "dropbox-" + m[2] + "-" + strconv.FormatInt(t.Unix(), 10), Platform: "android",
				Source: "dropbox", Kind: kind, Timestamp: t,
			}}
			body = []string{line}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "=====") {
			flush()
			continue
		}
		body = append(body, line)
		if m := procRe.FindStringSubmatch(line); m != nil && cur.Process == "" {
			cur.Process = m[1]
			cur.BundleID = strings.SplitN(m[1], ":", 2)[0]
			cur.App = m[1]
		}
		if cur.ExceptionType == "" {
			if m := javaExcRe.FindStringSubmatch(line); m != nil {
				cur.ExceptionType = m[1]
			}
		}
		if cur.Signal == "" {
			cur.Signal = signalFrom(line)
		}
	}
	flush()
	return es
}
