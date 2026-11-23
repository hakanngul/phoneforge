package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Installer performs the platform-specific part of an update.
type Installer interface {
	// Prepare extracts the downloaded archive and fully verifies the candidate
	// (signature, identity, version). Nothing running is touched.
	Prepare(archive string, m *Manifest) (candidate string, err error)
	// Apply swaps the verified candidate in and returns where the old app was kept.
	Apply(candidate string, version string) (backup string, err error)
	// Relaunch starts the new app once this process has exited.
	Relaunch() error
	// Rollback restores the app kept at backup.
	Rollback(backup string) error
	// Supported reports whether in-place install is possible; manualReason
	// explains why not (translocated, read-only, unsupported platform).
	Supported() (ok bool, manualReason string)
}

// Hooks connect the manager to the host application. All are optional.
type Hooks struct {
	OnChange           func(Status)                       // state changed (called without locks held; must not block)
	Busy               func() (bool, string)              // refuse installs while a test run/recording is active
	BeforeInstall      func(version string) error         // stop the owned server
	AfterFailedInstall func()                             // restart the server after a failed install
	Quit               func()                             // exit the app after a successful install
	Record             func(kind, reason, message string) // lifecycle event
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Dir            string // state directory (e.g. ~/.pocketlab); holds update.json and updates/
	CurrentVersion string
	Checker        Config
	Installer      Installer
	Disabled       bool
	DisabledReason string
	// ManualURL returns the page to send the user to when in-place install is
	// not possible (DMG / release page). May be nil.
	ManualURL       func(*Manifest) string
	Hooks           Hooks
	Now             func() time.Time
	FirstCheckDelay time.Duration // default 30s
	Interval        time.Duration // default 24h
}

// Status is the JSON served by GET /api/v1/update.
type Status struct {
	Current     string         `json:"current"`
	Disabled    bool           `json:"disabled"`
	Supported   bool           `json:"supported"`
	State       string         `json:"state"`
	Available   *Available     `json:"available"`
	Skipped     bool           `json:"skipped"`
	RemindUntil *time.Time     `json:"remindUntil"`
	Progress    *Progress      `json:"progress"`
	Error       string         `json:"error"`
	LastCheck   *time.Time     `json:"lastCheck"`
	ManualURL   string         `json:"manualUrl"`
	Settings    Settings       `json:"settings"`
	Rollback    *RollbackOffer `json:"rollback"`
}

// Available describes the offered update.
type Available struct {
	Version   string `json:"version"`
	NotesMD   string `json:"notesMd"`
	NotesURL  string `json:"notesUrl"`
	Published string `json:"published"`
	Size      int64  `json:"size"`
}

// Progress is the download progress.
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// RollbackOffer is shown when a fresh update did not start properly. From is
// the version the backup restores, To the version that is currently installed.
type RollbackOffer struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// State names.
const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateUpToDate    = "up-to-date"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateInstalling  = "installing"
	StateError       = "error"
)

// ErrBusyOp is returned when another check/download/install is running.
var ErrBusyOp = errors.New("update: another update operation is in progress")

// Manager ties checking, downloading, installing and rollback together.
type Manager struct {
	opts    ManagerOptions
	checker *Checker

	mu         sync.Mutex
	st         *State
	state      string
	result     *Result
	progress   *Progress
	errMsg     string
	downloaded string // verified archive for result
	rollback   *RollbackOffer
	opActive   bool
	baseCtx    context.Context
	kick       chan struct{}
	wg         sync.WaitGroup
}

