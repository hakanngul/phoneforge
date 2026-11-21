package dsl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/ocr"
)

func blankPNG(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// fakeOCR replaces the OCR engine for the duration of a test.
func fakeOCR(t *testing.T, seq ...[]ocr.Result) *int {
	t.Helper()
	calls := new(int)
	old := ocrRecognize
	ocrRecognize = func(context.Context, []byte) ([]ocr.Result, error) {
		i := *calls
		*calls++
		if i >= len(seq) {
			i = len(seq) - 1
		}
		return seq[i], nil
	}
	t.Cleanup(func() { ocrRecognize = old })
	return calls
}

func ocrDevice(pxW, pxH, w, h int) *fakeDevice {
	f := newFake()
	f.shot = device.Screenshot{PNG: blankPNG(pxW, pxH), Width: w, Height: h}
	return f
}

func runSteps(t *testing.T, f device.Device, steps string, opts Options) Result {
	t.Helper()
	s, err := Parse([]byte(hdr + steps + `}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ExecuteScript(context.Background(), f, s, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestParseNewActionsAndAliases(t *testing.T) {
	for _, a := range []string{"ocr_tap", "tap_text_ocr", "assert_text_ocr", "wait_for_text_ocr", "ocr", "read_screen_text",
		"web_navigate", "web_click", "web_type", "web_eval", "web_wait", "web_assert_text", "Tap-Text-OCR"} {
		if _, ok := canonicalAction(a); !ok {
			t.Errorf("action %q not registered", a)
		}
	}
	if c, _ := canonicalAction("tap_text_ocr"); c != "ocr_tap" {
		t.Errorf("tap_text_ocr -> %q", c)
	}
	if c, _ := canonicalAction("wait_for_text_ocr"); c != "assert_text_ocr" {
		t.Errorf("wait_for_text_ocr -> %q", c)
	}
	s, err := Parse([]byte(`{"steps":[
		{"action":"ocr_tap","text":"Sign in","index":1,"exact":true},
		{"action":"web_click","selector":"#go"},
		{"action":"web_type","selector":{"cssSelector":"input[name=q]"},"text":"hi"},
		{"action":"wait_for_text_ocr","text":"Done","timeout_ms":500}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Steps[0].Index != 1 || !s.Steps[0].Exact {
		t.Errorf("ocr_tap fields: %+v", s.Steps[0])
	}
	if cssOf(s.Steps[1]) != "#go" || cssOf(s.Steps[2]) != "input[name=q]" {
		t.Errorf("css selectors: %q %q", cssOf(s.Steps[1]), cssOf(s.Steps[2]))
	}
	if s.Steps[3].TimeoutMs != 500 {
		t.Errorf("timeout_ms = %d", s.Steps[3].TimeoutMs)
	}
}

func TestParseRejectsBadOCRRegex(t *testing.T) {
	_, err := Parse([]byte(`{"steps":[{"action":"ocr_tap","text":"(unclosed","regex":true}]}`))
	if err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse([]byte(`{"steps":[{"action":"ocr_tap","text":"(unclosed"}]}`)); err != nil {
		t.Fatalf("non-regex text must parse: %v", err)
	}
}

func TestOCRTapScalesToDeviceSpace(t *testing.T) {
	// Screenshot PNG is 2x the logical size (retina style).
	f := ocrDevice(200, 400, 100, 200)
	fakeOCR(t, []ocr.Result{
		{Text: "Cancel", Bounds: device.Rect{X: 20, Y: 20, W: 40, H: 20}},
		{Text: "Sign in", Bounds: device.Rect{X: 100, Y: 300, W: 60, H: 40}},
	})
	res := runSteps(t, f, `[{"action":"ocr_tap","text":"sign IN"}]`, Options{})
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
	if len(f.taps) != 1 || f.taps[0] != (tap{65, 160}) {
		t.Fatalf("taps = %+v", f.taps)
	}
	if res.Steps[0].Matched == nil || res.Steps[0].Matched.X != 50 {
		t.Fatalf("matched = %+v", res.Steps[0].Matched)
	}
}

func TestOCRTapIndexExactRegexAndErrors(t *testing.T) {
	f := ocrDevice(100, 200, 100, 200)
	fakeOCR(t, []ocr.Result{
		{Text: "Item 1", Bounds: device.Rect{X: 0, Y: 0, W: 10, H: 10}},
		{Text: "Item 2", Bounds: device.Rect{X: 0, Y: 100, W: 10, H: 10}},
		{Text: "Item", Bounds: device.Rect{X: 50, Y: 50, W: 10, H: 10}},
	})
	res := runSteps(t, f, `[
		{"action":"ocr_tap","text":"item","index":1},
		{"action":"ocr_tap","text":"item","exact":true},
		{"action":"ocr_tap","text":"^Item \\d$","regex":true,"index":0}]`, Options{})
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
	want := []tap{{5, 105}, {55, 55}, {5, 5}}
	for i, w := range want {
		if f.taps[i] != w {
			t.Fatalf("tap %d = %+v want %+v", i, f.taps[i], w)
		}
	}
	for _, c := range []struct{ step, code string }{
		{`{"action":"ocr_tap","text":"missing"}`, CodeNoMatch},
		{`{"action":"ocr_tap","text":"item","index":9}`, CodeNoMatch},
		{`{"action":"ocr_tap"}`, CodeValidation},
	} {
		r := runSteps(t, f, `[`+c.step+`]`, Options{NoFailureScreenshot: true})
		if r.Passed || r.Steps[0].Code != c.code {
			t.Errorf("%s: code %q passed=%v", c.step, r.Steps[0].Code, r.Passed)
		}
	}
}

func TestAssertTextOCRPolls(t *testing.T) {
	f := ocrDevice(100, 200, 100, 200)
	calls := fakeOCR(t,
		[]ocr.Result{{Text: "Loading"}},
		[]ocr.Result{{Text: "Loading"}},
		[]ocr.Result{{Text: "Welcome home", Bounds: device.Rect{X: 1, Y: 2, W: 3, H: 4}}},
	)
	old := waitPollInterval
	waitPollInterval = 2 * time.Millisecond
	defer func() { waitPollInterval = old }()
	res := runSteps(t, f, `[{"action":"wait_for_text_ocr","text":"welcome","timeout_ms":2000}]`, Options{})
	if !res.Passed || *calls != 3 {
		t.Fatalf("passed=%v calls=%d %+v", res.Passed, *calls, res)
	}
	res = runSteps(t, f, `[{"action":"assert_text_ocr","text":"never there","timeout_ms":20}]`, Options{NoFailureScreenshot: true})
	if res.Passed || res.Steps[0].Code != CodeAssertionFailed {
		t.Fatalf("expected assertion failure: %+v", res.Steps[0])
	}
}

func TestOCRActionReturnsText(t *testing.T) {
	f := ocrDevice(100, 200, 100, 200)
	fakeOCR(t, []ocr.Result{{Text: "Hello"}, {Text: "  "}, {Text: "World"}})
	res := runSteps(t, f, `[{"action":"read_screen_text","storeAs":"screen"}]`, Options{})
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
	r := res.Steps[0].Result
	if r["text"] != "Hello\nWorld" || r["count"] != 2 {
		t.Fatalf("result = %+v", r)
	}
	if res.Storage["screen"] != "Hello\nWorld" {
		t.Fatalf("storage = %+v", res.Storage)
	}
}

// ---- web ----

type fakePage struct {
	calls []string
	text  string
	eval  json.RawMessage
}

func (p *fakePage) rec(s string) error                         { p.calls = append(p.calls, s); return nil }
func (p *fakePage) Navigate(_ context.Context, u string) error { return p.rec("nav " + u) }
func (p *fakePage) Click(_ context.Context, s string) error    { return p.rec("click " + s) }
func (p *fakePage) Type(_ context.Context, s, t string) error  { return p.rec("type " + s + "=" + t) }
func (p *fakePage) Evaluate(_ context.Context, js string) (json.RawMessage, error) {
	p.calls = append(p.calls, "eval "+js)
	return p.eval, nil
}
func (p *fakePage) WaitFor(_ context.Context, s string, d time.Duration) error {
	return p.rec("wait " + s + " " + d.String())
}
func (p *fakePage) Text(_ context.Context, s string) (string, error) {
	p.calls = append(p.calls, "text "+s)
	return p.text, nil
}

type fakeHost struct {
	page   *fakePage
	serial string
	err    error
}

func (h *fakeHost) Page(_ context.Context, serial string) (WebPage, error) {
	h.serial = serial
	return h.page, h.err
}

type androidFake struct {
	*fakeDevice
	platform device.Platform
}

func (a androidFake) Info() device.Info {
	return device.Info{ID: "fake", Serial: "SER1", Platform: a.platform}
}

func TestWebActions(t *testing.T) {
	page := &fakePage{text: "Hello Example", eval: json.RawMessage(`"Example Domain"`)}
	host := &fakeHost{page: page}
	dev := androidFake{newFake(), device.PlatformAndroid}
	res := runSteps(t, dev, `[
		{"action":"web_navigate","url":"https://example.com"},
		{"action":"web_wait","selector":"h1","timeout_ms":1500},
		{"action":"web_click","selector":"#go"},
		{"action":"web_type","selector":"input","text":"hi"},
		{"action":"web_eval","script":"document.title","storeAs":"title"},
		{"action":"web_assert_text","selector":"h1","text":"Example"}]`, Options{Web: host})
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
	want := []string{"nav https://example.com", "wait h1 1.5s", "click #go", "type input=hi", "eval document.title", "text h1"}
	if strings.Join(page.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", page.calls)
	}
	if host.serial != "SER1" {
		t.Fatalf("serial = %q", host.serial)
	}
	if res.Storage["title"] != "Example Domain" || res.Steps[4].Result["result"] != `"Example Domain"` {
		t.Fatalf("eval result: %+v %+v", res.Storage, res.Steps[4].Result)
	}

	res = runSteps(t, dev, `[{"action":"web_assert_text","text":"absent"}]`, Options{Web: host, NoFailureScreenshot: true})
	if res.Passed || res.Steps[0].Code != CodeAssertionFailed {
		t.Fatalf("%+v", res.Steps[0])
	}
}

func TestWebActionErrors(t *testing.T) {
	dev := androidFake{newFake(), device.PlatformAndroid}
	// Not configured.
	SetWebHost(nil)
	res := runSteps(t, dev, `[{"action":"web_navigate","url":"https://x"}]`, Options{NoFailureScreenshot: true})
	if res.Passed || !strings.Contains(res.Steps[0].Error, "not configured") {
		t.Fatalf("%+v", res.Steps[0])
	}
	// Package-level host is used when Options.Web is nil.
	page := &fakePage{}
	SetWebHost(&fakeHost{page: page})
	defer SetWebHost(nil)
	if r := runSteps(t, dev, `[{"action":"web_click","selector":"a"}]`, Options{}); !r.Passed || len(page.calls) != 1 {
		t.Fatalf("default host unused: %+v", r)
	}
	// iOS is rejected.
	ios := androidFake{newFake(), device.PlatformIOS}
	r := runSteps(t, ios, `[{"action":"web_click","selector":"a"}]`, Options{NoFailureScreenshot: true})
	if r.Passed || !strings.Contains(r.Steps[0].Error, device.ErrUnsupported.Error()) {
		t.Fatalf("ios: %+v", r.Steps[0])
	}
	// Validation errors.
	for _, c := range []struct{ step, msg string }{
		{`{"action":"web_navigate"}`, "url"},
		{`{"action":"web_click"}`, "selector"},
		{`{"action":"web_type","selector":"a"}`, ""}, // text may be empty
		{`{"action":"web_eval"}`, "script"},
		{`{"action":"web_assert_text","selector":"a"}`, "text"},
	} {
		r := runSteps(t, dev, `[`+c.step+`]`, Options{NoFailureScreenshot: true})
		if c.msg == "" {
			if !r.Passed {
				t.Errorf("%s should pass: %+v", c.step, r.Steps[0])
			}
			continue
		}
		if r.Passed || r.Steps[0].Code != CodeValidation || !strings.Contains(r.Steps[0].Error, c.msg) {
			t.Errorf("%s: %+v", c.step, r.Steps[0])
		}
	}
	// Host errors surface.
	bad := &fakeHost{page: page, err: errors.New("no page targets")}
	r = runSteps(t, dev, `[{"action":"web_click","selector":"a"}]`, Options{Web: bad, NoFailureScreenshot: true})
	if r.Passed || !strings.Contains(r.Steps[0].Error, "no page targets") {
		t.Fatalf("%+v", r.Steps[0])
	}
}
