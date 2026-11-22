package iosbridge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- tar helpers ----

type tent struct {
	name, body, link string
	typ              byte
}

func makeTarGz(t *testing.T, ents []tent) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			_, _ = io.WriteString(tw, e.body)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func writeTar(t *testing.T, dir string, ents []tent) string {
	p := filepath.Join(dir, "t.tar.gz")
	if err := os.WriteFile(p, makeTarGz(t, ents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodTar() []tent {
	return []tent{
		{name: "WebDriverAgent-16.15.3/", typ: tar.TypeDir},
		{name: "WebDriverAgent-16.15.3/LICENSE", body: "BSD"},
		{name: "WebDriverAgent-16.15.3/WebDriverAgent.xcodeproj/project.pbxproj", body: "x"},
		{name: "WebDriverAgent-16.15.3/Scripts/run.sh", body: "#!/bin/sh"},
	}
}

func TestExtractStripsTopDir(t *testing.T) {
	d := t.TempDir()
	dst := filepath.Join(d, "src")
	if err := ExtractTarball(writeTar(t, d, goodTar()), dst); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"LICENSE", "WebDriverAgent.xcodeproj/project.pbxproj", "Scripts/run.sh"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	cases := map[string][]tent{
		"dotdot":      {{name: "top/../../evil", body: "x"}},
		"dotdot-deep": {{name: "top/a/../../../evil", body: "x"}},
		"absolute":    {{name: "/etc/evil", body: "x"}},
		"symlink-abs": {{name: "top/l", typ: tar.TypeSymlink, link: "/etc"}},
		"symlink-out": {{name: "top/l", typ: tar.TypeSymlink, link: "../../outside"}},
		"hardlink":    {{name: "top/h", typ: tar.TypeLink, link: "top/x"}},
		"backslash":   {{name: "top\\..\\evil", body: "x"}},
		"no-files":    {{name: "top/", typ: tar.TypeDir}},
	}
	for name, ents := range cases {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			dst := filepath.Join(d, "inner", "src")
			err := ExtractTarball(writeTar(t, d, ents), dst)
			if err == nil {
				t.Fatalf("expected error")
			}
			if _, e := os.Stat(filepath.Join(d, "evil")); e == nil {
				t.Fatal("file escaped extraction root")
			}
		})
	}
}

func TestPrepareSourceMarkerAndReextract(t *testing.T) {
	home := t.TempDir()
	tb := writeTar(t, t.TempDir(), goodTar())
	t.Setenv("PF_WDA_TARBALL", tb)
	if SourceReady(home) {
		t.Fatal("ready before extract")
	}
	var notes []string
	if err := PrepareSource(context.Background(), home, func(s string) { notes = append(notes, s) }); err != nil {
		t.Fatal(err)
	}
	if !SourceReady(home) || len(notes) == 0 {
		t.Fatalf("not ready after extract: %v", notes)
	}
	// second call is a no-op
	notes = nil
	if err := PrepareSource(context.Background(), home, func(s string) { notes = append(notes, s) }); err != nil || len(notes) != 0 {
		t.Fatalf("expected no-op, got %v %v", err, notes)
	}
	// pin change -> marker mismatch -> re-extract
	if err := os.WriteFile(filepath.Join(SrcDir(home), srcMarkerName), []byte("v0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(SrcDir(home), "stale.txt"), []byte("x"), 0o644)
	if SourceReady(home) {
		t.Fatal("stale marker accepted")
	}
	if err := PrepareSource(context.Background(), home, nil); err != nil {
		t.Fatal(err)
	}
	if !SourceReady(home) {
		t.Fatal("not re-extracted")
	}
	if _, err := os.Stat(filepath.Join(SrcDir(home), "stale.txt")); err == nil {
		t.Fatal("old source not replaced")
	}
}

func TestPrepareSourceDownloadFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PF_WDA_TARBALL", "")
	old := downloadFn
	defer func() { downloadFn = old }()
	called := false
	data := makeTarGz(t, goodTar())
	downloadFn = func(ctx context.Context, url, dst string) error {
		called = true
		return os.WriteFile(dst, data, 0o644)
	}
	if exe, err := os.Executable(); err == nil { // a tarball next to the test binary would shadow the fallback
		if _, e := os.Stat(filepath.Join(filepath.Dir(exe), WDATarballName)); e == nil {
			t.Skip("tarball next to test binary")
		}
	}
	if err := PrepareSource(context.Background(), home, nil); err != nil || !called || !SourceReady(home) {
		t.Fatalf("err=%v called=%v ready=%v", err, called, SourceReady(home))
	}
	// download failure -> structured issue
	_ = os.RemoveAll(home)
	downloadFn = func(context.Context, string, string) error { return errors.New("offline") }
	err := PrepareSource(context.Background(), home, nil)
	var is *Issue
	if !errors.As(err, &is) || is.Code != CodeSourceMissing || !strings.Contains(is.Message, "offline") {
		t.Fatalf("got %v", err)
	}
}

func TestTarballCandidatesOrder(t *testing.T) {
	t.Setenv("PF_WDA_TARBALL", "/x/custom.tgz")
	c := TarballCandidates("/home/wda")
	if c[0] != "/x/custom.tgz" || c[len(c)-1] != filepath.Join("/home/wda", WDATarballName) {
		t.Fatalf("%v", c)
	}
	var sawRes bool
	for _, p := range c {
		if strings.Contains(p, string(filepath.Separator)+"Resources"+string(filepath.Separator)) {
			sawRes = true
		}
	}
	if !sawRes {
		t.Fatalf("no Resources candidate: %v", c)
	}
}

// ---- fake runner ----

type fakeRunner struct {
	mu      sync.Mutex
	outputs map[string]fakeOut // key: name + " " + first arg
	stream  func(ctx context.Context, dir string, w io.Writer, args []string) error
	calls   []string
}
type fakeOut struct {
	out string
	err error
}

func (f *fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	f.calls = append(f.calls, key)
	if o, ok := f.outputs[key]; ok {
		return []byte(o.out), o.err
	}
	if o, ok := f.outputs[name]; ok {
		return []byte(o.out), o.err
	}
	return nil, errors.New("not found: " + key)
}

func (f *fakeRunner) Stream(ctx context.Context, dir string, w io.Writer, name string, args ...string) error {
	if f.stream == nil {
		return nil
	}
	return f.stream(ctx, dir, w, args)
}

func goodXcode() map[string]fakeOut {
	return map[string]fakeOut{
		"xcode-select -p":     {out: "/Applications/Xcode.app/Contents/Developer\n"},
		"xcodebuild -version": {out: "Xcode 27.2\nBuild version 27B52\n"},
	}
}

// ---- xcode checks ----

func TestCheckXcode(t *testing.T) {
	has := func(p string) bool { return p == "/Applications/Xcode.app" }
	none := func(string) bool { return false }
	cases := []struct {
		name string
		out  map[string]fakeOut
		stat func(string) bool
		want string // "" = ok
	}{
		{"ok", goodXcode(), has, ""},
		{"clt-but-xcode-present", map[string]fakeOut{"xcode-select -p": {out: "/Library/Developer/CommandLineTools\n"}}, has, CodeXcodeNotSelected},
		{"clt-only", map[string]fakeOut{"xcode-select -p": {out: "/Library/Developer/CommandLineTools\n"}}, none, CodeXcodeMissing},
		{"no-xcode-select", map[string]fakeOut{"xcode-select -p": {err: errors.New("exit 2")}}, none, CodeXcodeMissing},
		{"license", map[string]fakeOut{
			"xcode-select -p":     {out: "/Applications/Xcode.app/Contents/Developer"},
			"xcodebuild -version": {out: "You have not agreed to the Xcode license agreements.", err: errors.New("exit 69")},
		}, has, CodeXcodeLicense},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := CheckXcode(context.Background(), &fakeRunner{outputs: c.out}, c.stat)
			got := ""
			if is != nil {
				got = is.Code
				if is.Hint == "" {
					t.Error("issue without hint")
				}
			}
			if got != c.want {
				t.Fatalf("got %q want %q (%v)", got, c.want, is)
			}
		})
	}
	is := CheckXcode(context.Background(), &fakeRunner{outputs: map[string]fakeOut{"xcode-select -p": {out: "/Library/Developer/CommandLineTools"}}}, has)
	if !strings.Contains(is.Hint, "sudo xcode-select -s /Applications/Xcode.app/Contents/Developer") {
		t.Errorf("hint missing command: %q", is.Hint)
	}
	lic := CheckXcode(context.Background(), &fakeRunner{outputs: map[string]fakeOut{
		"xcode-select -p": {out: "/Applications/Xcode.app/Contents/Developer"}, "xcodebuild -version": {out: "license", err: errors.New("x")}}}, has)
	if !strings.Contains(lic.Hint, "sudo xcodebuild -license accept") {
		t.Errorf("license hint: %q", lic.Hint)
	}
}

