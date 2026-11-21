package mobtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/dsl"
)

// RunRecord is the persisted outcome of one script run.
type RunRecord struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"projectId,omitempty"`
	Script     string     `json:"script"`
	DeviceID   string     `json:"deviceId"`
	StartedAt  time.Time  `json:"startedAt"`
	DurationMs int64      `json:"durationMs"`
	Passed     bool       `json:"passed"`
	Result     dsl.Result `json:"result"`
}

// RunOptions tune RunWith.
type RunOptions struct {
	// ReadFile resolves `run "./x.mob"` lines (see ParseOptions).
	ReadFile func(name string) ([]byte, error)
	// Secrets resolves type_secret ids.
	Secrets dsl.SecretResolver
	// Params override script params.
	Params map[string]string
}

// DetectKind guesses "mob" or "json" from script text.
func DetectKind(script []byte) string {
	t := strings.TrimSpace(string(script))
	if strings.HasPrefix(t, "{") {
		return "json"
	}
	return "mob"
}

// Run executes script ("mob" or "json" kind; "" detects) on dev through the
// dsl engine. A non-nil error means the script itself is invalid; step
// failures are reported in the record (Passed=false).
func Run(ctx context.Context, dev device.Device, script []byte, kind string) (RunRecord, error) {
	return RunWith(ctx, dev, script, kind, RunOptions{})
}

// RunWith is Run with options.
func RunWith(ctx context.Context, dev device.Device, script []byte, kind string, opts RunOptions) (RunRecord, error) {
	kind = strings.TrimPrefix(strings.ToLower(kind), ".")
	if kind == "" {
		kind = DetectKind(script)
	}
	var (
		s    dsl.Script
		eopt = dsl.Options{Secrets: opts.Secrets, Params: opts.Params}
	)
	switch kind {
	case "mob":
		var errs []ParseError
		s, errs = ParseWith(script, ParseOptions{ReadFile: opts.ReadFile})
		if len(errs) > 0 {
			return RunRecord{}, ParseErrors(errs)
		}
		// .mob test runs use deterministic mode with settle on.
		yes := true
		eopt.Mode, eopt.Settle = "deterministic", &yes
	case "json":
		var err error
		if s, err = dsl.Parse(script); err != nil {
			return RunRecord{}, err
		}
	default:
		return RunRecord{}, fmt.Errorf("unknown script kind %q (use mob or json)", kind)
	}
	started := time.Now().UTC()
	res, err := dsl.ExecuteScript(ctx, dev, s, eopt)
	if err != nil {
		return RunRecord{}, err
	}
	return RunRecord{
		ID:         newRunID(started),
		DeviceID:   dev.Info().ID,
		StartedAt:  started,
		DurationMs: time.Since(started).Milliseconds(),
		Passed:     res.Passed,
		Result:     res,
	}, nil
}

func newRunID(t time.Time) string {
	return fmt.Sprintf("%016x-%s", uint64(t.UnixNano()), randHex(4))
}
