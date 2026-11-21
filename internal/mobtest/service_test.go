package mobtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/dsl"
)

type fake struct {
	id    string
	taps  [][2]int
	typed []string
}

func (f *fake) Info() device.Info { return device.Info{ID: f.id} }
func (f *fake) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{PNG: []byte("PNG")}, nil
}
func (f *fake) UITree(context.Context) (device.Element, error) {
	return device.Element{Role: "Frame", Bounds: device.Rect{W: 1000, H: 2000}, Children: []device.Element{
		{Role: "Button", Text: "Sign in", Bounds: device.Rect{X: 100, Y: 100, W: 200, H: 100}},
		{Role: "EditText", Text: "", Label: "Email", Bounds: device.Rect{X: 100, Y: 300, W: 200, H: 100}},
	}}, nil
}
func (f *fake) Tap(_ context.Context, x, y int) error {
	f.taps = append(f.taps, [2]int{x, y})
	return nil
}
func (f *fake) LongPress(context.Context, int, int, int) error       { return nil }
func (f *fake) Swipe(context.Context, int, int, int, int, int) error { return nil }
func (f *fake) TypeText(_ context.Context, s string) error {
	f.typed = append(f.typed, s)
	return nil
}
func (f *fake) PressKey(context.Context, device.Key) error         { return nil }
func (f *fake) ListApps(context.Context) ([]device.AppInfo, error) { return nil, nil }
func (f *fake) LaunchApp(context.Context, string) error            { return nil }
func (f *fake) StopApp(context.Context, string) error              { return nil }