// ---- team ----

func TestNormalizeTeamID(t *testing.T) {
	for in, want := range map[string]bool{"ABCDE12345": true, " abcde12345 ": true, "ABCDE1234": false, "ABCDE123456": false, "ABCDE-2345": false, "": false} {
		if _, ok := NormalizeTeamID(in); ok != want {
			t.Errorf("%q: got %v want %v", in, ok, want)
		}
	}
	if id, _ := NormalizeTeamID(" abcde12345 "); id != "ABCDE12345" {
		t.Errorf("not normalized: %q", id)
	}
}

func certPEM(t *testing.T, ou string, notAfter time.Time) string {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "Apple Development: Test (X)", OrganizationalUnit: []string{ou}},
		NotBefore:    time.Now().Add(-24 * time.Hour), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTeamFinderFallbacks(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	profDir := filepath.Join(home, "Library", "Developer", "Xcode", "UserData", "Provisioning Profiles")
	_ = os.MkdirAll(profDir, 0o755)
	_ = os.WriteFile(filepath.Join(profDir, "a.mobileprovision"), []byte("x"), 0o644)
	noEnv := func(string) string { return "" }
	profXML := "<dict><key>TeamIdentifier</key>\n<array>\n\t<string>PROF123456</string>\n</array></dict>"
	prefs := "(\n {\n  teamID = XCODE12345;\n  teamName = \"Me\";\n }\n)"

	certOK := certPEM(t, "CERT123456", time.Now().Add(24*time.Hour))
	certOld := certPEM(t, "OLDC123456", time.Now().Add(-time.Hour))

	run := func(o map[string]fakeOut) TeamFinder {
		return TeamFinder{Run: &fakeRunner{outputs: o}, Home: home, Env: noEnv}
	}
	// 1. env wins over everything
	f := run(map[string]fakeOut{"security find-certificate": {out: certOK}})
	f.Env = func(k string) string {
		if k == "PF_TEAM_ID" {
			return "env1234567"
		}
		return ""
	}
	f.Saved = func() string { return "SAVED12345" }
	if tm, err := f.Find(ctx); err != nil || tm.ID != "ENV1234567" || tm.Source != "env" {
		t.Fatalf("env: %v %v", tm, err)
	}
	// invalid env is an error, not a silent fallthrough
	f.Env = func(string) string { return "bad" }
	if _, err := f.Find(ctx); err == nil || err.(*Issue).Code != CodeTeamInvalid {
		t.Fatalf("invalid env: %v", err)
	}
	// 2. saved setting
	f = run(map[string]fakeOut{"security find-certificate": {out: certOK}})
	f.Saved = func() string { return "saved12345" }
	if tm, _ := f.Find(ctx); tm.ID != "SAVED12345" || tm.Source != "settings" {
		t.Fatalf("saved: %v", tm)
	}
	// 3. certificate (expired one ignored)
	f = run(map[string]fakeOut{"security find-certificate": {out: certOld + certOK}})
	if tm, _ := f.Find(ctx); tm.ID != "CERT123456" || tm.Source != "certificate" {
		t.Fatalf("cert: %v", tm)
	}
	// 4. profile when cert missing/expired
	f = run(map[string]fakeOut{"security find-certificate": {out: certOld}, "security cms": {out: profXML}, "defaults": {out: prefs}})
	if tm, _ := f.Find(ctx); tm.ID != "PROF123456" || tm.Source != "profile" {
		t.Fatalf("profile: %v", tm)
	}
	// 5. xcode prefs
	f = run(map[string]fakeOut{"security find-certificate": {err: errors.New("none")}, "defaults": {out: prefs}})
	if tm, _ := f.Find(ctx); tm.ID != "XCODE12345" || tm.Source != "xcode" {
		t.Fatalf("xcode: %v", tm)
	}
	// 6. nothing -> TEAM_NOT_FOUND
	f = run(map[string]fakeOut{})
	_, err := f.Find(ctx)
	var is *Issue
	if !errors.As(err, &is) || is.Code != CodeTeamNotFound || is.Hint == "" {
		t.Fatalf("none: %v", err)
	}
	// garbage values are rejected
	f = run(map[string]fakeOut{"defaults": {out: "teamID = short;"}})
	if _, err := f.Find(ctx); err == nil {
		t.Fatal("short team id accepted")
	}
}

