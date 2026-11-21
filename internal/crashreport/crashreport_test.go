package crashreport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
)

const ipsSample = `{"app_name":"Demo","timestamp":"2026-05-12 14:23:45.00 +0100","app_version":"1.0","slice_uuid":"x","build_version":"1","platform":2,"bundleID":"com.example.demo","share_with_app_devs":1,"is_first_party":0,"bug_type":"309","os_version":"iPhone OS 17.5 (21F79)","incident_id":"ABC","name":"Demo"}
{"captureTime":"2026-05-12 14:23:44.1234 +0100","procName":"Demo","exception":{"codes":"0x1, 0x0","rawCodes":[1,0],"type":"EXC_BAD_ACCESS","signal":"SIGSEGV","subtype":"KERN_INVALID_ADDRESS at 0x0"},"threads":[]}`

func TestParseIPS(t *testing.T) {
	r := ParseIPS("Demo-2026-05-12-142345.ips", ipsSample)
	if r.BundleID != "com.example.demo" || r.App != "Demo" || r.Process != "Demo" {
		t.Fatalf("ids: %+v", r)
	}
	if r.ExceptionType != "EXC_BAD_ACCESS" || r.Signal != "SIGSEGV" || r.Kind != "crash" {
		t.Fatalf("exception: %+v", r)
	}
	want := time.Date(2026, 5, 12, 13, 23, 45, 0, time.UTC)
	if !r.Timestamp.UTC().Equal(want) {
		t.Fatalf("timestamp %v want %v", r.Timestamp, want)
	}
	if r.OSVersion != "iPhone OS 17.5 (21F79)" {
		t.Fatalf("os %q", r.OSVersion)
	}
}

func TestParseIPSTruncatedAndJetsam(t *testing.T) {
	r := ParseIPS("JetsamEvent-2026-01-02-030405.ips", `{"bug_type":"298","timestamp":"2026-01-02 03:04:05.00 +0000","os_version":"iPhone OS 17"}`+"\n{\"x\":")
	if r.Kind != "jetsam" || r.Timestamp.IsZero() {
		t.Fatalf("%+v", r)
	}
}

func TestParseLegacyCrash(t *testing.T) {
	txt := "Incident Identifier: 1\nProcess:             Legacy [123]\nIdentifier:          com.legacy.app\nDate/Time:           2020-03-04 05:06:07.000 +0000\nOS Version:          iPhone OS 13.0\nException Type:  EXC_CRASH (SIGABRT)\n"
	r := ParseIPS("Legacy.crash", txt)
	if r.Source != "crash" || r.Process != "Legacy" || r.BundleID != "com.legacy.app" || r.ExceptionType != "EXC_CRASH" || r.Signal != "SIGABRT" {
		t.Fatalf("%+v", r)
	}
	if r.Timestamp.Year() != 2020 {
		t.Fatalf("ts %v", r.Timestamp)
	}
}

func TestTimeFromNameFallback(t *testing.T) {
	r := ParseIPS("Foo-2024-06-07-081500.ips", "garbage")
	if r.Timestamp.Year() != 2024 || r.Timestamp.Month() != 6 {
		t.Fatalf("%v", r.Timestamp)
	}
}

func TestFilter(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	in := []Report{
		{Name: "a", BundleID: "com.a", Timestamp: base},
		{Name: "b", BundleID: "com.b", Timestamp: base.Add(time.Hour)},
		{Name: "c", BundleID: "com.a", Timestamp: base.Add(2 * time.Hour)},
	}
	got := Filter(in, Query{App: "COM.A"})
	if len(got) != 2 || got[0].Name != "c" {
		t.Fatalf("%+v", got)
	}
	got = Filter(in, Query{Since: base.Add(30 * time.Minute), Limit: 1})
	if len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("%+v", got)
	}
}

