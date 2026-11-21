package dsl

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/ocr"
	"phoneforge/internal/webauto"
)

// ---- OCR actions ----

// ocrRecognize is the OCR engine (a variable so tests can fake it).
var ocrRecognize = ocr.Recognize

// ocrLines screenshots the device and runs OCR. Bounds are converted to the
// device's logical coordinate space (the space Tap uses) when the screenshot
// PNG has a different pixel size than the size the driver reports.
func (r *runner) ocrLines(ctx context.Context) ([]ocr.Result, device.Screenshot, error) {
	shot, err := r.dev.Screenshot(ctx)
	if err != nil {
		return nil, shot, err
	}
	if len(shot.PNG) == 0 {
		return nil, shot, unsupported("ocr", "the driver returned an empty screenshot")
	}
	res, err := ocrRecognize(ctx, shot.PNG)
	if err != nil {
		return nil, shot, codeErr(CodeExecutionError, "ocr: %v", err)
	}
	sx, sy := ocr.Scale(shot.PNG, shot.Width, shot.Height)
	if sx != 1 || sy != 1 {
		scaled := make([]ocr.Result, len(res))
		for i, x := range res {
			b := x.Bounds
			x.Bounds = device.Rect{X: int(float64(b.X)*sx + 0.5), Y: int(float64(b.Y)*sy + 0.5),
				W: int(float64(b.W)*sx + 0.5), H: int(float64(b.H)*sy + 0.5)}
			scaled[i] = x
		}
		res = scaled
	}
	return res, shot, nil
}