// NewManager loads persisted state and, from the cached manifest, any
// previously found update (no network).
func NewManager(opts ManagerOptions) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FirstCheckDelay <= 0 {
		opts.FirstCheckDelay = 30 * time.Second
	}
	if opts.Interval <= 0 {
		opts.Interval = 24 * time.Hour
	}
	if opts.Checker.CurrentVersion == "" {
		opts.Checker.CurrentVersion = opts.CurrentVersion
	}
	m := &Manager{opts: opts, state: StateIdle, baseCtx: context.Background(), kick: make(chan struct{}, 1)}
	m.st = LoadState(opts.Dir)
	m.checker = m.newChecker()
	if len(m.st.ManifestData) > 0 && len(m.st.ManifestSig) > 0 {
		if mf, err := VerifyManifest(m.st.ManifestData, m.st.ManifestSig, m.checker.cfg.PublicKeys); err == nil {
			if res := m.checker.Evaluate(mf); res.Available {
				m.result = &res
				m.state = StateAvailable
			} else if !m.st.LastCheck.IsZero() {
				m.state = StateUpToDate
			}
		}
	}
	return m
}

// newChecker builds a Checker for the live channel setting.
func (m *Manager) newChecker() *Checker {
	cfg := m.opts.Checker
	cfg.Channel = m.st.Settings.Channel
	return NewChecker(cfg)
}

func (m *Manager) now() time.Time { return m.opts.Now() }

func (m *Manager) save() { _ = m.st.Save(m.opts.Dir) }

func (m *Manager) updatesDir() string { return filepath.Join(m.opts.Dir, "updates") }

// ---- status ----

// Status returns the current status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() Status {
	s := Status{
		Current:  m.opts.CurrentVersion,
		Disabled: m.opts.Disabled,
		State:    m.state,
		Error:    m.errMsg,
		Settings: m.st.Settings,
		Rollback: m.rollback,
	}
	if m.opts.Disabled {
		s.State = StateIdle
		if m.errMsg == "" {
			s.Error = m.opts.DisabledReason
		}
	}
	ok, reason := false, ""
	if m.opts.Installer != nil {
		ok, reason = m.opts.Installer.Supported()
	}
	s.Supported = ok && !m.opts.Disabled
	if m.result != nil && m.result.Available {
		a := m.result.Asset
		s.Available = &Available{Version: m.result.Manifest.Version, NotesMD: m.result.Manifest.NotesMD,
			NotesURL: m.result.Manifest.NotesURL, Published: m.result.Manifest.Published, Size: a.Size}
		s.Skipped = m.st.SkipVersion != "" && m.st.SkipVersion == m.result.Manifest.Version
		if !ok && m.opts.ManualURL != nil {
			s.ManualURL = m.opts.ManualURL(m.result.Manifest)
		}
		if !ok && s.Error == "" {
			s.Error = reason
		}
	}
	if m.st.RemindUntil.After(m.now()) {
		t := m.st.RemindUntil
		s.RemindUntil = &t
	}
	if m.progress != nil {
		p := *m.progress
		s.Progress = &p
	}
	if !m.st.LastCheck.IsZero() {
		t := m.st.LastCheck
		s.LastCheck = &t
	}
	return s
}

// setState updates state/error and notifies.
func (m *Manager) setState(state, errMsg string) {
	m.mu.Lock()
	m.state, m.errMsg = state, errMsg
	if state != StateDownloading {
		m.progress = nil
	}
	s := m.statusLocked()
	m.mu.Unlock()
	m.notify(s)
}

func (m *Manager) notify(s Status) {
	if m.opts.Hooks.OnChange != nil {
		m.opts.Hooks.OnChange(s)
	}
}

func (m *Manager) record(kind, reason, msg string) {
	if m.opts.Hooks.Record != nil {
		m.opts.Hooks.Record(kind, reason, msg)
	}
}

// settledState is the state to show when no operation is running.
func (m *Manager) settledStateLocked() string {
	if m.result != nil && m.result.Available {
		if m.downloaded != "" {
			return StateReady
		}
		return StateAvailable
	}
	if !m.st.LastCheck.IsZero() {
		return StateUpToDate
	}
	return StateIdle
}