const logcatSample = `--------- beginning of crash
2026-11-21 10:00:00.100  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
2026-11-21 10:00:00.100  1234  1234 E AndroidRuntime: Process: com.example.app, PID: 1234
2026-11-21 10:00:00.100  1234  1234 E AndroidRuntime: java.lang.NullPointerException: boom
2026-11-21 10:00:00.100  1234  1234 E AndroidRuntime: 	at com.example.app.Main.onCreate(Main.java:10)
2026-11-21 10:05:00.000  2222  2222 F libc    : Fatal signal 11 (SIGSEGV), code 1 (SEGV_MAPERR), fault addr 0x0 in tid 2222 (native), pid 2222 (native)
2026-11-21 10:05:00.500  2222  2222 F DEBUG   : *** *** *** *** *** *** *** *** *** *** *** *** *** *** *** ***
2026-11-21 10:05:00.500  2222  2222 F DEBUG   : Cmdline: com.example.native
2026-11-21 10:05:00.500  2222  2222 F DEBUG   : signal 11 (SIGSEGV), code 1 (SEGV_MAPERR), fault addr 0x0
`

func TestParseLogcat(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	es := ParseLogcatCrashes(logcatSample, time.UTC, now)
	if len(es) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(es), es)
	}
	j := es[0]
	if j.Process != "com.example.app" || j.ExceptionType != "java.lang.NullPointerException" || j.BundleID != "com.example.app" {
		t.Fatalf("java: %+v", j.Report)
	}
	if !strings.Contains(j.Text, "Main.onCreate") || !strings.HasPrefix(j.Name, "logcat-20261121T100000.100-1234") {
		t.Fatalf("java text/name: %q", j.Name)
	}
	n := es[2]
	if n.Process != "com.example.native" || n.Signal != "SIGSEGV" {
		t.Fatalf("native: %+v", n.Report)
	}
}

func TestParseLogcatYearlessInFutureRollsBack(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	es := ParseLogcatCrashes("12-30 10:00:00.100  1  1 E AndroidRuntime: FATAL EXCEPTION: main\n", time.UTC, now)
	if len(es) != 1 || es[0].Timestamp.Year() != 2025 {
		t.Fatalf("%+v", es)
	}
}

const dropSample = `Drop box contents: 2 entries
Max entries: 1000

========================================
2026-11-21 09:00:00 data_app_crash (text, 100 bytes)
Process: com.example.app
Flags: 0x38e83e46
Package: com.example.app v1 (1.0)

java.lang.IllegalStateException: bad
	at a.b.C.d(C.java:1)

========================================
2026-11-21 08:00:00 data_app_crash (text, 10 bytes)
Process: com.other
`

func TestParseDropbox(t *testing.T) {
	es := ParseDropbox(dropSample, time.UTC)
	if len(es) != 2 {
		t.Fatalf("got %d", len(es))
	}
	if es[0].BundleID != "com.example.app" || es[0].ExceptionType != "java.lang.IllegalStateException" || es[0].Source != "dropbox" {
		t.Fatalf("%+v", es[0].Report)
	}
	if es[1].Process != "com.other" {
		t.Fatalf("%+v", es[1].Report)
	}
}

func TestAndroidProviderWithFakeADB(t *testing.T) {
	p := &androidProvider{run: func(ctx context.Context, args ...string) (string, error) {
		switch {
		case args[0] == "logcat":
			return logcatSample, nil
		case len(args) > 4 && args[4] == "data_app_crash":
			return dropSample, nil
		case args[0] == "shell" && args[1] == "date":
			return "+0000\n", nil
		case args[0] == "shell" && args[1] == "ls":
			return "ls: /data/tombstones: Permission denied", nil
		}
		return "", nil
	}}
	rs, err := p.List(context.Background())
	if err != nil || len(rs) != 5 {
		t.Fatalf("%d %v", len(rs), err)
	}
	txt, err := p.Get(context.Background(), rs[0].Name)
	if err != nil || txt == "" {
		t.Fatalf("get: %q %v", txt, err)
	}
	if _, err := p.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := p.Delete(context.Background(), "x"); err == nil {
		t.Fatal("delete should be unsupported")
	}
}

type fakeProv struct {
	reports []Report
	deleted string
}

func (f *fakeProv) List(ctx context.Context) ([]Report, error) { return f.reports, nil }
func (f *fakeProv) Get(ctx context.Context, name string) (string, error) {
	for _, r := range f.reports {
		if r.Name == name {
			return "FULL " + name, nil
		}
	}
	return "", ErrNotFound
}
func (f *fakeProv) Delete(ctx context.Context, name string) error { f.deleted = name; return nil }

type fakeDev struct {
	device.Device
	info device.Info
}

func (d fakeDev) Info() device.Info { return d.info }

