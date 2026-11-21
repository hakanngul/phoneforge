package ocr

import (
	"fmt"
	"regexp"
	"strings"
)

// FindOptions controls Find matching.
type FindOptions struct {
	Exact         bool // whole-text equality instead of substring
	CaseSensitive bool
	Regex         bool // query is a regular expression (Exact ignored)
}

// Find returns results whose text matches query, in input order.
func Find(results []Result, query string, opts FindOptions) ([]Result, error) {
	var match func(string) bool
	switch {
	case opts.Regex:
		pat := query
		if !opts.CaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("ocr: bad regex: %w", err)
		}
		match = re.MatchString
	default:
		q := query
		norm := func(s string) string { return strings.TrimSpace(s) }
		if !opts.CaseSensitive {
			norm = func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
		}
		q = norm(q)
		if opts.Exact {
			match = func(s string) bool { return norm(s) == q }
		} else {
			match = func(s string) bool { return strings.Contains(norm(s), q) }
		}
	}
	var out []Result
	for _, r := range results {
		if match(r.Text) {
			out = append(out, r)
		}
	}
	return out, nil
}