// begin takes the single operation slot.
func (m *Manager) begin() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.opActive {
		return ErrBusyOp
	}
	m.opActive = true
	return nil
}

func (m *Manager) end() {
	m.mu.Lock()
	m.opActive = false
	m.mu.Unlock()
}

// Wait blocks until background operations started by the manager finish.
func (m *Manager) Wait() { m.wg.Wait() }

// ---- checking ----

// CheckNow checks the feed immediately, even when auto-check is off.
func (m *Manager) CheckNow(ctx context.Context) (Status, error) {
	if m.opts.Disabled {
		return m.Status(), ErrDisabled
	}
	if err := m.begin(); err != nil {
		return m.Status(), err
	}
	defer m.end()
	err := m.check(ctx)
	return m.Status(), err
}

func (m *Manager) check(ctx context.Context) error {
	m.setState(StateChecking, "")
	m.mu.Lock()
	snap := *m.st
	checker := m.newChecker()
	m.mu.Unlock()

	res, err := checker.Check(ctx, &snap)

	m.mu.Lock()
	m.st.LastCheck, m.st.LastError, m.st.Failures = snap.LastCheck, snap.LastError, snap.Failures
	if err == nil {
		m.st.ETag, m.st.ManifestData, m.st.ManifestSig = snap.ETag, snap.ManifestData, snap.ManifestSig
		keep := m.result != nil && res.Available && m.result.Manifest.Version == res.Manifest.Version
		if !keep {
			m.downloaded = ""
		}
		if res.Available {
			r := res
			m.result = &r
		} else {
			m.result = nil
			m.downloaded = ""
		}
		m.errMsg = ""
		m.state = m.settledStateLocked()
	} else {
		m.errMsg = err.Error()
		m.state = StateError
	}
	m.save()
	auto := err == nil && res.Available && m.st.Settings.AutoDownload && m.downloaded == "" &&
		m.st.SkipVersion != res.Manifest.Version
	s := m.statusLocked()
	m.mu.Unlock()
	m.notify(s)
	if auto {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			_ = m.Download(m.baseCtx)
		}()
	}
	return err
}

// Start runs the background check loop until ctx ends. Settings are read live;
// with AutoCheck off no network request is made.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		next := m.now().Add(m.opts.FirstCheckDelay)
		for {
			wait := next.Sub(m.now())
			if wait < 0 {
				wait = 0
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-m.kick:
				t.Stop()
				m.mu.Lock()
				if m.st.Settings.AutoCheck && !m.opts.Disabled && m.checkDueLocked() {
					next = m.now()
				}
				m.mu.Unlock()
				continue
			case <-t.C:
			}
			m.mu.Lock()
			auto := m.st.Settings.AutoCheck && !m.opts.Disabled
			m.mu.Unlock()
			if !auto {
				next = m.now().Add(time.Minute) // cheap local poll for the setting
				if m.opts.Interval < time.Minute {
					next = m.now().Add(m.opts.Interval)
				}
				continue
			}
			if err := m.begin(); err != nil {
				next = m.now().Add(time.Minute)
				continue
			}
			err := m.check(ctx)
			m.end()
			if err != nil {
				m.mu.Lock()
				f := m.st.Failures
				m.mu.Unlock()
				next = m.now().Add(Backoff(f))
			} else {
				next = m.now().Add(jitter(m.opts.Interval))
			}
		}
	}()
}

func (m *Manager) checkDueLocked() bool {
	return m.st.LastCheck.IsZero() || m.now().Sub(m.st.LastCheck) >= m.opts.Interval
}

// ---- user choices ----

// Skip hides the given version until a newer one appears.
func (m *Manager) Skip(version string) {
	m.mu.Lock()
	m.st.SkipVersion = version
	m.save()
	s := m.statusLocked()
	m.mu.Unlock()
	m.notify(s)
}

