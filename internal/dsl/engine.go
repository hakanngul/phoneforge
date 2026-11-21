package dsl

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"phoneforge/internal/device"
)

// Timing knobs (variables so tests can shorten them).
var (
	waitPollInterval = 250 * time.Millisecond
	scrollSettle     = 400 * time.Millisecond
	retryDelay       = 1000 * time.Millisecond
	alertPause       = 300 * time.Millisecond
	settleStable     = 500 * time.Millisecond
)

// outcome is what one attempt of a step produced.
type outcome struct {
	matched    *device.Rect
	warnings   []string
	result     map[string]any
	screenshot string
	cond       *bool
	thenR      []StepResult
	elseR      []StepResult
	bodyR      []StepResult
	iterations *int
	err        error
}

func (o *outcome) set(k string, v any) {
	if o.result == nil {
		o.result = map[string]any{}
	}
	o.result[k] = v
}

func (o *outcome) warn(format string, args ...any) {
	o.warnings = append(o.warnings, fmt.Sprintf(format, args...))
}

type runner struct {
	dev     device.Device
	script  Script
	opts    Options
	vars    map[string]string
	start   time.Time
	mode    string
	settle  bool
	initial *snapshot
	lastObs *snapshot
	checks  []Checkpoint
	curIdx  int
}

// Execute parses script and runs it against dev. A non-nil error is returned
// only for a malformed script; step failures are reported in Result.
func Execute(ctx context.Context, dev device.Device, script []byte) (Result, error) {
	s, err := Parse(script)
	if err != nil {
		return Result{}, err
	}
	return ExecuteScript(ctx, dev, s, Options{})
}

// ExecuteScript runs an already parsed script.
func ExecuteScript(ctx context.Context, dev device.Device, s Script, opts Options) (Result, error) {
	if err := Validate(s); err != nil {
		return Result{}, err
	}
	vars, err := scriptVars(s, opts.Params)
	if err != nil {
		return Result{}, err
	}
	r := &runner{dev: dev, script: s, opts: opts, vars: vars, start: time.Now(), mode: s.Mode, settle: s.Settle}
	if opts.Mode != "" {
		r.mode = opts.Mode
	}
	if opts.Settle != nil {
		r.settle = *opts.Settle
	}
	if r.mode == "" {
		r.mode = "explore"
	}
	vars["repeat_index"] = "0"
	if usesAction(s.Steps, "assert_screen_changed") {
		if snap, err := r.takeSnapshot(ctx, true, true); err == nil {
			r.initial = snap
		}
	}
	steps, stop := r.execList(ctx, s.Steps, true, false)
	res := Result{Passed: stop == "", Steps: steps, Checkpoints: r.checks, TotalMs: time.Since(r.start).Milliseconds()}
	switch stop {
	case "":
		res.Status = "passed"
	case StatusNeedsReplan, StatusNeedsUser:
		res.Status = stop
	default:
		res.Status = "failed"
	}
	delete(vars, "repeat_index")
	if len(vars) > 0 {
		res.Storage = vars
	}
	if res.Steps == nil {
		res.Steps = []StepResult{}
	}
	return res, nil
}

func usesAction(steps []Step, canon string) bool {
	for _, st := range steps {
		if c, _ := canonicalAction(st.Action); c == canon {
			return true
		}
		if usesAction(st.Then, canon) || usesAction(st.Else, canon) || usesAction(st.Body, canon) {
			return true
		}
	}
	return false
}

// execList runs steps in order. It returns the results and a stop reason:
// "" (all done), StatusFailed, StatusNeedsReplan or StatusNeedsUser.
// force makes every step abort on failure (used inside if_exists).
func (r *runner) execList(ctx context.Context, steps []Step, top, force bool) ([]StepResult, string) {
	lastObserve := -1
	if top && r.mode == "explore" {
		for i, st := range steps {
			if c, _ := canonicalAction(st.Action); c == "observe" {
				lastObserve = i
			}
		}
	}
	res := make([]StepResult, 0, len(steps))
	stop := ""
	for i, st := range steps {
		if stop != "" {
			res = append(res, StepResult{Index: i, ID: st.ID, Action: st.Action, Status: StatusSkipped})
			continue
		}
		if top && r.mode == "explore" && i != lastObserve {
			if c, _ := canonicalAction(st.Action); c == "observe" {
				res = append(res, StepResult{Index: i, ID: st.ID, Action: st.Action, Status: StatusSkipped,
					Warnings: []string{"explore mode runs only the last observe"}})
				continue
			}
		}
		if top {
			r.curIdx = i
		}
		sr := r.execStep(ctx, i, st, force)
		switch sr.Status {
		case StatusFailed:
			stop = StatusFailed
		case StatusNeedsReplan, StatusNeedsUser:
			stop = sr.Status
		case StatusSkipped:
			if ctx.Err() != nil {
				stop = StatusFailed
			}
		}
		res = append(res, sr)
	}
	return res, stop
}