func ocrTexts(res []ocr.Result) []string {
	out := make([]string, 0, len(res))
	for _, x := range res {
		if t := strings.TrimSpace(x.Text); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// checkOCRStep validates the parts of an OCR step that can be checked at
// parse time (a literal regular expression).
func checkOCRStep(st Step) error {
	if !st.Regex || st.Text == "" || strings.Contains(st.Text, "${") {
		return nil
	}
	if c, _ := canonicalAction(st.Action); c != "ocr_tap" && c != "assert_text_ocr" {
		return nil
	}
	if _, err := regexp.Compile(st.Text); err != nil {
		return fmt.Errorf("invalid regex in text: %w", err)
	}
	return nil
}

func (r *runner) doOCR(ctx context.Context, canon string, st Step, oc *outcome) error {
	switch canon {
	case "ocr":
		res, shot, err := r.ocrLines(ctx)
		if err != nil {
			return err
		}
		texts := ocrTexts(res)
		joined := strings.Join(texts, "\n")
		oc.set("text", joined)
		oc.set("lines", texts)
		oc.set("count", len(texts))
		oc.set("width", shot.Width)
		oc.set("height", shot.Height)
		if st.StoreAs != "" {
			r.vars[st.StoreAs] = joined
		}
		return nil
	case "ocr_tap":
		if st.Text == "" {
			return codeErr(CodeValidation, "ocr_tap requires text")
		}
		if st.Index < 0 {
			return codeErr(CodeValidation, "ocr_tap index must be >= 0")
		}
		res, _, err := r.ocrLines(ctx)
		if err != nil {
			return err
		}
		matches, err := ocr.Find(res, st.Text, ocr.FindOptions{Exact: st.Exact, Regex: st.Regex})
		if err != nil {
			return codeErr(CodeValidation, "%v", err)
		}
		if len(matches) == 0 {
			return codeErr(CodeNoMatch, "no on-screen text matched %q (OCR saw %d lines)", st.Text, len(res))
		}
		if st.Index >= len(matches) {
			return codeErr(CodeNoMatch, "ocr_tap index %d out of range: %d match(es) for %q", st.Index, len(matches), st.Text)
		}
		m := matches[st.Index]
		b := m.Bounds
		oc.matched = &b
		oc.set("matchCount", len(matches))
		oc.set("matchedText", m.Text)
		x, y := b.Center()
		return r.dev.Tap(ctx, x, y)
	case "assert_text_ocr":
		if st.Text == "" {
			return codeErr(CodeValidation, "%s requires text", st.Action)
		}
		timeout := st.TimeoutMs
		if timeout <= 0 {
			timeout = defaultWaitTimeoutMs
		}
		poll := waitPollInterval
		if st.PollIntervalMs > 0 {
			poll = msDuration(st.PollIntervalMs)
		}
		deadline := time.Now().Add(msDuration(timeout))
		var lastErr error
		seen := 0
		for {
			res, _, err := r.ocrLines(ctx)
			if err != nil {
				lastErr = err
				if ctx.Err() != nil {
					return err
				}
			} else {
				matches, ferr := ocr.Find(res, st.Text, ocr.FindOptions{Exact: st.Exact, Regex: st.Regex})
				if ferr != nil {
					return codeErr(CodeValidation, "%v", ferr)
				}
				if len(matches) > 0 {
					b := matches[0].Bounds
					oc.matched = &b
					oc.set("matchCount", len(matches))
					return nil
				}
				lastErr, seen = nil, len(res)
			}
			if !time.Now().Add(poll).Before(deadline) {
				break
			}
			if err := sleep(ctx, poll); err != nil {
				return err
			}
		}
		if lastErr != nil {
			return lastErr
		}
		msg := st.Message
		if msg == "" {
			msg = fmt.Sprintf("text %q not found by OCR within %dms (%d lines seen)", st.Text, timeout, seen)
		}
		return codeErr(CodeAssertionFailed, "%s", msg)
	}
	return codeErr(CodeInvalidAction, "unknown action %q", st.Action)
}

// ---- web actions ----

// WebPage is the subset of a Chrome/WebView page the web_* actions use.
// *webauto.Page implements it.
type WebPage interface {
	Navigate(ctx context.Context, url string) error
	Click(ctx context.Context, selector string) error
	Type(ctx context.Context, selector, text string) error
	Evaluate(ctx context.Context, js string) (json.RawMessage, error)
	WaitFor(ctx context.Context, selector string, timeout time.Duration) error
	Text(ctx context.Context, selector string) (string, error)
}

// WebHost resolves the page to drive for an Android device serial.
type WebHost interface {
	Page(ctx context.Context, serial string) (WebPage, error)
}

type managerHost struct{ m *webauto.Manager }

func (h managerHost) Page(ctx context.Context, serial string) (WebPage, error) {
	return h.m.Session(serial).Attach(ctx, "")
}

// NewWebHost adapts a webauto.Manager to a WebHost (default page target).
func NewWebHost(m *webauto.Manager) WebHost {
	if m == nil {
		return nil
	}
	return managerHost{m}
}

var (
	webMu      sync.RWMutex
	defaultWeb WebHost
)

// SetWebHost installs the process-wide WebHost used when Options.Web is nil.
// The server calls it once at start-up.
func SetWebHost(h WebHost) {
	webMu.Lock()
	defaultWeb = h
	webMu.Unlock()
}

func (r *runner) webPage(ctx context.Context, canon string) (WebPage, error) {
	host := r.opts.Web
	if host == nil {
		webMu.RLock()
		host = defaultWeb
		webMu.RUnlock()
	}
	if host == nil {
		return nil, codeErr(CodeExecutionError, "%s: web automation is not configured on this server", canon)
	}
	info := r.dev.Info()
	if info.Platform != device.PlatformAndroid {
		return nil, unsupported(canon, "web automation needs an Android device")
	}
	if info.Serial == "" {
		return nil, codeErr(CodeExecutionError, "%s: device has no adb serial", canon)
	}
	p, err := host.Page(ctx, info.Serial)
	if err != nil {
		return nil, codeErr(CodeExecutionError, "%s: %v", canon, err)
	}
	return p, nil
}

func cssOf(st Step) string {
	if st.Selector == nil {
		return ""
	}
	return st.Selector.CSSSelector
}

func (r *runner) doWeb(ctx context.Context, canon string, st Step, oc *outcome) error {
	css := cssOf(st)
	switch canon {
	case "web_navigate":
		if st.URL == "" {
			return codeErr(CodeValidation, "web_navigate requires url")
		}
	case "web_click", "web_wait":
		if css == "" {
			return codeErr(CodeValidation, "%s requires selector (a CSS selector string)", canon)
		}
	case "web_type":
		if css == "" {
			return codeErr(CodeValidation, "web_type requires selector (a CSS selector string)")
		}
	case "web_eval":
		if st.Script == "" {
			return codeErr(CodeValidation, "web_eval requires script")
		}
	case "web_assert_text":
		if st.Text == "" {
			return codeErr(CodeValidation, "web_assert_text requires text")
		}
	}
	p, err := r.webPage(ctx, canon)
	if err != nil {
		return err
	}
	switch canon {
	case "web_navigate":
		return p.Navigate(ctx, st.URL)
	case "web_click":
		return p.Click(ctx, css)
	case "web_type":
		return p.Type(ctx, css, st.Text)
	case "web_eval":
		v, err := p.Evaluate(ctx, st.Script)
		if err != nil {
			return err
		}
		out := string(v)
		oc.set("result", out)
		if st.StoreAs != "" {
			var s string
			if json.Unmarshal(v, &s) == nil {
				out = s
			}
			r.vars[st.StoreAs] = out
		}
		return nil
	case "web_wait":
		timeout := st.TimeoutMs
		if timeout <= 0 {
			timeout = defaultWaitTimeoutMs
		}
		return p.WaitFor(ctx, css, msDuration(timeout))
	case "web_assert_text":
		got, err := p.Text(ctx, css)
		if err != nil {
			return err
		}
		if !strings.Contains(got, st.Text) {
			msg := st.Message
			if msg == "" {
				msg = fmt.Sprintf("page text %q not found in %q", st.Text, firstNonEmpty(css, "body"))
			}
			return codeErr(CodeAssertionFailed, "%s", msg)
		}
		return nil
	}
	return codeErr(CodeInvalidAction, "unknown action %q", st.Action)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