// Remind hides the update banner for d.
func (m *Manager) Remind(d time.Duration) {
	if d <= 0 {
		d = 24 * time.Hour
	}
	m.mu.Lock()
	m.st.RemindUntil = m.now().Add(d)
	m.save()
	s := m.statusLocked()
	m.mu.Unlock()
	m.notify(s)
}

// SetSettings validates and applies settings without a restart.
func (m *Manager) SetSettings(s Settings) error {
	if s.Channel == "" {
		s.Channel = "stable"
	}
	if s.Channel != "stable" && s.Channel != "beta" {
		return fmt.Errorf("invalid channel %q", s.Channel)
	}
	m.mu.Lock()
	if s.Channel != m.st.Settings.Channel {
		// Different feed: drop the cache and any offered update.
		m.st.ETag, m.st.ManifestData, m.st.ManifestSig = "", nil, nil
		if !m.opActive {
			m.result, m.downloaded = nil, ""
			m.state = StateIdle
		}
	}
	m.st.Settings = s
	m.save()
	st := m.statusLocked()
	m.mu.Unlock()
	m.notify(st)
	select {
	case m.kick <- struct{}{}:
	default:
	}
	return nil
}

// ---- download / install ----

// Download fetches and verifies the offered update's archive.
func (m *Manager) Download(ctx context.Context) error {
	if err := m.preconditions(false); err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	defer m.end()
	_, err := m.download(ctx)
	return err
}

func (m *Manager) preconditions(needInstall bool) error {
	if m.opts.Disabled {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.result == nil || !m.result.Available {
		return ErrNothingToUpdate
	}
	if needInstall {
		if m.opts.Installer == nil {
			return ErrUnsupported
		}
		if ok, why := m.opts.Installer.Supported(); !ok {
			return fmt.Errorf("%w: %s", ErrUnsupported, why)
		}
	}
	return nil
}

func (m *Manager) download(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.downloaded != "" {
		if _, err := os.Stat(m.downloaded); err == nil {
			p := m.downloaded
			m.mu.Unlock()
			return p, nil
		}
		m.downloaded = ""
	}
	res := *m.result
	m.mu.Unlock()
	m.setState(StateDownloading, "")
	client := m.opts.Checker.HTTPClient
	if client == nil || client.Timeout != 0 {
		client = NewHTTPClient(0) // a whole-download timeout would cut big files
	}
	dir := filepath.Join(m.updatesDir(), res.Manifest.Version)
	path, err := Download(ctx, client, res.Asset, dir, func(done, total int64) {
		m.mu.Lock()
		m.progress = &Progress{Done: done, Total: total}
		s := m.statusLocked()
		m.mu.Unlock()
		m.notify(s)
	})
	if err != nil {
		m.setState(StateError, err.Error())
		return "", err
	}
	m.mu.Lock()
	m.downloaded = path
	m.mu.Unlock()
	m.setState(StateReady, "")
	return path, nil
}

// StartDownload begins a download in the background (state "downloading" is
// visible immediately).
func (m *Manager) StartDownload() error {
	if err := m.preconditions(false); err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	m.setState(StateDownloading, "")
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.end()
		_, _ = m.download(m.baseContext())
	}()
	return nil
}

func (m *Manager) baseContext() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseCtx
}

func (m *Manager) busyErr() error {
	if m.opts.Hooks.Busy != nil {
		if busy, why := m.opts.Hooks.Busy(); busy {
			return fmt.Errorf("busy: %s", why)
		}
	}
	return nil
}

// Install downloads (if needed), verifies, swaps the app in, relaunches and quits.
func (m *Manager) Install(ctx context.Context) error {
	if err := m.preconditions(true); err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	defer m.end()
	return m.install(ctx)
}

// StartInstall runs Install in the background after cheap synchronous checks.
func (m *Manager) StartInstall() error {
	if err := m.preconditions(true); err != nil {
		return err
	}
	if err := m.busyErr(); err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	m.setState(StateInstalling, "")
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.end()
		_ = m.install(m.baseContext())
	}()
	return nil
}

