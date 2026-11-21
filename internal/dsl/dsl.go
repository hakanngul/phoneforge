// Package dsl implements PhoneForge's JSON scripting engine, compatible with
// the publicly documented MobAI automation DSL (version 0.2). A script is a
// list of steps executed against a device.Device; predicates are resolved
// against the live UI tree.
package dsl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"phoneforge/internal/device"
)

// Supported script versions. An empty version is treated as the current one.
const (
	// Version is the current DSL version.
	Version = "0.2"

	defaultSettleMs    = 400
	defaultStepLimitMs = 120000
	defaultMaxRetries  = 3
)

// Step status values reported in StepResult.Status.
const (
	// StatusOK marks a step that completed successfully.
	StatusOK = "ok"
	// StatusFailed marks a step that failed.
	StatusFailed = "failed"
	// StatusSkipped marks a step that was not run (or whose failure was ignored via on_fail=skip).
	StatusSkipped = "skipped"
	// StatusNeedsReplan marks a failed step whose strategy is replan: the run
	// stopped so that a caller (an LLM planner) can produce a new plan.
	StatusNeedsReplan = "needs_replan"
	// StatusNeedsUser marks a failed step whose strategy is require_user: the
	// run stopped so that a human can intervene.
	StatusNeedsUser = "needs_user"
)

// Failure-handling strategies for FailPolicy.Strategy.
const (
	// OnFailAbort stops the script on failure (default).
	OnFailAbort = "abort"
	// OnFailSkip records the failure as skipped and continues.
	OnFailSkip = "skip"
	// OnFailRetry re-runs the step up to MaxRetries more times, then applies the fallback.
	OnFailRetry = "retry"
	// OnFailReplan stops the run and reports status needs_replan.
	OnFailReplan = "replan"
	// OnFailRequireUser stops the run and reports status needs_user.
	OnFailRequireUser = "require_user"
)

// Error codes reported in StepResult.Code.
const (
	CodeNoMatch           = "NO_MATCH"
	CodeAmbiguousMatch    = "AMBIGUOUS_MATCH"
	CodeTimeout           = "TIMEOUT"
	CodeAssertionFailed   = "ASSERTION_FAILED"
	CodeValidation        = "VALIDATION_ERROR"
	CodeInvalidAction     = "INVALID_ACTION"
	CodeContextNotAvail   = "CONTEXT_NOT_AVAILABLE"
	CodeSafetyBlocked     = "SAFETY_BLOCKED"
	CodeExecutionError    = "EXECUTION_ERROR"
	webNotSupportedPhrase = "web context not supported yet"
)

// Selector identifies a UI element (a "predicate" in the MobAI docs). All
// non-empty fields must match. Index picks the n-th (0-based) match after
// near / bounds_hint ordering.
type Selector struct {
	Text           string `json:"text,omitempty"`
	TextContains   string `json:"textContains,omitempty"`
	TextStartsWith string `json:"textStartsWith,omitempty"`
	// TextRegex is a Go regular expression ("regex" is accepted as an alias).
	TextRegex     string `json:"textRegex,omitempty"`
	Value         string `json:"value,omitempty"`
	ValueContains string `json:"valueContains,omitempty"`
	ResourceID    string `json:"resourceId,omitempty"`
	// AccessibilityID matches an element's ResourceID, Label, or the part of
	// the ResourceID after "/".
	AccessibilityID string `json:"accessibilityId,omitempty"`
	Label           string `json:"label,omitempty"`
	LabelContains   string `json:"labelContains,omitempty"`
	// Role matches Element.Role exactly.
	Role string `json:"role,omitempty"`
	// Type is the portable element type (button, input, text, image, cell,
	// scrollview, switch, slider, link, icon, "*") or a raw role suffix.
	Type       string    `json:"type,omitempty"`
	Enabled    *bool     `json:"enabled,omitempty"`
	Visible    *bool     `json:"visible,omitempty"`
	Selected   *bool     `json:"selected,omitempty"`
	Near       *Near     `json:"near,omitempty"`
	BoundsHint string    `json:"boundsHint,omitempty"`
	ParentOf   *Selector `json:"parentOf,omitempty"`
	Index      int       `json:"index,omitempty"`
	// CSSSelector targets web contexts, which PhoneForge does not support yet.
	CSSSelector string `json:"cssSelector,omitempty"`
}