// ---- classification ----

func TestClassifyBuildLog(t *testing.T) {
	cases := []struct{ name, log, want string }{
		{"devmode", "CompileC x\nerror: Developer Mode is disabled on 'iPhone'.", CodeDeveloperModeOff},
		{"appid", "error: Communication with Apple failed\nerror: Failed Registering Bundle Identifier: The app identifier cannot be registered. You have reached the maximum number of App IDs", CodeAppIDLimit},
		{"notregistered", "error: Provisioning profile \"iOS Team Provisioning Profile\" doesn't include the currently selected device", CodeDeviceNotRegisterd},
		{"nodevices", "error: Communication with Apple failed\nYour team has no devices from which to generate a provisioning profile.", CodeDeviceNotRegisterd},
		{"noteam", "error: Signing for \"WebDriverAgentRunner\" requires a development team. Select a development team in the Signing & Capabilities editor.", CodeProvisioning},
		{"noprofile", "error: No profiles for 'com.x.y' were found", CodeProvisioning},
		{"license", "error: You have not agreed to the Xcode license agreements.", CodeXcodeLicense},
		{"untrusted", "error: invalid code signature, inadequate entitlements or its profile has not been explicitly trusted by the user", CodeDeveloperNotTrust},
		{"generic", "CompileC foo\nerror: use of undeclared identifier 'x'\n** BUILD FAILED **", CodeBuildFailed},
		// build chatter before the first error must not decide the class
		{"chatter", "Provisioning Profile: \"X\"\nCompileC foo\nerror: use of undeclared identifier 'x'", CodeBuildFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := ClassifyBuildLog(strings.Split(c.log, "\n"))
			if is.Code != c.want {
				t.Fatalf("got %s want %s", is.Code, c.want)
			}
			if is.Hint == "" || is.Message == "" {
				t.Fatal("missing message/hint")
			}
			if c.want == CodeBuildFailed && len(is.Lines) == 0 {
				t.Fatal("generic failure should carry error lines")
			}
		})
	}
	if is := ClassifyBuildLog([]string{"error: Developer Mode is disabled"}); !strings.Contains(is.Hint, "Settings > Privacy & Security > Developer Mode") {
		t.Errorf("hint: %q", is.Hint)
	}
}