func (m *Manager) fail(err error) error {
	m.setState(StateError, err.Error())
	return err
}

func (m *Manager) install(ctx context.Context) error {
	m.mu.Lock()
	res := *m.result
	m.mu.Unlock()
	ver := res.Manifest.Version
	inst := m.opts.Installer

	archive, err := m.download(ctx)
	if err != nil {
		return err
	}
	m.setState(StateInstalling, "")
	candidate, err := inst.Prepare(archive, res.Manifest)
	if err != nil {
		_ = os.Remove(archive) // never keep an archive that failed verification
		m.mu.Lock()
		m.downloaded = ""
		m.mu.Unlock()
		return m.fail(fmt.Errorf("update verification failed: %w", err))
	}
	if err := m.busyErr(); err != nil {
		m.mu.Lock()
		m.state = m.settledStateLocked()
		m.errMsg = err.Error()
		s := m.statusLocked()
		m.mu.Unlock()
		m.notify(s)
		return err
	}
	m.record("update_started", "update to "+ver, "from "+m.opts.CurrentVersion)
	failed := func(err error) error {
		if m.opts.Hooks.AfterFailedInstall != nil {
			m.opts.Hooks.AfterFailedInstall()
		}
		m.record("update_failed", "update to "+ver+" failed", err.Error())
		return m.fail(err)
	}
	if m.opts.Hooks.BeforeInstall != nil {
		if err := m.opts.Hooks.BeforeInstall(ver); err != nil {
			return failed(fmt.Errorf("could not stop the server: %w", err))
		}
	}
	backup, err := inst.Apply(candidate, ver)
	if err != nil {
		return failed(err)
	}
	appPath := ""
	if ap, ok := inst.(interface{ AppPath() string }); ok {
		appPath = ap.AppPath()
	}
	m.mu.Lock()
	m.st.Pending = &Pending{From: m.opts.CurrentVersion, To: ver, AppPath: appPath, BackupPath: backup, StartedAt: m.now()}
	m.st.Backup = &Backup{Path: backup, From: m.opts.CurrentVersion, To: ver}
	m.st.SkipVersion, m.st.RemindUntil = "", time.Time{}
	m.save()
	m.mu.Unlock()
	m.record("update_started", "update to "+ver, "bundle replaced; relaunching")
	_ = os.RemoveAll(filepath.Join(m.updatesDir(), ver))
	if err := inst.Relaunch(); err != nil {
		return failed(fmt.Errorf("updated to %s but could not relaunch; reopen Pocketlab: %w", ver, err))
	}
	if m.opts.Hooks.Quit != nil {
		m.opts.Hooks.Quit()
	}
	return nil
}

// ---- rollback / startup ----

func (m *Manager) backupForRollback() (*Backup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b *Backup
	switch {
	case m.st.Pending != nil && m.st.Pending.BackupPath != "":
		b = &Backup{Path: m.st.Pending.BackupPath, From: m.st.Pending.From, To: m.st.Pending.To}
	case m.st.Backup != nil:
		b = m.st.Backup
	}
	if b == nil || b.Path == "" {
		return nil, errors.New("no previous version is available to restore")
	}
	if _, err := os.Stat(b.Path); err != nil {
		return nil, errors.New("the previous version's backup is missing")
	}
	return b, nil
}

// Rollback restores the previous app version and relaunches.
func (m *Manager) Rollback(ctx context.Context) error {
	if m.opts.Disabled {
		return ErrDisabled
	}
	if m.opts.Installer == nil {
		return ErrUnsupported
	}
	if ok, why := m.opts.Installer.Supported(); !ok {
		return fmt.Errorf("%w: %s", ErrUnsupported, why)
	}
	b, err := m.backupForRollback()
	if err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	defer m.end()
	return m.rollbackTo(b)
}

