package update

import (
	"strconv"
	"strings"
)

type semver struct {
	major, minor, patch uint64
	pre                 []string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 { // build metadata is ignored
		s = s[:i]
	}
	var v semver
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre := s[i+1:]
		if pre == "" {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
		for _, p := range v.pre {
			if p == "" {
				return v, false
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := [3]uint64{}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || p == "" || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, true
}

// ValidVersion reports whether s is a semantic version ("v" prefix allowed).
func ValidVersion(s string) bool { _, ok := parseSemver(s); return ok }

// CompareVersions returns -1, 0 or 1 as a is older than, equal to or newer than b.
// Prereleases sort before their release (0.1.0-dev < 0.1.0). Invalid versions
// sort before valid ones.
func CompareVersions(a, b string) int {
	va, oka := parseSemver(a)
	vb, okb := parseSemver(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for _, p := range [][2]uint64{{va.major, vb.major}, {va.minor, vb.minor}, {va.patch, vb.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0
	case len(va.pre) == 0:
		return 1
	case len(vb.pre) == 0:
		return -1
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := comparePre(va.pre[i], vb.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1
	case len(va.pre) > len(vb.pre):
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		if na < nb {
			return -1
		} else if na > nb {
			return 1
		}
		return 0
	case ea == nil:
		return -1 // numeric identifiers sort before alphanumeric
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}