func TestClassifyInstallError(t *testing.T) {
	cases := map[string]string{
		"device not found (is it connected, unlocked and trusted?): x": CodeDeviceNotTrusted,
		"install failed: something":                                    CodeDeveloperNotTrust,
		"Unable to launch com.x: invalid code signature, inadequate entitlements or its profile has not been explicitly trusted": CodeDeveloperNotTrust,
		"Developer Mode is disabled": CodeDeveloperModeOff,
	}
	for in, want := range cases {
		is := ClassifyInstallError(errors.New(in))
		if is == nil || is.Code != want {
			t.Errorf("%q: got %v want %s", in, is, want)
		}
	}
	if ClassifyInstallError(errors.New("some random failure")) != nil {
		t.Error("unrecognised error should not classify")
	}
	if ClassifyInstallError(nil) != nil {
		t.Error("nil")
	}
}

// ---- builder state machine ----

func newTestBuilder(t *testing.T, r *fakeRunner) *Builder {
	home := t.TempDir()
	b := NewBuilder(filepath.Join(t.TempDir(), "settings.json"))
	b.Run = r
	b.Home = home
	b.GOOS = "darwin"
	b.UserHome = t.TempDir()
	b.Env = func(string) string { return "" }
	b.Prepare = func(ctx context.Context, h string, note func(string)) error {
		note("fake prepare")
		return os.MkdirAll(SrcDir(h), 0o755)
	}
	return b
}

func waitDone(t *testing.T, b *Builder) BuildStatus {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	b.Wait(ctx)
	if ctx.Err() != nil {
		t.Fatal("build did not finish")
	}
	return b.Status()
}