func testServer(t *testing.T, fp *fakeProv) *httptest.Server {
	svc := &Service{Override: func(info device.Info) (Provider, error) {
		if info.Platform == device.PlatformSimulator {
			return nil, device.ErrUnsupported
		}
		return fp, nil
	}}
	get := func(id string) (device.Device, error) {
		switch id {
		case "ios-1":
			return fakeDev{info: device.Info{ID: id, Platform: device.PlatformIOS}}, nil
		case "sim-1":
			return fakeDev{info: device.Info{ID: id, Platform: device.PlatformSimulator}}, nil
		}
		return nil, device.ErrNotFound
	}
	r := chi.NewRouter()
	r.Route("/api/v1/devices/{id}", func(r chi.Router) { r.Mount("/crashes", Routes(svc, get)) })
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRoutes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fp := &fakeProv{reports: []Report{
		{Name: "old", BundleID: "com.a", Timestamp: base},
		{Name: "new", BundleID: "com.a", Timestamp: base.Add(time.Hour)},
		{Name: "other", BundleID: "com.b", Timestamp: base.Add(2 * time.Hour)},
	}}
	ts := testServer(t, fp)
	u := ts.URL + "/api/v1/devices/ios-1/crashes"

	code, body := do(t, "GET", u+"?app=com.a")
	if code != 200 || strings.Contains(body, "other") || strings.Index(body, `"new"`) > strings.Index(body, `"old"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body = do(t, "GET", u+"?since="+base.Add(30*time.Minute).Format(time.RFC3339)+"&limit=1")
	if code != 200 || !strings.Contains(body, `"other"`) || strings.Contains(body, `"new"`) {
		t.Fatalf("since/limit: %d %s", code, body)
	}
	if code, _ = do(t, "GET", u+"?since=yesterday"); code != 400 {
		t.Fatalf("bad since: %d", code)
	}
	if code, body = do(t, "GET", u+"/new"); code != 200 || body != "FULL new" {
		t.Fatalf("get: %d %q", code, body)
	}
	if code, _ = do(t, "GET", u+"/missing"); code != 404 {
		t.Fatalf("missing: %d", code)
	}
	if code, _ = do(t, "DELETE", u+"/old"); code != 200 || fp.deleted != "old" {
		t.Fatalf("delete: %d %q", code, fp.deleted)
	}
	if code, _ = do(t, "GET", ts.URL+"/api/v1/devices/nope/crashes"); code != 404 {
		t.Fatalf("no device: %d", code)
	}
	if code, _ = do(t, "GET", ts.URL+"/api/v1/devices/sim-1/crashes"); code != 501 {
		t.Fatalf("unsupported: %d", code)
	}
}

func TestSimulatorProvider(t *testing.T) {
	dir := t.TempDir()
	udid := "ABCD-1234"
	mine := `{"app_name":"Sim","timestamp":"2026-05-12 14:23:45.00 +0000","bundleID":"com.sim.app","bug_type":"309","os_version":"iPhone OS 27.2"}` + "\n" +
		`{"procName":"Sim","procPath":"/Users/x/Library/Developer/CoreSimulator/Devices/` + udid + `/data/Containers/Bundle/Sim.app/Sim","exception":{"type":"EXC_CRASH","signal":"SIGABRT"}}`
	other := strings.ReplaceAll(mine, udid, "OTHER-UDID")
	_ = os.WriteFile(filepath.Join(dir, "Sim-2026.ips"), []byte(mine), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "Other-2026.ips"), []byte(other), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(mine), 0o600)

	svc := &Service{SimulatorDir: dir}
	p, err := svc.ProviderFor(device.Info{Platform: device.PlatformIOS, Simulator: true, Serial: udid})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := p.List(context.Background())
	if err != nil || len(rs) != 1 || rs[0].BundleID != "com.sim.app" || rs[0].Signal != "SIGABRT" {
		t.Fatalf("%+v %v", rs, err)
	}
	if txt, err := p.Get(context.Background(), "Sim-2026.ips"); err != nil || !strings.Contains(txt, "procName") {
		t.Fatalf("get: %v", err)
	}
	if _, err := p.Get(context.Background(), "../etc/passwd"); err != ErrNotFound {
		t.Fatalf("traversal: %v", err)
	}
	if err := p.Delete(context.Background(), "Sim-2026.ips"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Sim-2026.ips")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
}