// StartRollback runs Rollback in the background.
func (m *Manager) StartRollback() error {
	if m.opts.Disabled {
		return ErrDisabled
	}
	if m.opts.Installer == nil {
		return ErrUnsupported
	}
	if ok, why := m.opts.Installer.Supported(); !ok {
		return fmt.Errorf("%w: %s", ErrUnsupported, why)
	}
	b, err := m.backupForRollback()
	if err != nil {
		return err
	}
	if err := m.begin(); err != nil {
		return err
	}
	m.setState(StateInstalling, "")
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.end()
		_ = m.rollbackTo(b)
	}()
	return nil
}

func (m *Manager) rollbackTo(b *Backup) error {
	if err := m.busyErr(); err != nil {
		return err
	}
	m.setState(StateInstalling, "")
	failed := func(err error) error {
		if m.opts.Hooks.AfterFailedInstall != nil {
			m.opts.Hooks.AfterFailedInstall()
		}
		m.record("update_failed", "rollback to "+b.From+" failed", err.Error())
		return m.fail(err)
	}
	if m.opts.Hooks.BeforeInstall != nil {
		if err := m.opts.Hooks.BeforeInstall(b.From); err != nil {
			return failed(fmt.Errorf("could not stop the server: %w", err))
		}
	}
	if err := m.opts.Installer.Rollback(b.Path); err != nil {
		return failed(err)
	}
	m.record("rollback", "reverted to "+b.From, "from "+m.opts.CurrentVersion)
	m.mu.Lock()
	m.st.Pending, m.st.Backup, m.rollback = nil, nil, nil
	m.result, m.downloaded = nil, ""
	// The version we just left must not be re-offered right away.
	m.st.SkipVersion = b.To
	m.save()
	m.mu.Unlock()
	if err := m.opts.Installer.Relaunch(); err != nil {
		return failed(fmt.Errorf("reverted to %s but could not relaunch; reopen Pocketlab: %w", b.From, err))
	}
	if m.opts.Hooks.Quit != nil {
		m.opts.Hooks.Quit()
	}
	return nil
}

// RollbackLaunches is how many launches without a healthy marker trigger a rollback offer.
const RollbackLaunches = 2

// Startup must be called once at launch. It settles a pending install.
func (m *Manager) Startup() {
	m.mu.Lock()
	p := m.st.Pending
	if p == nil {
		m.mu.Unlock()
		return
	}
	cur := m.opts.CurrentVersion
	var rec func()
	switch {
	case CompareVersions(cur, p.To) == 0:
		p.Launches++
		if p.Launches == 1 {
			rec = func() { m.record("update_ok", "updated to "+p.To, "from "+p.From) }
		}
		if p.Launches >= RollbackLaunches && !p.Healthy {
			m.rollback = &RollbackOffer{From: p.From, To: p.To,
				Reason: fmt.Sprintf("Pocketlab %s did not finish starting up after %d launches", p.To, p.Launches)}
		}
	case CompareVersions(cur, p.From) == 0:
		msg := "the update to " + p.To + " did not take effect"
		rec = func() { m.record("update_failed", msg, "still running "+cur) }
		m.st.Pending = nil
		m.errMsg, m.state = msg, StateError
	default:
		m.st.Pending = nil
	}
	m.save()
	s := m.statusLocked()
	m.mu.Unlock()
	if rec != nil {
		rec()
	}
	m.notify(s)
}

// MarkHealthy declares the running version healthy: the pending install is
// cleared (the backup file is kept for a manual rollback).
func (m *Manager) MarkHealthy() {
	m.mu.Lock()
	if m.st.Pending == nil {
		m.mu.Unlock()
		return
	}
	m.st.Pending.Healthy = true
	m.st.Pending = nil
	m.rollback = nil
	m.save()
	s := m.statusLocked()
	m.mu.Unlock()
	m.notify(s)
}