func TestBuilderSuccessDevice(t *testing.T) {
	out := goodXcode()
	r := &fakeRunner{outputs: out}
	b := newTestBuilder(t, r)
	r.stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		joined := strings.Join(args, " ")
		for _, want := range []string{"build-for-testing", "-allowProvisioningUpdates", "DEVELOPMENT_TEAM=TEAM123456", "generic/platform=iOS", "PRODUCT_BUNDLE_IDENTIFIER=com.phoneforge.WebDriverAgentRunner"} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q: %s", want, joined)
			}
		}
		if !strings.HasSuffix(dir, "src") {
			t.Errorf("dir %q", dir)
		}
		io.WriteString(w, "CompileC a.o a.m\nLd WebDriverAgentRunner\n** TEST BUILD SUCCEEDED **\n")
		return os.MkdirAll(b.RunnerApp(), 0o755)
	}
	st, err := b.Start(BuildOptions{TeamID: "team123456", SaveTeam: true})
	if err != nil || st.State != BuildRunning {
		t.Fatalf("start: %v %+v", err, st)
	}
	st = waitDone(t, b)
	if st.State != BuildSucceeded || st.Phase != PhaseDone || st.RunnerApp == "" || st.Issue != nil {
		t.Fatalf("final: %+v", st)
	}
	if !strings.Contains(strings.Join(st.Lines, "\n"), "CompileC a.o a.m") || st.Next == 0 {
		t.Fatalf("lines: %v", st.Lines)
	}
	if st.Team != "TEAM123456" {
		t.Fatalf("team %q", st.Team)
	}
	// team was saved to settings
	if got := b.teamFinder().Saved(); got != "TEAM123456" {
		t.Fatalf("saved team %q", got)
	}
	// log file written
	if lb, err := os.ReadFile(filepath.Join(b.Home, "build.log")); err != nil || !strings.Contains(string(lb), "Ld WebDriverAgentRunner") {
		t.Fatalf("log file: %v %q", err, lb)
	}
	// incremental lines
	inc := b.StatusSince(st.Next)
	if len(inc.Lines) != 0 {
		t.Fatalf("since=Next should yield none: %v", inc.Lines)
	}
	if first := b.StatusSince(0); len(first.Lines) != st.Next {
		t.Fatalf("since=0: %d lines want %d", len(first.Lines), st.Next)
	}
}

func TestBuilderSim(t *testing.T) {
	r := &fakeRunner{outputs: goodXcode()}
	b := newTestBuilder(t, r)
	r.stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-sdk iphonesimulator") || strings.Contains(joined, "DEVELOPMENT_TEAM") || !strings.Contains(joined, "CODE_SIGNING_ALLOWED=NO") {
			t.Errorf("sim args: %s", joined)
		}
		p := filepath.Join(b.Home, "build-sim", "Build", "Products")
		os.MkdirAll(p, 0o755)
		return os.WriteFile(filepath.Join(p, "WebDriverAgentRunner_iphonesimulator27.0-arm64.xctestrun"), []byte("x"), 0o644)
	}
	// no team needed for sim, even though none can be discovered
	if _, err := b.Start(BuildOptions{Mode: ModeSim}); err != nil {
		t.Fatal(err)
	}
	st := waitDone(t, b)
	if st.State != BuildSucceeded || st.XCTestRun == "" || st.Mode != ModeSim {
		t.Fatalf("%+v", st)
	}
	if !exists(filepath.Join(b.Home, "build-sim.log")) {
		t.Fatal("sim log missing")
	}
	if !b.Environment(context.Background()).SimBuilt {
		t.Fatal("env should report sim built")
	}
}

func TestBuilderPrechecks(t *testing.T) {
	// not macOS
	b := newTestBuilder(t, &fakeRunner{})
	b.GOOS = "linux"
	if _, err := b.Start(BuildOptions{}); err == nil || err.(*Issue).Code != CodeRequiresMac {
		t.Fatalf("linux: %v", err)
	}
	// xcode missing
	b = newTestBuilder(t, &fakeRunner{outputs: map[string]fakeOut{"xcode-select -p": {out: "/Library/Developer/CommandLineTools"}}})
	if _, err := b.Start(BuildOptions{}); err == nil || err.(*Issue).Code != CodeXcodeMissing && err.(*Issue).Code != CodeXcodeNotSelected {
		t.Fatalf("xcode: %v", err)
	}
	if b.Status().State != BuildIdle {
		t.Fatal("precheck failure must not start a build")
	}
	// no team
	b = newTestBuilder(t, &fakeRunner{outputs: goodXcode()})
	_, err := b.Start(BuildOptions{})
	if err == nil || err.(*Issue).Code != CodeTeamNotFound {
		t.Fatalf("team: %v", err)
	}
	// invalid team given by user
	if _, err := b.Start(BuildOptions{TeamID: "nope"}); err == nil || err.(*Issue).Code != CodeTeamInvalid {
		t.Fatalf("invalid: %v", err)
	}
	if b.teamFinder().Saved() != "" {
		t.Fatal("invalid team saved")
	}
	// a saved team is picked up without TeamID
	if _, err := b.SaveTeam("abcde12345"); err != nil {
		t.Fatal(err)
	}
	b.Run.(*fakeRunner).stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		return os.MkdirAll(b.RunnerApp(), 0o755)
	}
	if _, err := b.Start(BuildOptions{}); err != nil {
		t.Fatalf("saved team: %v", err)
	}
	if st := waitDone(t, b); st.Team != "ABCDE12345" || st.TeamSource != "settings" {
		t.Fatalf("%+v", st)
	}
}

