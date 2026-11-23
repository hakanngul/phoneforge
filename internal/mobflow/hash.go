package mobflow

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// StepHash identifies the semantic content of a step: type, do, expect and the
// names of the declared parameters (paramNames) the step references. The title
// is excluded so renaming a step does not invalidate its recorded segment.
func StepHash(s Step, paramNames []string) string {
	declared := make(map[string]bool, len(paramNames))
	for _, n := range paramNames {
		declared[n] = true
	}
	used := []string{}
	for _, n := range ParamRefs(s.Do + "\x00" + s.Expect) {
		if declared[n] {
			used = append(used, n)
		}
	}
	sort.Strings(used)
	return hashParts(s.Type, s.Do, s.Expect, strings.Join(used, ","))
}

// CaseHash covers what makes every cached segment invalid when it changes: the
// app, the secret ids and the ordered step ids.
func CaseHash(c *Case) string {
	secrets := append([]string(nil), c.Secrets...)
	sort.Strings(secrets)
	ids := make([]string, len(c.Steps))
	for i, s := range c.Steps {
		ids[i] = s.ID
	}
	return hashParts(c.App.BundleID, c.App.Platform, strings.Join(secrets, ","), strings.Join(ids, ","))
}

// StepHashes returns the current hash of every step, keyed by step id.
func (c *Case) StepHashes() map[string]string {
	names := c.ParamNames()
	out := make(map[string]string, len(c.Steps))
	for _, s := range c.Steps {
		out[s.ID] = StepHash(s, names)
	}
	return out
}

func hashParts(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}
