package iosbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/config"
)

// Build modes.
const (
	ModeDevice = "device"
	ModeSim    = "sim"
)

// Build states.
const (
	BuildIdle      = "idle"
	BuildRunning   = "running"
	BuildSucceeded = "succeeded"
	BuildFailed    = "failed"
	BuildCanceled  = "canceled"
)

// Build phases (BuildStatus.Phase), in order.
const (
	PhaseChecking   = "checking"
	PhaseExtracting = "extracting"
	PhaseBuilding   = "building"
	PhaseDone       = "done"
)

// ErrBuildBusy means a build is already running.
var ErrBuildBusy = &Issue{Code: CodeBuildBusy, Message: "A WebDriverAgent build is already running.", Hint: "Wait for it to finish (or cancel it)."}

const (
	ringLines   = 400
	maxLineLen  = 2000
	statusTail  = 80
	devBundleID = "com.phoneforge"
)

// BuildOptions selects what to build.
type BuildOptions struct {
	Mode string // ModeDevice (default) or ModeSim
	// TeamID overrides discovery for this build (device mode); it is validated
	// and, when SaveTeam is set, persisted in the PhoneForge settings.
	TeamID   string
	SaveTeam bool
}

// BuildStatus is the JSON shape of a build.
type BuildStatus struct {
	State      string    `json:"state"`
	Phase      string    `json:"phase,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Detail     string    `json:"detail,omitempty"` // latest meaningful log line
	Team       string    `json:"team,omitempty"`
	TeamSource string    `json:"teamSource,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Issue      *Issue    `json:"issue,omitempty"`
	RunnerApp  string    `json:"runnerApp,omitempty"`
	XCTestRun  string    `json:"xctestrun,omitempty"`
	LogFile    string    `json:"logFile,omitempty"`
	Lines      []string  `json:"lines"`            // log lines starting at index Next-len(Lines)
	Next       int       `json:"next"`             // total number of lines so far (use as ?since=)
	Source     string    `json:"source,omitempty"` // tarball the source came from
}

// Env describes the build environment (for the UI's pre-flight view).
type Env struct {
	Supported    bool   `json:"supported"`       // macOS only
	Xcode        *Issue `json:"xcode,omitempty"` // nil when Xcode is fine
	XcodeVersion string `json:"xcodeVersion,omitempty"`
	Team         *Team  `json:"team,omitempty"`
	TeamIssue    *Issue `json:"teamIssue,omitempty"`
	Tarball      string `json:"tarball,omitempty"`
	SourceReady  bool   `json:"sourceReady"`
	WDATag       string `json:"wdaVersion"`
	RunnerBuilt  bool   `json:"runnerBuilt"`
	RunnerApp    string `json:"runnerApp,omitempty"`
	SimBuilt     bool   `json:"simBuilt"`
	Home         string `json:"home"`
}

// Builder builds WebDriverAgent from the shipped source. One build at a time.
type Builder struct {
	Run          Runner
	Home         string                                                          // WDA home; "" = WDAHome()
	SettingsPath string                                                          // user settings file ("" disables saving)
	GOOS         string                                                          // "" = runtime.GOOS
	Env          func(string) string                                             // nil = os.Getenv
	UserHome     string                                                          // "" = os.UserHomeDir()
	Prepare      func(ctx context.Context, home string, note func(string)) error // nil = PrepareSource

	mu     sync.Mutex
	st     BuildStatus
	ring   []string
	total  int // lines ever appended
	cancel context.CancelFunc
	done   chan struct{}
}

// NewBuilder returns a Builder using the real xcodebuild.
func NewBuilder(settingsPath string) *Builder {
	return &Builder{Run: ExecRunner{}, SettingsPath: settingsPath, st: BuildStatus{State: BuildIdle}}
}

func (b *Builder) home() string {
	if b.Home != "" {
		return b.Home
	}
	return WDAHome()
}

func (b *Builder) goos() string {
	if b.GOOS != "" {
		return b.GOOS
	}
	return runtime.GOOS
}

func (b *Builder) getenv(k string) string {
	if b.Env != nil {
		return b.Env(k)
	}
	return os.Getenv(k)
}

func (b *Builder) bundlePrefix() string {
	if v := b.getenv("PF_BUNDLE_PREFIX"); v != "" {
		return v
	}
	return devBundleID
}

func (b *Builder) teamFinder() TeamFinder {
	uh := b.UserHome
	if uh == "" {
		uh, _ = os.UserHomeDir()
	}
	tf := TeamFinder{Run: b.Run, Home: uh, Env: b.Env}
	if b.SettingsPath != "" {
		p := b.SettingsPath
		tf.Saved = func() string { return config.ReadSetting(p, config.KeyWDATeamID) }
	}
	return tf
}