func TestBuilderFailureClassifiedAndBusy(t *testing.T) {
	r := &fakeRunner{outputs: goodXcode()}
	b := newTestBuilder(t, r)
	release := make(chan struct{})
	r.stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		io.WriteString(w, "CompileC x\n")
		<-release
		io.WriteString(w, "error: Provisioning profile \"x\" doesn't include the currently selected device\n** BUILD FAILED **\n")
		return errors.New("exit status 65")
	}
	if _, err := b.Start(BuildOptions{TeamID: "ABCDE12345"}); err != nil {
		t.Fatal(err)
	}
	// second start while running -> busy
	if _, err := b.Start(BuildOptions{TeamID: "ABCDE12345"}); err == nil || err.(*Issue).Code != CodeBuildBusy {
		t.Fatalf("busy: %v", err)
	}
	close(release)
	st := waitDone(t, b)
	if st.State != BuildFailed || st.Issue == nil || st.Issue.Code != CodeDeviceNotRegisterd || len(st.Issue.Lines) == 0 {
		t.Fatalf("%+v", st)
	}
	// a new build can start afterwards and resets the state/log
	r.stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		return os.MkdirAll(b.RunnerApp(), 0o755)
	}
	if _, err := b.Start(BuildOptions{TeamID: "ABCDE12345"}); err != nil {
		t.Fatal(err)
	}
	st = waitDone(t, b)
	if st.State != BuildSucceeded || st.Issue != nil || strings.Contains(strings.Join(st.Lines, "\n"), "BUILD FAILED") {
		t.Fatalf("%+v", st)
	}
}

func TestBuilderSourceMissingAndCancel(t *testing.T) {
	r := &fakeRunner{outputs: goodXcode()}
	b := newTestBuilder(t, r)
	b.Prepare = func(context.Context, string, func(string)) error {
		return &Issue{Code: CodeSourceMissing, Message: "no tarball", Hint: "reinstall"}
	}
	b.Start(BuildOptions{TeamID: "ABCDE12345"})
	if st := waitDone(t, b); st.State != BuildFailed || st.Issue.Code != CodeSourceMissing {
		t.Fatalf("%+v", st)
	}

	b = newTestBuilder(t, r)
	started := make(chan struct{})
	r.stream = func(ctx context.Context, dir string, w io.Writer, args []string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	b.Start(BuildOptions{TeamID: "ABCDE12345"})
	<-started
	if !b.Cancel() {
		t.Fatal("cancel returned false")
	}
	if st := waitDone(t, b); st.State != BuildCanceled || st.Issue.Code != CodeCanceled {
		t.Fatalf("%+v", st)
	}
	if b.Cancel() {
		t.Fatal("cancel with nothing running")
	}
}

func TestEnvironment(t *testing.T) {
	r := &fakeRunner{outputs: goodXcode()}
	b := newTestBuilder(t, r)
	e := b.Environment(context.Background())
	if !e.Supported || e.Xcode != nil || e.XcodeVersion != "Xcode 27.2" || e.TeamIssue == nil || e.TeamIssue.Code != CodeTeamNotFound || e.RunnerBuilt || e.WDATag != WDATag {
		t.Fatalf("%+v", e)
	}
	b.GOOS = "linux"
	if e := b.Environment(context.Background()); e.Supported {
		t.Fatal("linux must be unsupported")
	}
}

func TestBuilderLineCap(t *testing.T) {
	b := newTestBuilder(t, &fakeRunner{})
	for i := 0; i < ringLines+50; i++ {
		b.addLine("line")
	}
	st := b.StatusSince(0)
	if st.Next != ringLines+50 || len(st.Lines) != ringLines {
		t.Fatalf("next=%d lines=%d", st.Next, len(st.Lines))
	}
	if len(b.Status().Lines) != statusTail {
		t.Fatalf("tail %d", len(b.Status().Lines))
	}
}
