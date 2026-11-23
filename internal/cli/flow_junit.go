package cli

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pocketlab/internal/mobflow"
)

type junitSuites struct {
	XMLName xml.Name    `xml:"testsuites"`
	Suites  []junitSuit `xml:"testsuite"`
}

type junitSuit struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Skipped   int         `xml:"skipped,attr"`
	Time      string      `xml:"time,attr"`
	Timestamp string      `xml:"timestamp,attr"`
	Cases     []junitCase `xml:"testcase"`
	Out       string      `xml:"system-out,omitempty"`
}

type junitCase struct {
	Name    string        `xml:"name,attr"`
	Class   string        `xml:"classname,attr"`
	Time    string        `xml:"time,attr"`
	Failure *junitFailure `xml:"failure,omitempty"`
	Skipped *struct{}     `xml:"skipped,omitempty"`
	Out     string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

func secs(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }

// writeJUnit writes a JUnit XML report of run: one testsuite named after the
// case, one testcase per step. The screenshot of a failed step is downloaded
// next to the report (<report>.screenshots/); when that fails, the failure
// names the server artifact instead.
func (c *call) writeJUnit(path string, run *mobflow.Run) error {
	suite := junitSuit{
		Name: run.CaseID, Timestamp: run.StartedAt.UTC().Format(time.RFC3339),
	}
	var total int64
	for _, st := range run.Steps {
		total += st.DurationMs
		tc := junitCase{Name: st.ID + ": " + firstNonEmpty(st.Title, st.Type), Class: run.CaseID, Time: secs(st.DurationMs)}
		switch st.State {
		case mobflow.StepFailed:
			msg := firstNonEmpty(st.Error, "step failed")
			tc.Failure = &junitFailure{Message: msg, Type: "StepFailed", Text: msg}
			shot := c.junitShot(path, run, st)
			if shot != "" {
				tc.Failure.Text += "\nscreenshot: " + shot
				tc.Out = "screenshot: " + shot
			}
			suite.Failures++
		case mobflow.StepSkipped, mobflow.StepPending:
			tc.Skipped = &struct{}{}
			suite.Skipped++
		case mobflow.StepHealed:
			tc.Out = "self-healed: the recorded segment was repaired by an agent"
		}
		suite.Cases = append(suite.Cases, tc)
	}
	if run.Status != mobflow.RunPassed && suite.Failures == 0 {
		// The run failed without a failing step (stale cache, cancelled, device error).
		msg := firstNonEmpty(run.Error, "run "+run.Status)
		suite.Cases = append(suite.Cases, junitCase{
			Name: "run", Class: run.CaseID, Time: "0.000",
			Failure: &junitFailure{Message: msg, Type: "Run" + strings.ToUpper(run.Status[:1]) + run.Status[1:], Text: msg},
		})
		suite.Failures++
	}
	suite.Tests = len(suite.Cases)
	suite.Time = secs(total)
	if len(run.Healed) > 0 {
		suite.Out = "healed steps: " + joinIDs(run.Healed)
	}
	b, err := xml.MarshalIndent(junitSuites{Suites: []junitSuit{suite}}, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append([]byte(xml.Header), append(b, '\n')...), 0o644)
}

func (c *call) junitShot(report string, run *mobflow.Run, st mobflow.StepRun) string {
	if st.Screenshot == "" {
		return ""
	}
	ref := fmt.Sprintf("run %s artifact %s", run.ID, st.Screenshot)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	data, _, err := c.client().raw(ctx, "GET", "/flows/runs/"+url.PathEscape(run.ID)+"/artifacts/"+url.PathEscape(st.Screenshot), nil)
	if err != nil || len(data) == 0 {
		return ref
	}
	dir := strings.TrimSuffix(report, filepath.Ext(report)) + ".screenshots"
	if os.MkdirAll(dir, 0o755) != nil {
		return ref
	}
	p := filepath.Join(dir, st.Screenshot)
	if os.WriteFile(p, data, 0o644) != nil {
		return ref
	}
	return p
}
