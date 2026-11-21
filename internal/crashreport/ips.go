package crashreport

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

type ipsHeader struct {
	AppName    string `json:"app_name"`
	Name       string `json:"name"`
	Timestamp  string `json:"timestamp"`
	BundleID   string `json:"bundleID"`
	BugType    string `json:"bug_type"`
	OSVersion  string `json:"os_version"`
	IncidentID string `json:"incident_id"`
}

var (
	exceptionObjRe = regexp.MustCompile(`"exception"\s*:\s*(\{[^{}]*\})`)
	procNameRe     = regexp.MustCompile(`"procName"\s*:\s*"([^"]*)"`)
	captureTimeRe  = regexp.MustCompile(`"captureTime"\s*:\s*"([^"]*)"`)
	bundleIDBodyRe = regexp.MustCompile(`"CFBundleIdentifier"\s*:\s*"([^"]*)"`)
	tsFileRe       = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})-(\d{2})(\d{2})(\d{2})`)
)

var tsLayouts = []string{
	"2006-01-02 15:04:05.00 -0700",
	"2006-01-02 15:04:05.000 -0700",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05.00",
	"2006-01-02 15:04:05",
	time.RFC3339,
}

func parseTS(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range tsLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// bugTypeKind maps an Apple bug_type to a coarse kind.
func bugTypeKind(bt string) string {
	switch bt {
	case "298":
		return "jetsam"
	case "309", "109", "":
		return "crash"
	case "385", "288":
		return "stacks"
	}
	return "other"
}

// ParseIPS parses an Apple report. The newer .ips format is a one-line JSON
// header followed by a JSON body; the older .crash format is plain text.
// text may be a truncated prefix of the report; missing fields stay empty.
func ParseIPS(name, text string) Report {
	r := Report{Name: name, Platform: "ios", Source: "ips", Size: len(text), Kind: "crash"}
	first, rest, _ := strings.Cut(text, "\n")
	first = strings.TrimSpace(first)
	var h ipsHeader
	if strings.HasPrefix(first, "{") && json.Unmarshal([]byte(first), &h) == nil && (h.BugType != "" || h.AppName != "" || h.Timestamp != "") {
		r.App = firstNonEmpty(h.AppName, h.Name)
		r.BundleID = h.BundleID
		r.OSVersion = h.OSVersion
		r.Kind = bugTypeKind(h.BugType)
		r.Timestamp = parseTS(h.Timestamp)
		if m := exceptionObjRe.FindStringSubmatch(rest); m != nil {
			var ex struct {
				Type   string `json:"type"`
				Signal string `json:"signal"`
			}
			if json.Unmarshal([]byte(m[1]), &ex) == nil {
				r.ExceptionType, r.Signal = ex.Type, ex.Signal
			}
		}
		if m := procNameRe.FindStringSubmatch(rest); m != nil {
			r.Process = m[1]
		}
		if r.BundleID == "" {
			if m := bundleIDBodyRe.FindStringSubmatch(rest); m != nil {
				r.BundleID = m[1]
			}
		}
		if r.Timestamp.IsZero() {
			if m := captureTimeRe.FindStringSubmatch(rest); m != nil {
				r.Timestamp = parseTS(m[1])
			}
		}
	} else {
		parseCrashText(&r, text)
	}
	if r.Process == "" {
		r.Process = r.App
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = timeFromName(name)
	}
	return r
}

// parseCrashText fills r from the legacy ".crash" header lines.
func parseCrashText(r *Report, text string) {
	r.Source = "crash"
	for i, line := range strings.Split(text, "\n") {
		if i > 60 {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Process":
			if j := strings.Index(v, " ["); j > 0 {
				v = v[:j]
			}
			r.Process = v
			r.App = v
		case "Identifier":
			r.BundleID = v
		case "Date/Time":
			r.Timestamp = parseTS(v)
		case "OS Version":
			r.OSVersion = v
		case "Exception Type":
			t, sig, _ := strings.Cut(v, " (")
			r.ExceptionType = strings.TrimSpace(t)
			r.Signal = strings.TrimSuffix(sig, ")")
		}
	}
}

func timeFromName(name string) time.Time {
	m := tsFileRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+"-"+m[2]+"-"+m[3]+" "+m[4]+":"+m[5]+":"+m[6], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