func (r *runner) platformMatches(p string) bool {
	if p == "" {
		return true
	}
	plat := r.dev.Info().Platform
	if p == "ios" {
		return plat == device.PlatformIOS || plat == device.PlatformSimulator
	}
	return plat == device.Platform(p)
}

func (r *runner) policy(st Step) (strategy string, retries int, delay time.Duration, fallback string) {
	p := st.OnFail
	if p == nil {
		p = r.script.OnFail
	}
	strategy, fallback = OnFailAbort, OnFailAbort
	retries = defaultMaxRetries
	delay = retryDelay
	if r.script.Defaults.Retries > 0 {
		retries = r.script.Defaults.Retries
	}
	if p != nil {
		if p.Strategy != "" {
			strategy = p.Strategy
		}
		if p.FallbackStrategy != "" {
			fallback = p.FallbackStrategy
		}
		if p.MaxRetries != nil {
			retries = *p.MaxRetries
		}
		if p.RetryDelayMs != nil {
			delay = msDuration(*p.RetryDelayMs)
		}
	}
	if st.Retries != nil {
		retries = *st.Retries
	}
	if st.RetryDelayMs != nil {
		delay = msDuration(*st.RetryDelayMs)
	}
	if retries < 0 {
		retries = 0
	}
	return
}

func (r *runner) execStep(ctx context.Context, idx int, st Step, force bool) StepResult {
	began := time.Now()
	sr := StepResult{Index: idx, ID: st.ID, Action: st.Action}
	if !r.platformMatches(st.Platform) {
		sr.Status = StatusSkipped
		sr.Warnings = []string{"skipped: step is for platform " + st.Platform}
		return sr
	}
	canon, _ := canonicalAction(st.Action)
	strategy, retries, delay, fallback := r.policy(st)
	if force {
		strategy = OnFailAbort
	}
	attempts := 1
	if strategy == OnFailRetry {
		attempts += retries
	}
	var oc *outcome
	alertTried := false
	for a := 0; a < attempts; a++ {
		if cerr := ctx.Err(); cerr != nil {
			oc = &outcome{err: cerr}
			break
		}
		oc = r.attempt(ctx, canon, st)
		if oc.err != nil && ctx.Err() == nil && r.script.Alerts != "" && !alertTried && alertable(canon) {
			alertTried = true
			if r.handleAlerts(ctx) {
				oc = r.attempt(ctx, canon, st)
			}
		}
		if oc.err == nil || ctx.Err() != nil {
			break
		}
		if a < attempts-1 {
			if serr := sleep(ctx, delay); serr != nil {
				oc.err = serr
				break
			}
		}
	}
	sr.Matched, sr.Warnings, sr.Result = oc.matched, oc.warnings, oc.result
	sr.ConditionMet, sr.ThenResults, sr.ElseResults = oc.cond, oc.thenR, oc.elseR
	sr.BodyResults, sr.Iterations = oc.bodyR, oc.iterations
	sr.Screenshot = oc.screenshot
	if oc.err == nil {
		sr.Status = StatusOK
		if changesUI(canon) {
			if serr := sleep(ctx, settleDuration(r.script.Defaults, st)); serr != nil {
				sr.Status, sr.Error, sr.Code = StatusFailed, serr.Error(), classify(serr)
			}
		}
		sr.DurationMs = time.Since(began).Milliseconds()
		return sr
	}
	sr.Error = oc.err.Error()
	sr.Code = classify(oc.err)
	final := strategy
	if strategy == OnFailRetry {
		final = fallback
		if final == OnFailRetry {
			final = OnFailAbort
		}
	}
	switch {
	case ctx.Err() != nil:
		sr.Status = StatusFailed
	case final == OnFailSkip:
		sr.Status = StatusSkipped
	case final == OnFailReplan:
		sr.Status = StatusNeedsReplan
	case final == OnFailRequireUser:
		sr.Status = StatusNeedsUser
	default:
		sr.Status = StatusFailed
	}
	if sr.Status != StatusSkipped {
		if !r.opts.NoFailureScreenshot && sr.Screenshot == "" {
			sr.Screenshot = r.failureScreenshot()
		}
		if r.mode == "explore" {
			sr.Debug = r.debugInfo()
		}
	}
	sr.DurationMs = time.Since(began).Milliseconds()
	return sr
}

// alertable reports whether a failed step may be retried after dismissing a
// system alert (waits and assertions are about the screen, not about input).
func alertable(canon string) bool {
	switch canon {
	case "wait", "checkpoint", "assert_not_exists", "if_exists", "repeat":
		return false
	}
	return true
}

func (r *runner) stepLimit(canon string, st Step) time.Duration {
	def := defaultStepLimitMs
	if r.script.Defaults.TimeoutMs > 0 {
		def = r.script.Defaults.TimeoutMs
	}
	limit := def
	switch canon {
	case "wait_for", "wait_stable", "assert_exists", "assert_not_exists", "assert_text_ocr", "web_wait":
		if st.TimeoutMs+10000 > limit {
			limit = st.TimeoutMs + 10000
		}
	case "wait":
		if d := waitMs(st) + 10000; d > limit {
			limit = d
		}
	default:
		if st.TimeoutMs > 0 {
			limit = st.TimeoutMs
		}
	}
	return time.Duration(limit) * time.Millisecond
}