func newSvc(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreProjectsAndScripts(t *testing.T) {
	s := newSvc(t).Store
	p, err := s.CreateProject("  My First Tests! ")
	if err != nil || p.ID != "my-first-tests" || p.Name != "My First Tests!" || !strings.HasPrefix(p.Path, s.Root()) {
		t.Fatalf("%+v %v", p, err)
	}
	p2, _ := s.CreateProject("My First Tests!")
	if p2.ID != "my-first-tests-2" {
		t.Fatalf("id = %q", p2.ID)
	}
	if _, err := s.CreateProject("   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := s.CreateProject("bad\x00name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control char: %v", err)
	}
	ps, _ := s.ListProjects()
	if len(ps) != 2 {
		t.Fatalf("%+v", ps)
	}

	info, created, err := s.WriteScript(p.ID, "login.mob", []byte("tap \"a\"\n"))
	if err != nil || !created || info.Kind != "mob" || info.Size != 8 {
		t.Fatalf("%+v %v %v", info, created, err)
	}
	if _, created, _ := s.WriteScript(p.ID, "login.mob", []byte("tap \"b\"\n")); created {
		t.Fatal("overwrite reported as created")
	}
	_, _, _ = s.WriteScript(p.ID, "flow.json", []byte(`{"steps":[]}`))
	b, err := s.ReadScript(p.ID, "login.mob")
	if err != nil || string(b) != "tap \"b\"\n" {
		t.Fatalf("%q %v", b, err)
	}
	list, _ := s.ListScripts(p.ID)
	if len(list) != 2 || list[0].Name != "flow.json" || list[1].Name != "login.mob" {
		t.Fatalf("%+v", list)
	}
	// stray files (meta, temp files, other extensions) are not scripts
	_ = os.WriteFile(filepath.Join(p.Path, "notes.txt"), []byte("x"), 0o600)
	if list, _ := s.ListScripts(p.ID); len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	if err := s.DeleteScript(p.ID, "login.mob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadScript(p.ID, "login.mob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Fatal("project dir still exists")
	}
}

func TestStoreRejectsTraversal(t *testing.T) {
	svc := newSvc(t)
	s := svc.Store
	p, _ := s.CreateProject("proj")
	victim := filepath.Join(filepath.Dir(s.Root()), "victim.mob")
	_ = os.WriteFile(victim, []byte("x"), 0o600)

	for _, bad := range []string{"../victim.mob", "..", ".", "", "a/b.mob", `a\b.mob`, "/etc/passwd", ".hidden.mob", "a..b.mob",
		"x.txt", "noext", "sp ace.mob", "ünï.mob", "a%2e%2e.mob", "x.mob\x00", strings.Repeat("a", 120) + ".mob", ".project.json", "a.MOB"} {
		if _, _, err := s.WriteScript(p.ID, bad, []byte("x")); !errors.Is(err, ErrInvalid) {
			t.Errorf("write %q: %v", bad, err)
		}
		if _, err := s.ReadScript(p.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("read %q: %v", bad, err)
		}
		if err := s.DeleteScript(p.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("delete %q: %v", bad, err)
		}
	}
	for _, bad := range []string{"..", ".", "../x", "a/b", ".runs", "", ".hidden", strings.Repeat("a", 70)} {
		if _, err := s.ListScripts(bad); err == nil {
			t.Errorf("list %q accepted", bad)
		}
		if err := s.DeleteProject(bad); err == nil {
			t.Errorf("delete project %q accepted", bad)
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "x" {
		t.Fatal("victim modified")
	}
	// a directory that is not a project is invisible
	_ = os.Mkdir(filepath.Join(s.Root(), "rogue"), 0o700)
	if _, err := s.ListScripts("rogue"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if ps, _ := s.ListProjects(); len(ps) != 1 {
		t.Fatalf("%+v", ps)
	}
	// symlinked scripts are refused
	if err := os.Symlink(victim, filepath.Join(p.Path, "link.mob")); err == nil {
		if _, err := s.ReadScript(p.ID, "link.mob"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("symlink read: %v", err)
		}
		if _, _, err := s.WriteScript(p.ID, "link.mob", []byte("y")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink write: %v", err)
		}
		if b, _ := os.ReadFile(victim); string(b) != "x" {
			t.Fatal("victim modified via symlink")
		}
		if list, _ := s.ListScripts(p.ID); len(list) != 0 {
			t.Fatalf("symlink listed: %+v", list)
		}
	}
	if _, _, err := s.WriteScript(p.ID, "big.mob", bytes.Repeat([]byte("a"), maxScriptBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestRunAndHistory(t *testing.T) {
	svc := newSvc(t)
	f := &fake{id: "dev1"}
	rec, err := Run(context.Background(), f, []byte("# Param: who = Sign in\ntap \"${who}\" timeout:1000\ntype \"Email\" → \"a@b.c\"\n"), "mob")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Passed || rec.DeviceID != "dev1" || rec.ID == "" || rec.StartedAt.IsZero() || len(rec.Result.Steps) != 2 {
		t.Fatalf("%+v", rec)
	}
	if len(f.taps) != 2 || f.typed[0] != "a@b.c" {
		t.Fatalf("taps=%v typed=%v", f.taps, f.typed)
	}
	// JSON kind and detection
	rec2, err := Run(context.Background(), &fake{id: "d"}, []byte(`{"steps":[{"action":"assert_visible","predicate":{"text":"Nope"}}]}`), "")
	if err != nil || rec2.Passed || rec2.Result.Status != "failed" || rec2.Result.Steps[0].Screenshot == "" {
		t.Fatalf("%+v %v", rec2, err)
	}
	// invalid scripts return errors
	if _, err := Run(context.Background(), f, []byte("zap\n"), "mob"); err == nil {
		t.Fatal("bad mob accepted")
	} else {
		var pe ParseErrors
		if !errors.As(err, &pe) || pe[0].Line != 1 {
			t.Fatalf("%v", err)
		}
	}
	if _, err := Run(context.Background(), f, []byte(`{"steps":[{"action":"zap"}]}`), "json"); err == nil {
		t.Fatal("bad json accepted")
	}
	if _, err := Run(context.Background(), f, []byte("tap \"x\""), "yaml"); err == nil {
		t.Fatal("bad kind accepted")
	}

	// history round trip
	rec.ProjectID, rec.Script = "p1", "a.mob"
	saved, err := svc.History.Append(rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.History.Get(saved.ID)
	if err != nil || got.Script != "a.mob" || !got.Passed || len(got.Result.Steps) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := svc.History.Get("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := svc.History.Get("0000000000000000-00000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestHistoryOrderFilterAndCap(t *testing.T) {
	h, _ := NewHistory(t.TempDir())
	h.cap = 5
	base := time.Now().UTC()
	for i := 0; i < 8; i++ {
		proj := "a"
		if i%2 == 1 {
			proj = "b"
		}
		_, err := h.Append(RunRecord{ProjectID: proj, Script: fmt.Sprint(i), StartedAt: base.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	all, _ := h.List("", 0)
	if len(all) != 5 || all[0].Script != "7" || all[4].Script != "3" {
		t.Fatalf("cap/order: %+v", scripts(all))
	}
	a, _ := h.List("a", 10)
	if len(a) != 2 || a[0].Script != "6" || a[1].Script != "4" {
		t.Fatalf("filter: %+v", scripts(a))
	}
	if l, _ := h.List("", 2); len(l) != 2 {
		t.Fatalf("limit: %d", len(l))
	}
}

func scripts(r []RunRecord) []string {
	var out []string
	for _, x := range r {
		out = append(out, x.Script)
	}
	return out
}

func TestRunIncludesFromProject(t *testing.T) {
	svc := newSvc(t)
	p, _ := svc.Store.CreateProject("inc")
	_, _, _ = svc.Store.WriteScript(p.ID, "login.mob", []byte("# Param: who = nobody\ntap \"${who}\"\n"))
	_, _, _ = svc.Store.WriteScript(p.ID, "main.mob", []byte("run \"./login.mob\" who=\"Sign in\"\n"))
	f := &fake{id: "d"}
	rec, err := svc.RunScript(context.Background(), f, p.ID, "main.mob")
	if err != nil || !rec.Passed || len(f.taps) != 1 || rec.ProjectID != p.ID || rec.Script != "main.mob" {
		t.Fatalf("%+v %v taps=%v", rec, err, f.taps)
	}
	if got, err := svc.History.Get(rec.ID); err != nil || got.ProjectID != p.ID {
		t.Fatalf("%+v %v", got, err)
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.Bytes()
}

func TestRoutes(t *testing.T) {
	svc := newSvc(t)
	f := &fake{id: "dev1"}
	h := svc.Routes(func(id string) (device.Device, error) {
		switch id {
		case "dev1":
			return f, nil
		case "broken":
			return nil, errors.New("adb exploded")
		}
		return nil, device.ErrNotFound
	})
	type errBody struct {
		Error string       `json:"error"`
		Code  string       `json:"code"`
		Errs  []ParseError `json:"errors"`
	}
	expectErr := func(code int, body []byte, wantStatus int, wantCode string) errBody {
		t.Helper()
		var e errBody
		_ = json.Unmarshal(body, &e)
		if code != wantStatus || e.Code != wantCode || e.Error == "" {
			t.Fatalf("status %d body %s, want %d %s", code, body, wantStatus, wantCode)
		}
		return e
	}

	// projects
	code, b := do(t, h, "GET", "/projects", "")
	if code != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/projects", `{"name":"Smoke"}`)
	var proj Project
	_ = json.Unmarshal(b, &proj)
	if code != 201 || proj.ID != "smoke" || proj.Name != "Smoke" || proj.Path == "" || proj.CreatedAt.IsZero() {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/projects", `nope`)
	expectErr(code, b, 400, "invalid_request")
	code, b = do(t, h, "POST", "/projects", `{"name":""}`)
	expectErr(code, b, 400, "invalid_request")

	// scripts
	code, _ = do(t, h, "PUT", "/projects/smoke/scripts/login.mob", "tap \"Sign in\"\nassert_exists \"Nope\"\n")
	if code != 201 {
		t.Fatalf("put new = %d", code)
	}
	code, b = do(t, h, "PUT", "/projects/smoke/scripts/login.mob", "tap \"Sign in\"\n")
	var info ScriptInfo
	_ = json.Unmarshal(b, &info)
	if code != 200 || info.Name != "login.mob" || info.Kind != "mob" || info.Size != 14 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "PUT", "/projects/smoke/scripts/../x.mob", "x")
	if code == 201 || code == 200 {
		t.Fatalf("traversal accepted: %d %s", code, b)
	}
	code, b = do(t, h, "PUT", "/projects/smoke/scripts/bad%2Fname.mob", "x")
	expectErr(code, b, 400, "invalid_request")
	code, b = do(t, h, "PUT", "/projects/smoke/scripts/x.txt", "x")
	expectErr(code, b, 400, "invalid_request")
	code, b = do(t, h, "GET", "/projects/smoke/scripts", "")
	var list []ScriptInfo
	_ = json.Unmarshal(b, &list)
	if code != 200 || len(list) != 1 || list[0].Name != "login.mob" {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "GET", "/projects/smoke/scripts/login.mob", "")
	if code != 200 || string(b) != "tap \"Sign in\"\n" {
		t.Fatalf("%d %q", code, b)
	}
	code, b = do(t, h, "GET", "/projects/smoke/scripts/missing.mob", "")
	expectErr(code, b, 404, "not_found")
	code, b = do(t, h, "GET", "/projects/nope/scripts", "")
	expectErr(code, b, 404, "not_found")

	// run
	code, b = do(t, h, "POST", "/projects/smoke/scripts/login.mob/run", `{"deviceId":"dev1"}`)
	var rec RunRecord
	_ = json.Unmarshal(b, &rec)
	if code != 200 || !rec.Passed || rec.ProjectID != "smoke" || rec.Script != "login.mob" || rec.DeviceID != "dev1" || rec.ID == "" || len(rec.Result.Steps) != 1 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/projects/smoke/scripts/login.mob/run", `{}`)
	expectErr(code, b, 400, "invalid_request")
	code, b = do(t, h, "POST", "/projects/smoke/scripts/login.mob/run", `{"deviceId":"ghost"}`)
	expectErr(code, b, 404, "device_not_found")
	code, b = do(t, h, "POST", "/projects/smoke/scripts/login.mob/run", `{"deviceId":"broken"}`)
	expectErr(code, b, 502, "device_error")
	code, b = do(t, h, "POST", "/projects/smoke/scripts/none.mob/run", `{"deviceId":"dev1"}`)
	expectErr(code, b, 404, "not_found")
	_, _ = do(t, h, "PUT", "/projects/smoke/scripts/broken.mob", "tap \"a\"\nzap\n")
	code, b = do(t, h, "POST", "/projects/smoke/scripts/broken.mob/run", `{"deviceId":"dev1"}`)
	e := expectErr(code, b, 400, "invalid_script")
	if len(e.Errs) != 1 || e.Errs[0].Line != 2 {
		t.Fatalf("%s", b)
	}
	// failing script still returns 200 with passed=false and history entry
	_, _ = do(t, h, "PUT", "/projects/smoke/scripts/fail.mob", "assert_exists \"Nope\"\n")
	code, b = do(t, h, "POST", "/projects/smoke/scripts/fail.mob/run", `{"deviceId":"dev1"}`)
	var failRec RunRecord
	_ = json.Unmarshal(b, &failRec)
	if code != 200 || failRec.Passed || failRec.Result.Steps[0].Status != "failed" || failRec.Result.Steps[0].Screenshot == "" {
		t.Fatalf("%d %s", code, b)
	}

	// validate
	code, b = do(t, h, "POST", "/validate?kind=mob", "tap \"a\"\nzap\n")
	var v struct {
		OK     bool         `json:"ok"`
		Errors []ParseError `json:"errors"`
	}
	_ = json.Unmarshal(b, &v)
	if code != 200 || v.OK || len(v.Errors) != 1 || v.Errors[0].Line != 2 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/validate", "tap \"a\"\n")
	_ = json.Unmarshal(b, &v)
	if code != 200 || !v.OK || v.Errors == nil || len(v.Errors) != 0 || !strings.Contains(string(b), `"errors":[]`) {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/validate", `{"steps":[{"action":"tap","x":1,"y":2}]}`)
	_ = json.Unmarshal(b, &v)
	if code != 200 || !v.OK {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/validate?kind=json", "{\n  \"steps\": [\n    {\"action\": \"zap\"}\n  ]\n}")
	_ = json.Unmarshal(b, &v)
	if code != 200 || v.OK || !strings.Contains(v.Errors[0].Message, `unknown action "zap"`) {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/validate?kind=json", "{\n  \"steps\": [\n    {\"action\": \n  ]\n}")
	_ = json.Unmarshal(b, &v)
	if code != 200 || v.OK || v.Errors[0].Line < 3 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "POST", "/validate?kind=yaml", "x")
	expectErr(code, b, 400, "invalid_request")
	// run lines: syntax-only without a project, resolved with one
	code, b = do(t, h, "POST", "/validate", "run \"./login.mob\"\n")
	_ = json.Unmarshal(b, &v)
	if !v.OK {
		t.Fatalf("%s", b)
	}
	code, b = do(t, h, "POST", "/validate?projectId=smoke", "run \"./missing.mob\"\nrun \"./login.mob\"\n")
	_ = json.Unmarshal(b, &v)
	if code != 200 || v.OK || len(v.Errors) != 1 || v.Errors[0].Line != 1 {
		t.Fatalf("%d %s", code, b)
	}

	// runs
	code, b = do(t, h, "GET", "/runs?projectId=smoke&limit=2", "")
	var runs []RunRecord
	_ = json.Unmarshal(b, &runs)
	if code != 200 || len(runs) != 2 || runs[0].ID != failRec.ID {
		t.Fatalf("%d %s", code, b)
	}
	if runs[0].Result.Steps[0].Screenshot != "" {
		t.Fatal("list should not include screenshots")
	}
	code, b = do(t, h, "GET", "/runs?projectId=other", "")
	if code != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "GET", "/runs?limit=abc", "")
	expectErr(code, b, 400, "invalid_request")
	code, b = do(t, h, "GET", "/runs/"+failRec.ID, "")
	var one RunRecord
	_ = json.Unmarshal(b, &one)
	if code != 200 || one.ID != failRec.ID || one.Result.Steps[0].Screenshot == "" {
		t.Fatalf("%d %s", code, b)
	}
	code, b = do(t, h, "GET", "/runs/0000000000000000-00000000", "")
	expectErr(code, b, 404, "not_found")
	code, b = do(t, h, "GET", "/runs/..%2F..%2Fetc", "")
	if code == 200 {
		t.Fatalf("traversal in run id: %d", code)
	}

	// delete
	code, _ = do(t, h, "DELETE", "/projects/smoke/scripts/login.mob", "")
	if code != 204 {
		t.Fatalf("delete script = %d", code)
	}
	code, b = do(t, h, "DELETE", "/projects/smoke/scripts/login.mob", "")
	expectErr(code, b, 404, "not_found")
	code, _ = do(t, h, "DELETE", "/projects/smoke", "")
	if code != 204 {
		t.Fatalf("delete project = %d", code)
	}
	code, b = do(t, h, "DELETE", "/projects/smoke", "")
	expectErr(code, b, 404, "not_found")
}

var _ = dsl.Version
