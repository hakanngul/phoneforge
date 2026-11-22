package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pocketlab/internal/device"
)

var fixtureResults = []Result{
	{Text: "Log In", Bounds: device.Rect{X: 100, Y: 200, W: 80, H: 20}},
	{Text: "Login help", Bounds: device.Rect{X: 100, Y: 300, W: 80, H: 20}},
	{Text: "Sign up 42", Bounds: device.Rect{X: 100, Y: 400, W: 80, H: 20}},
}

func TestFind(t *testing.T) {
	cases := []struct {
		q    string
		o    FindOptions
		want int
	}{
		{"log in", FindOptions{}, 1},
		{"login", FindOptions{}, 2 - 1}, // "Login help" only; "Log In" has a space
		{"log in", FindOptions{CaseSensitive: true}, 0},
		{"Log In", FindOptions{CaseSensitive: true, Exact: true}, 1},
		{"login", FindOptions{Exact: true}, 0},
		{`\d+$`, FindOptions{Regex: true}, 1},
		{`^log\s?in`, FindOptions{Regex: true}, 2},
		{`^log\s?in`, FindOptions{Regex: true, CaseSensitive: true}, 0},
	}
	for _, c := range cases {
		got, err := Find(fixtureResults, c.q, c.o)
		if err != nil || len(got) != c.want {
			t.Errorf("Find(%q,%+v) = %d, %v; want %d", c.q, c.o, len(got), err, c.want)
		}
	}
	if _, err := Find(nil, "(", FindOptions{Regex: true}); err == nil {
		t.Error("expected regex error")
	}
	x, y := fixtureResults[0].Bounds.Center()
	if x != 140 || y != 210 {
		t.Errorf("center = %d,%d", x, y)
	}
}

const tsvFixture = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
	"1\t1\t0\t0\t0\t0\t0\t0\t800\t600\t-1\t\n" +
	"4\t1\t1\t1\t1\t0\t10\t20\t100\t30\t-1\t\n" +
	"5\t1\t1\t1\t1\t1\t10\t20\t40\t30\t96.5\tHello\n" +
	"5\t1\t1\t1\t1\t2\t60\t22\t50\t28\t90\tWorld\n" +
	"5\t1\t1\t1\t2\t1\t10\t80\t60\t20\t80\tNext\n" +
	"5\t1\t1\t1\t2\t2\t80\t80\t10\t20\t-1\t \n"

func TestParseTesseractTSV(t *testing.T) {
	lines, err := ParseTesseractTSV(tsvFixture, false)
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines: %v %v", lines, err)
	}
	if lines[0].Text != "Hello World" || lines[0].Bounds != (device.Rect{X: 10, Y: 20, W: 100, H: 30}) {
		t.Errorf("line0 = %+v", lines[0])
	}
	if c := lines[0].Confidence; c < 0.93 || c > 0.94 {
		t.Errorf("conf = %v", c)
	}
	words, err := ParseTesseractTSV(tsvFixture, true)
	if err != nil || len(words) != 3 || words[1].Text != "World" {
		t.Fatalf("words: %v %v", words, err)
	}
	if _, err := ParseTesseractTSV("5\tx\t1\t1\t1\t1\t1\t1\t1\t1\t1\tz\n", false); err == nil {
		t.Error("expected error for bad row")
	}
}

func TestParseVisionJSON(t *testing.T) {
	in := `[{"text":"Merhaba","x":5,"y":6,"w":70,"h":18,"confidence":0.5},{"text":"  ","x":0,"y":0,"w":1,"h":1,"confidence":1}]`
	res, err := ParseVisionJSON([]byte(in))
	if err != nil || len(res) != 1 || res[0].Text != "Merhaba" || res[0].Bounds.W != 70 || res[0].Confidence != 0.5 {
		t.Fatalf("%v %v", res, err)
	}
	if res, err := ParseVisionJSON([]byte("[]\n")); err != nil || len(res) != 0 {
		t.Fatalf("empty: %v %v", res, err)
	}
	if _, err := ParseVisionJSON([]byte("nope")); err == nil {
		t.Error("expected error")
	}
}

func blankPNG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.White)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVisionHelperIntegration(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	if _, err := exec.LookPath("swiftc"); err != nil {
		t.Skip("swiftc not found")
	}
	SetHelperPath(filepath.Join(t.TempDir(), "plocr"))
	defer SetHelperPath("")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin, err := EnsureHelper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := runHelper(ctx, bin, blankPNG(t), Options{Fast: true})
	if err != nil || len(res) != 0 {
		t.Fatalf("blank image: %v %v", res, err)
	}
}

type fakeDev struct {
	device.Device
	tapped [2]int
}

func (f *fakeDev) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{PNG: blankPNG(nil), Width: 64, Height: 64}, nil
}
func (f *fakeDev) Tap(_ context.Context, x, y int) error { f.tapped = [2]int{x, y}; return nil }

func TestRoutes(t *testing.T) {
	f := &fakeDev{}
	h := Routes(func(id string) (device.Device, error) {
		if id == "d1" {
			return f, nil
		}
		return nil, device.ErrNotFound
	})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do("GET", "/status", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "available") {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/devices/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown device: %d", rec.Code)
	}
	if rec := do("POST", "/devices/d1/find", `{}`); rec.Code != 400 {
		t.Errorf("missing text: %d", rec.Code)
	}
	if rec := do("POST", "/devices/d1/find", `{"text":"(","regex":true}`); rec.Code != 400 {
		t.Errorf("bad regex: %d", rec.Code)
	}
	if eng, ok := Available(); ok && eng == EngineTesseract {
		rec := do("POST", "/devices/d1", "")
		var out struct {
			Width   int      `json:"width"`
			Results []Result `json:"results"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Width != 64 {
			t.Errorf("ocr: %d %s", rec.Code, rec.Body)
		}
		if rec := do("POST", "/devices/d1/tap", `{"text":"zzz"}`); rec.Code != 404 {
			t.Errorf("tap missing: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestWarmMarkerIsKeyedAndOutsideBundle(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "plocr")
	if isWarm(bin) {
		t.Fatal("no marker yet, must be cold")
	}
	markWarm(bin)
	if !isWarm(bin) {
		t.Fatal("marker just written must be warm")
	}
	// A marker from another helper/OS build is stale.
	if err := os.WriteFile(bin+".warm", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isWarm(bin) {
		t.Fatal("legacy marker \"ok\" must not count as warm")
	}
}