// attempt substitutes variables and runs the step once under its time limit.
func (r *runner) attempt(ctx context.Context, canon string, st Step) *outcome {
	oc := &outcome{}
	sub, err := Substitute(st, r.vars, true)
	if err != nil {
		oc.err = withCode(CodeValidation, err)
		return oc
	}
	limit := r.stepLimit(canon, sub)
	actx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	err = r.do(actx, canon, sub, oc)
	if err != nil && ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		err = codeErr(CodeTimeout, "step timed out after %dms: %v", limit.Milliseconds(), err)
	}
	oc.err = err
	return oc
}

func changesUI(canon string) bool {
	switch canon {
	case "launch_app", "tap", "long_press", "type", "type_secret", "swipe", "press_key",
		"double_tap", "two_finger_tap", "pinch", "drag", "drag_path", "open_url", "dismiss_keyboard",
		"scroll", "clear", "paste_text", "navigate", "toggle", "select_picker", "ocr_tap":
		return true
	}
	return false
}

func (r *runner) failureScreenshot() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shot, err := r.dev.Screenshot(ctx)
	if err != nil || len(shot.PNG) == 0 {
		return ""
	}
	return b64(shot.PNG)
}

func (r *runner) debugInfo() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tree, err := r.dev.UITree(ctx)
	if err != nil {
		return nil
	}
	nodes := flatten(tree)
	var texts []string
	for _, n := range nodes {
		if t := textOf(n.el); t != "" && isVisible(n.el, tree.Bounds) {
			texts = append(texts, t)
			if len(texts) >= 40 {
				break
			}
		}
	}
	return map[string]any{"elements": len(nodes), "visibleText": texts}
}

// handleAlerts dismisses or accepts up to three system alerts in a row. It
// only touches system dialogs: Android permission / system UI buttons and
// buttons inside an iOS alert container; app-owned dialogs are left alone.
func (r *runner) handleAlerts(ctx context.Context) bool {
	accept := r.script.Alerts == "accept"
	handled := false
	for i := 0; i < 3; i++ {
		tree, err := r.dev.UITree(ctx)
		if err != nil {
			return handled
		}
		nodes := flatten(tree)
		hit := -1
		for j, n := range nodes {
			if isAlertButton(nodes, j, n.el, accept) {
				hit = j
				break
			}
		}
		if hit < 0 {
			return handled
		}
		x, y := nodes[hit].el.Bounds.Center()
		if err := r.dev.Tap(ctx, x, y); err != nil {
			return handled
		}
		handled = true
		if sleep(ctx, alertPause) != nil {
			return handled
		}
	}
	return handled
}

var (
	acceptLabels  = []string{"allow", "allow while using app", "while using the app", "ok", "accept", "allow once", "only this time"}
	dismissLabels = []string{"don't allow", "don’t allow", "deny", "cancel", "not now", "dismiss"}
	acceptIDs     = []string{"permission_allow_button", "permission_allow_foreground_only_button", "permission_allow_one_time_button", "android:id/button1"}
	dismissIDs    = []string{"permission_deny_button", "permission_deny_and_dont_ask_again_button", "android:id/button2"}
	systemIDPre   = []string{"com.android.permissioncontroller:", "com.google.android.permissioncontroller:", "com.android.packageinstaller:", "android:id/", "com.android.systemui:"}
)

func isAlertButton(nodes []node, i int, e device.Element, accept bool) bool {
	labels, ids := dismissLabels, dismissIDs
	if accept {
		labels, ids = acceptLabels, acceptIDs
	}
	for _, id := range ids {
		if e.ResourceID == id || strings.HasSuffix(e.ResourceID, ":id/"+id) || strings.HasSuffix(e.ResourceID, "/"+id) {
			return true
		}
	}
	text := strings.ToLower(textOf(e))
	if text == "" {
		return false
	}
	match := false
	for _, l := range labels {
		if text == l {
			match = true
		}
	}
	if !match {
		return false
	}
	for _, p := range systemIDPre {
		if strings.HasPrefix(e.ResourceID, p) {
			return true
		}
	}
	for p := nodes[i].parent; p >= 0; p = nodes[p].parent {
		if strings.Contains(strings.ToLower(nodes[p].el.Role), "alert") {
			return true
		}
	}
	return false
}

// sleep waits for dur or until ctx is done, returning ctx.Err() in the latter case.
func sleep(ctx context.Context, dur time.Duration) error {
	if dur <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func msDuration(ms int) time.Duration {
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}

func b64(p []byte) string { return base64.StdEncoding.EncodeToString(p) }

func waitMs(st Step) int {
	if st.Ms > 0 {
		return st.Ms
	}
	return st.DurationMs
}