// RunnerApp is the signed device runner path under this builder's home.
func (b *Builder) RunnerApp() string {
	return filepath.Join(b.home(), "build", "Build", "Products", "Debug-iphoneos", "WebDriverAgentRunner-Runner.app")
}

// SimXCTestRun returns the simulator .xctestrun path, or "".
func (b *Builder) SimXCTestRun() string {
	m, _ := filepath.Glob(filepath.Join(b.home(), "build-sim", "Build", "Products", "*iphonesimulator*.xctestrun"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// EnvironmentQuick reports only what is known without running any command
// (support, source, built flags).
func (b *Builder) EnvironmentQuick() Env { return b.environment(context.Background(), true) }

// Environment runs the pre-flight checks (Xcode, team).
func (b *Builder) Environment(ctx context.Context) Env { return b.environment(ctx, false) }

func (b *Builder) environment(ctx context.Context, quick bool) Env {
	home := b.home()
	e := Env{
		Supported:   b.goos() == "darwin",
		WDATag:      WDATag,
		Tarball:     FindTarball(home),
		SourceReady: SourceReady(home),
		RunnerBuilt: exists(b.RunnerApp()),
		SimBuilt:    b.SimXCTestRun() != "",
		Home:        home,
	}
	if e.RunnerBuilt {
		e.RunnerApp = b.RunnerApp()
	}
	if !e.Supported || quick {
		return e
	}
	e.Xcode = CheckXcode(ctx, b.Run, nil)
	if e.Xcode == nil {
		e.XcodeVersion = XcodeVersion(ctx, b.Run)
	}
	if t, err := b.teamFinder().Find(ctx); err != nil {
		var is *Issue
		if errors.As(err, &is) {
			e.TeamIssue = is
		}
	} else {
		e.Team = &t
	}
	return e
}

// SaveTeam validates and persists the team id in the PhoneForge settings.
func (b *Builder) SaveTeam(id string) (string, error) {
	n, ok := NormalizeTeamID(id)
	if !ok {
		return "", &Issue{Code: CodeTeamInvalid, Message: "A Team ID is exactly 10 letters or digits (for example ABCDE12345).", Hint: "Find it in Xcode > Settings > Accounts > select your team, or at developer.apple.com > Membership details."}
	}
	if b.SettingsPath == "" {
		return n, errors.New("settings store not configured")
	}
	return n, config.WriteSetting(b.SettingsPath, config.KeyWDATeamID, n)
}

// Status returns the current build with the last lines of log.
func (b *Builder) Status() BuildStatus { return b.StatusSince(-1) }

// StatusSince returns the status with log lines from index since (<0 = the
// last statusTail lines).
func (b *Builder) StatusSince(since int) BuildStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.st
	first := b.total - len(b.ring) // index of ring[0]
	from := since
	if from < 0 {
		from = b.total - statusTail
	}
	if from < first {
		from = first
	}
	if from < 0 {
		from = 0
	}
	if from > b.total {
		from = b.total
	}
	st.Lines = append(make([]string, 0, len(b.ring)-(from-first)), b.ring[from-first:]...)
	st.Next = b.total
	return st
}

// Wait blocks until the running build (if any) finishes or ctx ends.
func (b *Builder) Wait(ctx context.Context) {
	b.mu.Lock()
	d := b.done
	b.mu.Unlock()
	if d == nil {
		return
	}
	select {
	case <-d:
	case <-ctx.Done():
	}
}

// Cancel stops a running build. It reports whether one was running.
func (b *Builder) Cancel() bool {
	b.mu.Lock()
	c := b.cancel
	running := b.st.State == BuildRunning
	b.mu.Unlock()
	if c != nil && running {
		c()
		return true
	}
	return false
}

func (b *Builder) logf(format string, a ...any) { b.addLine(fmt.Sprintf(format, a...)) }

func (b *Builder) addLine(s string) {
	s = strings.TrimRight(s, "\r\n ")
	if s == "" {
		return
	}
	if len(s) > maxLineLen {
		s = s[:maxLineLen] + "..."
	}
	b.mu.Lock()
	b.ring = append(b.ring, s)
	if len(b.ring) > ringLines {
		b.ring = b.ring[len(b.ring)-ringLines:]
	}
	b.total++
	b.st.Detail = detailOf(s, b.st.Detail)
	b.mu.Unlock()
}

// detailOf picks a short human string from an xcodebuild line.
func detailOf(line, prev string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "/") && !strings.Contains(t, " ") {
		return prev
	}
	for _, p := range []string{"CompileC ", "CompileSwift ", "Ld ", "CodeSign ", "ProcessInfoPlistFile ", "CopySwiftLibs ", "Touch ", "SwiftCompile ", "Resolve ", "Signing ", "Linking "} {
		if strings.HasPrefix(t, p) {
			if len(t) > 140 {
				t = t[:140] + "..."
			}
			return t
		}
	}
	if strings.HasPrefix(t, "==>") {
		return strings.TrimSpace(strings.TrimPrefix(t, "==>"))
	}
	return prev
}

func (b *Builder) setPhase(p string) {
	b.mu.Lock()
	b.st.Phase = p
	b.mu.Unlock()
}

// Start validates the environment synchronously (returning a structured
// *Issue when something must be fixed first) and then builds in the background.
func (b *Builder) Start(opts BuildOptions) (BuildStatus, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeDevice
	}
	if mode != ModeDevice && mode != ModeSim {
		return BuildStatus{}, fmt.Errorf("mode must be %q or %q", ModeDevice, ModeSim)
	}
	if b.goos() != "darwin" {
		return BuildStatus{}, &Issue{Code: CodeRequiresMac, Message: "Building WebDriverAgent requires macOS with Xcode.", Hint: "Run PhoneForge on a Mac to control iPhones."}
	}
	b.mu.Lock()
	if b.st.State == BuildRunning {
		b.mu.Unlock()
		return BuildStatus{}, ErrBuildBusy
	}
	b.mu.Unlock()

	pctx, pcancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pcancel()
	if is := CheckXcode(pctx, b.Run, nil); is != nil {
		return BuildStatus{}, is
	}
	var team Team
	if mode == ModeDevice {
		if opts.TeamID != "" {
			id, ok := NormalizeTeamID(opts.TeamID)
			if !ok {
				return BuildStatus{}, &Issue{Code: CodeTeamInvalid, Message: "A Team ID is exactly 10 letters or digits (for example ABCDE12345).", Hint: "Find it in Xcode > Settings > Accounts > select your team."}
			}
			team = Team{ID: id, Source: "request"}
			if opts.SaveTeam && b.SettingsPath != "" {
				if err := config.WriteSetting(b.SettingsPath, config.KeyWDATeamID, id); err != nil {
					return BuildStatus{}, fmt.Errorf("could not save the Team ID: %w", err)
				}
			}
		} else {
			t, err := b.teamFinder().Find(pctx)
			if err != nil {
				return BuildStatus{}, err
			}
			team = t
		}
	}

	b.mu.Lock()
	if b.st.State == BuildRunning { // lost a race
		b.mu.Unlock()
		return BuildStatus{}, ErrBuildBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.done = make(chan struct{})
	b.ring, b.total = nil, 0
	b.st = BuildStatus{State: BuildRunning, Phase: PhaseChecking, Mode: mode, Team: team.ID, TeamSource: team.Source, StartedAt: time.Now(), LogFile: b.logPath(mode)}
	done := b.done
	b.mu.Unlock()

	go func() {
		defer close(done)
		defer cancel()
		b.run(ctx, mode, team)
	}()
	return b.Status(), nil
}

func (b *Builder) logPath(mode string) string {
	if mode == ModeSim {
		return filepath.Join(b.home(), "build-sim.log")
	}
	return filepath.Join(b.home(), "build.log")
}

func (b *Builder) finish(state string, is *Issue) {
	b.mu.Lock()
	b.st.State = state
	b.st.Phase = PhaseDone
	b.st.Issue = is
	b.st.FinishedAt = time.Now()
	if is == nil && state == BuildSucceeded {
		b.st.Detail = "Build succeeded"
	} else if is != nil {
		b.st.Detail = is.Message
	}
	b.mu.Unlock()
}

func (b *Builder) run(ctx context.Context, mode string, team Team) {
	home := b.home()
	if err := os.MkdirAll(home, 0o755); err != nil {
		b.finish(BuildFailed, &Issue{Code: CodeBuildFailed, Message: "Cannot create " + home + ": " + err.Error()})
		return
	}
	logf, lerr := os.Create(b.logPath(mode))
	if lerr == nil {
		defer logf.Close()
	}
	sink := &lineSink{b: b, file: logf}
	b.logf("==> WebDriverAgent %s, %s build", WDATag, mode)
	if team.ID != "" {
		b.logf("==> Using development team %s (from %s)", team.ID, team.Source)
	}

	// Source.
	b.setPhase(PhaseExtracting)
	prep := b.Prepare
	if prep == nil {
		prep = PrepareSource
	}
	if err := prep(ctx, home, func(s string) { b.logf("==> %s", s); sink.fileLine("==> " + s) }); err != nil {
		var is *Issue
		if !errors.As(err, &is) {
			is = &Issue{Code: CodeSourceMissing, Message: err.Error()}
		}
		b.finish(BuildFailed, is)
		return
	}
	if t := FindTarball(home); t != "" {
		b.mu.Lock()
		b.st.Source = t
		b.mu.Unlock()
	}

	// Build.
	b.setPhase(PhaseBuilding)
	prefix := b.bundlePrefix()
	runnerID, libID := prefix+".WebDriverAgentRunner", prefix+".WebDriverAgentLib"
	b.logf("==> Runner bundle id: %s", runnerID)
	var args []string
	if mode == ModeSim {
		args = []string{"build-for-testing",
			"-project", "WebDriverAgent.xcodeproj", "-scheme", "WebDriverAgentRunner",
			"-sdk", "iphonesimulator", "-destination", "generic/platform=iOS Simulator",
			"-derivedDataPath", filepath.Join(home, "build-sim"),
			"CODE_SIGNING_ALLOWED=NO", "CODE_SIGNING_REQUIRED=NO", "CODE_SIGN_IDENTITY=",
			"PRODUCT_BUNDLE_IDENTIFIER=" + runnerID, "WDA_PRODUCT_BUNDLE_IDENTIFIER=" + libID}
	} else {
		args = []string{"build-for-testing",
			"-project", "WebDriverAgent.xcodeproj", "-scheme", "WebDriverAgentRunner",
			"-destination", "generic/platform=iOS",
			"-derivedDataPath", filepath.Join(home, "build"),
			"-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration",
			"DEVELOPMENT_TEAM=" + team.ID, "CODE_SIGN_STYLE=Automatic",
			"PRODUCT_BUNDLE_IDENTIFIER=" + runnerID, "WDA_PRODUCT_BUNDLE_IDENTIFIER=" + libID}
	}
	b.logf("==> xcodebuild %s", strings.Join(args, " "))
	err := b.Run.Stream(ctx, SrcDir(home), sink, "xcodebuild", args...)
	sink.flush()
	if ctx.Err() != nil {
		b.finish(BuildCanceled, &Issue{Code: CodeCanceled, Message: "The build was canceled."})
		return
	}
	if err != nil {
		b.logf("==> xcodebuild failed: %v", err)
		b.mu.Lock()
		lines := append([]string(nil), b.ring...)
		b.mu.Unlock()
		is := ClassifyBuildLog(lines)
		is.Message = strings.TrimSuffix(is.Message, ".") + "."
		b.finish(BuildFailed, is)
		return
	}
	if mode == ModeSim {
		x := b.SimXCTestRun()
		if x == "" {
			b.finish(BuildFailed, &Issue{Code: CodeBuildFailed, Message: "Build reported success but no simulator .xctestrun was produced.", Hint: "See " + b.logPath(mode)})
			return
		}
		b.mu.Lock()
		b.st.XCTestRun = x
		b.mu.Unlock()
	} else {
		app := b.findRunnerApp()
		if app == "" {
			b.finish(BuildFailed, &Issue{Code: CodeBuildFailed, Message: "Build reported success but WebDriverAgentRunner-Runner.app was not found.", Hint: "See " + b.logPath(mode)})
			return
		}
		b.mu.Lock()
		b.st.RunnerApp = app
		b.mu.Unlock()
	}
	b.logf("==> Build succeeded")
	b.finish(BuildSucceeded, nil)
}

func (b *Builder) findRunnerApp() string {
	if p := b.RunnerApp(); exists(p) {
		return p
	}
	m, _ := filepath.Glob(filepath.Join(b.home(), "build", "Build", "Products", "*", "WebDriverAgentRunner-Runner.app"))
	if len(m) > 0 {
		return m[0]
	}
	return ""
}

// lineSink splits command output into lines for the ring and the log file.
type lineSink struct {
	b    *Builder
	file *os.File
	mu   sync.Mutex
	buf  []byte
}

func (s *lineSink) fileLine(l string) {
	if s.file != nil {
		_, _ = io.WriteString(s.file, l+"\n")
	}
}

func (s *lineSink) Write(p []byte) (int, error) {
	if s.file != nil {
		_, _ = s.file.Write(p)
	}
	s.mu.Lock()
	s.buf = append(s.buf, p...)
	var lines []string
	for {
		i := indexNL(s.buf)
		if i < 0 {
			break
		}
		lines = append(lines, string(s.buf[:i]))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > 1<<20 {
		s.buf = s.buf[:0]
	}
	s.mu.Unlock()
	for _, l := range lines {
		s.b.addLine(l)
	}
	return len(p), nil
}

func (s *lineSink) flush() {
	s.mu.Lock()
	rest := string(s.buf)
	s.buf = nil
	s.mu.Unlock()
	if rest != "" {
		s.b.addLine(rest)
	}
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