// Near restricts a predicate to elements close to an anchor element. The
// anchor is described by the embedded predicate fields.
type Near struct {
	Selector
	// Direction is above, below, left, right or any (default any).
	Direction string `json:"direction,omitempty"`
	// MaxDistance is the largest allowed centre distance in pixels (0 = unlimited).
	MaxDistance float64 `json:"maxDistance,omitempty"`
}

// Defaults holds script-wide step defaults.
type Defaults struct {
	// SettleMs is the post-action settle delay; nil means 400.
	SettleMs *int `json:"settleMs,omitempty"`
	// Retries is the retry count for retry steps that set none.
	Retries int `json:"retries,omitempty"`
	// TimeoutMs is the default step limit and the default wait_for wait.
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// FailPolicy is a failure strategy (on_fail). In JSON it is either a bare
// strategy name or an object.
type FailPolicy struct {
	// Strategy is abort (default), skip, retry, replan or require_user.
	Strategy string `json:"strategy,omitempty"`
	// MaxRetries is the retry count for strategy retry (default 3).
	MaxRetries *int `json:"maxRetries,omitempty"`
	// RetryDelayMs is the pause between retries (default 1000).
	RetryDelayMs *int `json:"retryDelayMs,omitempty"`
	// FallbackStrategy applies when retries run out (default abort).
	FallbackStrategy string `json:"fallbackStrategy,omitempty"`
}

// ParamSpec is a script parameter: an optional default for ${name}.
type ParamSpec struct {
	Default string
	// Has reports whether a default exists; a param without one must be supplied.
	Has bool
}

// PathPoint is one point of a drag_path.
type PathPoint struct {
	X          int `json:"x"`
	Y          int `json:"y"`
	DurationMs int `json:"durationMs,omitempty"`
}

// Step is a single script instruction. Only the fields relevant to Action are used.
type Step struct {
	Action string `json:"action"`
	// ID is echoed back in StepResult.ID.
	ID string `json:"id,omitempty"`
	// Context is "native" (default) or "web" (unsupported), or an array.
	Context json.RawMessage `json:"context,omitempty"`
	// Platform ("ios" or "android") restricts the step to that platform.
	Platform string `json:"platform,omitempty"`

	BundleID    string            `json:"bundleId,omitempty"`
	Fresh       bool              `json:"fresh,omitempty"`
	Arguments   []string          `json:"arguments,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Debug       bool              `json:"debug,omitempty"`
	LogPath     string            `json:"logPath,omitempty"`

	Selector *Selector `json:"selector,omitempty"`
	Coords   *Coords   `json:"coords,omitempty"`
	X        *int      `json:"x,omitempty"`
	Y        *int      `json:"y,omitempty"`
	Text     string    `json:"text,omitempty"`
	// Index picks the n-th (0-based) OCR match for ocr_tap.
	Index int `json:"index,omitempty"`
	// Exact makes OCR text matching whole-string; Regex treats text as a regular expression.
	Exact bool `json:"exact,omitempty"`
	Regex bool `json:"regex,omitempty"`
	// URL is the link opened by open_url / open_link.
	URL string `json:"url,omitempty"`
	Key string `json:"key,omitempty"`
	// Ms is the wait / long-press duration; DurationMs is used when Ms is unset.
	Ms         int `json:"ms,omitempty"`
	X1         int `json:"x1,omitempty"`
	Y1         int `json:"y1,omitempty"`
	X2         int `json:"x2,omitempty"`
	Y2         int `json:"y2,omitempty"`
	DurationMs int `json:"durationMs,omitempty"`

	From       *ElementRef `json:"from,omitempty"`
	FromCoords *Coords     `json:"fromCoords,omitempty"`
	ToCoords   *Coords     `json:"toCoords,omitempty"`
	ToElement  *ElementRef `json:"toElement,omitempty"`
	// ToSelector is the drag destination (PhoneForge spelling of to_element).
	ToSelector *Selector `json:"toSelector,omitempty"`

	// Path is a local file path on the server (install_app).
	Path string `json:"path,omitempty"`
	// Permission is the permission name (grant_permission / revoke_permission).
	Permission string `json:"permission,omitempty"`
	// TimeoutMs is the step limit; for wait_for and assertions it is the wait.
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// Direction is the swipe / scroll direction.
	Direction string `json:"direction,omitempty"`
	// MaxSwipes caps scroll_until_visible swipes (default 8).
	MaxSwipes int `json:"maxSwipes,omitempty"`
	// MaxScrolls caps scroll to_element scrolls (default 10).
	MaxScrolls int      `json:"maxScrolls,omitempty"`
	Distance   string   `json:"distance,omitempty"`
	Amount     string   `json:"amount,omitempty"`
	Scale      *float64 `json:"scale,omitempty"`
	Velocity   *float64 `json:"velocity,omitempty"`

	ClearFirst      bool   `json:"clearFirst,omitempty"`
	DismissKeyboard bool   `json:"dismissKeyboard,omitempty"`
	SecretID        string `json:"secretId,omitempty"`
	Value           string `json:"value,omitempty"`
	State           string `json:"state,omitempty"`
	Target          string `json:"target,omitempty"`
	StoreAs         string `json:"storeAs,omitempty"`

	Include         json.RawMessage `json:"include,omitempty"`
	OnlyVisible     *bool           `json:"onlyVisible,omitempty"`
	IncludeKeyboard bool            `json:"includeKeyboard,omitempty"`
	Compact         bool            `json:"compact,omitempty"`
	Filter          json.RawMessage `json:"filter,omitempty"`

	FilePath       string `json:"filePath,omitempty"`
	Name           string `json:"name,omitempty"`
	Stable         bool   `json:"stable,omitempty"`
	PollIntervalMs int    `json:"pollIntervalMs,omitempty"`

	PressDurationMs int             `json:"pressDurationMs,omitempty"`
	HoldDurationMs  int             `json:"holdDurationMs,omitempty"`
	Points          []PathPoint     `json:"points,omitempty"`
	Actions         json.RawMessage `json:"actions,omitempty"`

	Lat         *float64          `json:"lat,omitempty"`
	Lon         *float64          `json:"lon,omitempty"`
	Permissions map[string]string `json:"permissions,omitempty"`
	Sensor      string            `json:"sensor,omitempty"`
	Angle       *float64          `json:"angle,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`

	Types       []string        `json:"types,omitempty"`
	IntervalMs  int             `json:"intervalMs,omitempty"`
	Label       string          `json:"label,omitempty"`
	Thresholds  json.RawMessage `json:"thresholds,omitempty"`
	Format      string          `json:"format,omitempty"`
	CaptureLogs bool            `json:"captureLogs,omitempty"`

	Script       string          `json:"script,omitempty"`
	ScriptPath   string          `json:"scriptPath,omitempty"`
	ExtractKey   string          `json:"extractKey,omitempty"`
	ExtractKind  string          `json:"extractKind,omitempty"`
	ExtractRegex string          `json:"extractRegex,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`

	URLContains   string `json:"urlContains,omitempty"`
	TitleContains string `json:"titleContains,omitempty"`
	PageID        string `json:"pageId,omitempty"`

	// Message overrides the assertion failure message.
	Message          string      `json:"message,omitempty"`
	Expected         *int        `json:"expected,omitempty"`
	Property         string      `json:"property,omitempty"`
	ExpectedValue    *FlexString `json:"expectedValue,omitempty"`
	ThresholdPercent *float64    `json:"thresholdPercent,omitempty"`

	// Control flow.
	Then          []Step     `json:"then,omitempty"`
	Else          []Step     `json:"else,omitempty"`
	Body          []Step     `json:"body,omitempty"`
	Times         FlexString `json:"times,omitempty"`
	While         *Selector  `json:"while,omitempty"`
	Until         *Selector  `json:"until,omitempty"`
	Condition     string     `json:"condition,omitempty"`
	MaxIterations int        `json:"maxIterations,omitempty"`

	// SettleMs overrides Defaults.SettleMs for this step.
	SettleMs *int `json:"settleMs,omitempty"`
	// OnFail is the per-step failure strategy (string or object).
	OnFail *FailPolicy `json:"onFail,omitempty"`
	// Retries overrides the retry count for retry steps.
	Retries *int `json:"retries,omitempty"`
	// RetryDelayMs overrides the retry delay.
	RetryDelayMs *int `json:"retryDelayMs,omitempty"`
}

// Script is a parsed DSL document.
type Script struct {
	Version  string   `json:"version,omitempty"`
	Defaults Defaults `json:"defaults,omitempty"`
	Steps    []Step   `json:"steps"`
	// OnFail applies to steps without their own on_fail.
	OnFail *FailPolicy `json:"onFail,omitempty"`
	// Params are named variables with optional defaults, used as ${name}.
	Params map[string]ParamSpec `json:"params,omitempty"`
	// Alerts is "accept" or "dismiss": system alerts are handled automatically.
	Alerts string `json:"alerts,omitempty"`
	// Settle makes taps wait (up to 500 ms) for a moving target to stop.
	Settle bool `json:"settle,omitempty"`
	// Mode is "explore" (default) or "deterministic".
	Mode string `json:"mode,omitempty"`
}

// StepResult is the outcome of one step.
type StepResult struct {
	Index  int    `json:"index"`
	ID     string `json:"id,omitempty"`
	Action string `json:"action"`
	Status string `json:"status"` // ok|failed|skipped|needs_replan|needs_user
	Error  string `json:"error,omitempty"`
	// Code is a MobAI-style error code (NO_MATCH, TIMEOUT, ...) for failed steps.
	Code       string       `json:"code,omitempty"`
	Matched    *device.Rect `json:"matched,omitempty"`
	DurationMs int64        `json:"durationMs"`
	Warnings   []string     `json:"warnings,omitempty"`
	// Result carries action outputs (matchCount, propertyValue, texts, ...).
	Result map[string]any `json:"result,omitempty"`
	// Screenshot is a base64 PNG captured when the step failed (or by a screenshot step).
	Screenshot string `json:"screenshot,omitempty"`
	// Debug is attached to failed steps in explore mode.
	Debug map[string]any `json:"debug,omitempty"`

	ConditionMet *bool        `json:"conditionMet,omitempty"`
	ThenResults  []StepResult `json:"thenResults,omitempty"`
	ElseResults  []StepResult `json:"elseResults,omitempty"`
	Iterations   *int         `json:"iterations,omitempty"`
	BodyResults  []StepResult `json:"bodyResults,omitempty"`
}

// Checkpoint is a named marker recorded by a checkpoint step.
type Checkpoint struct {
	Name      string          `json:"name"`
	StepIndex int             `json:"stepIndex"`
	ElapsedMs int64           `json:"elapsedMs"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// Result is the outcome of a script run.
type Result struct {
	Passed bool `json:"passed"`
	// Status is passed, failed, needs_replan or needs_user.
	Status      string            `json:"status,omitempty"`
	Steps       []StepResult      `json:"steps"`
	Checkpoints []Checkpoint      `json:"checkpoints,omitempty"`
	Storage     map[string]string `json:"storage,omitempty"`
	TotalMs     int64             `json:"totalMs"`
}

// SecretResolver resolves a type_secret secret id to its value.
type SecretResolver func(id string) (string, error)

// Options tune ExecuteScript.
type Options struct {
	// Params override script params.
	Params map[string]string
	// Secrets resolves type_secret ids; nil makes type_secret fail.
	Secrets SecretResolver
	// NoFailureScreenshot disables the screenshot captured on failed steps.
	NoFailureScreenshot bool
	// Mode, when set, overrides Script.Mode.
	Mode string
	// Settle, when non-nil, overrides Script.Settle.
	Settle *bool
	// Web provides Chrome/WebView automation for the web_* actions. When nil
	// the host installed with SetWebHost is used; when that is also nil the
	// web_* actions fail with a clear "not configured" error.
	Web WebHost
}

// Parse decodes and validates a script. Unknown keys, unknown versions,
// unknown actions and unknown failure strategies are rejected.
func Parse(data []byte) (Script, error) {
	var s Script
	if err := json.Unmarshal(data, &s); err != nil {
		return Script{}, fmt.Errorf("dsl: parse script: %w", err)
	}
	if err := Validate(s); err != nil {
		return Script{}, err
	}
	return s, nil
}

// Validate checks a script the same way Parse does.
func Validate(s Script) error {
	if s.Version != "" && s.Version != Version {
		return fmt.Errorf("dsl: unsupported version %q (want %q)", s.Version, Version)
	}
	switch s.Alerts {
	case "", "accept", "dismiss":
	default:
		return fmt.Errorf("dsl: invalid alerts %q (use accept or dismiss)", s.Alerts)
	}
	switch s.Mode {
	case "", "explore", "deterministic":
	default:
		return fmt.Errorf("dsl: invalid mode %q (use explore or deterministic)", s.Mode)
	}
	if err := validPolicy(s.OnFail); err != nil {
		return fmt.Errorf("dsl: script on_fail: %w", err)
	}
	return validateSteps(s.Steps, "")
}

func validPolicy(p *FailPolicy) error {
	if p == nil {
		return nil
	}
	for _, v := range []string{p.Strategy, p.FallbackStrategy} {
		switch v {
		case "", OnFailAbort, OnFailSkip, OnFailRetry, OnFailReplan, OnFailRequireUser:
		default:
			return fmt.Errorf("unknown on_fail strategy %q", v)
		}
	}
	return nil
}

func validateSteps(steps []Step, prefix string) error {
	for i, st := range steps {
		path := fmt.Sprintf("%s%d", prefix, i)
		if _, ok := canonicalAction(st.Action); !ok {
			return fmt.Errorf("dsl: step %s: unknown action %q", path, st.Action)
		}
		if err := validPolicy(st.OnFail); err != nil {
			return fmt.Errorf("dsl: step %s: %w", path, err)
		}
		switch st.Platform {
		case "", "ios", "android":
		default:
			return fmt.Errorf("dsl: step %s: unknown platform %q", path, st.Platform)
		}
		if err := checkOCRStep(st); err != nil {
			return fmt.Errorf("dsl: step %s: %w", path, err)
		}
		for _, sel := range []*Selector{st.Selector, st.ToSelector, st.While, st.Until} {
			if err := checkRegexes(sel); err != nil {
				return fmt.Errorf("dsl: step %s: %w", path, err)
			}
		}
		for name, list := range map[string][]Step{"then": st.Then, "else": st.Else, "body": st.Body} {
			if err := validateSteps(list, path+"."+name+"."); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRegexes(sel *Selector) error {
	if sel == nil {
		return nil
	}
	if sel.TextRegex != "" && !strings.Contains(sel.TextRegex, "${") {
		if _, err := regexp.Compile(sel.TextRegex); err != nil {
			return fmt.Errorf("invalid text_regex: %w", err)
		}
	}
	if sel.Near != nil {
		if err := checkRegexes(&sel.Near.Selector); err != nil {
			return err
		}
	}
	return checkRegexes(sel.ParentOf)
}

func settleDuration(d Defaults, st Step) time.Duration {
	ms := defaultSettleMs
	if d.SettleMs != nil {
		ms = *d.SettleMs
	}
	if st.SettleMs != nil {
		ms = *st.SettleMs
	}
	if ms < 0 {
		ms = 0
	}
	return time.Duration(ms) * time.Millisecond
}
