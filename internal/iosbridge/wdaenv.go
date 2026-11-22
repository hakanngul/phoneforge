package iosbridge

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/procutil"
)

// Runner runs external commands (xcodebuild, security, ...). Tests replace it.
type Runner interface {
	// Output runs a short command and returns its combined stdout+stderr.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream runs a long command in dir, writing combined output to w.
	Stream(ctx context.Context, dir string, w io.Writer, name string, args ...string) error
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, name, args...).CombinedOutput()
}

func (ExecRunner) Stream(ctx context.Context, dir string, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	procutil.SetProcessGroup(cmd)
	cmd.Cancel = func() error { procutil.Terminate(cmd); return nil }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// ---- team id ---------------------------------------------------------------

var teamIDRe = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// NormalizeTeamID trims and upper-cases s and reports whether it is a valid
// 10-character Apple Team ID.
func NormalizeTeamID(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s, teamIDRe.MatchString(s)
}

// Team describes the resolved development team.
type Team struct {
	ID     string `json:"id"`
	Source string `json:"source"` // env, settings, certificate, profile, xcode
}

// TeamFinder resolves the development team. Fields are injectable for tests.
type TeamFinder struct {
	Run  Runner
	Home string // user home dir
	Env  func(string) string
	// Saved returns the team id stored in Pocketlab settings ("" if none).
	Saved func() string
}

func (f TeamFinder) env(k string) string {
	if f.Env != nil {
		return brand.Lookup(f.Env, k)
	}
	return brand.Getenv(k)
}

// Find searches: PL_TEAM_ID env, saved setting, Apple Development
// certificate, provisioning profiles, Xcode preferences. It returns
// (Team{}, false) when none yields a valid id. An invalid PL_TEAM_ID is
// reported through the error.
func (f TeamFinder) Find(ctx context.Context) (Team, error) {
	if v := f.env("PL_TEAM_ID"); v != "" {
		id, ok := NormalizeTeamID(v)
		if !ok {
			return Team{}, &Issue{Code: CodeTeamInvalid, Message: "PL_TEAM_ID is not a valid 10-character Team ID.", Hint: "Use the 10 letters/digits shown under Xcode > Settings > Accounts > Team, or unset PL_TEAM_ID."}
		}
		return Team{id, "env"}, nil
	}
	if f.Saved != nil {
		if id, ok := NormalizeTeamID(f.Saved()); ok {
			return Team{id, "settings"}, nil
		}
	}
	if id := f.fromCert(ctx); id != "" {
		return Team{id, "certificate"}, nil
	}
	if id := f.fromProfiles(ctx); id != "" {
		return Team{id, "profile"}, nil
	}
	if id := f.fromXcode(ctx); id != "" {
		return Team{id, "xcode"}, nil
	}
	return Team{}, &Issue{
		Code:    CodeTeamNotFound,
		Message: "No Apple development team was found on this Mac.",
		Hint:    "Open Xcode > Settings > Accounts, sign in with your Apple ID (a free one works) and let it create an \"Apple Development\" certificate, or enter your 10-character Team ID below.",
	}
}

// fromCert reads the OU (team id) of the newest valid "Apple Development" certificate.
func (f TeamFinder) fromCert(ctx context.Context) string {
	out, err := f.Run.Output(ctx, "security", "find-certificate", "-a", "-c", "Apple Development", "-p")
	if err != nil && len(out) == 0 {
		return ""
	}
	return teamFromCertPEM(out, time.Now())
}

func teamFromCertPEM(data []byte, now time.Time) string {
	type cand struct {
		id  string
		end time.Time
	}
	var cands []cand
	for {
		var blk *pem.Block
		blk, data = pem.Decode(data)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil || now.After(c.NotAfter) || len(c.Subject.OrganizationalUnit) == 0 {
			continue
		}
		if id, ok := NormalizeTeamID(c.Subject.OrganizationalUnit[0]); ok {
			cands = append(cands, cand{id, c.NotAfter})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].end.After(cands[j].end) })
	return cands[0].id
}

var profileTeamRe = regexp.MustCompile(`(?s)<key>TeamIdentifier</key>\s*<array>\s*<string>([A-Za-z0-9]+)</string>`)

func (f TeamFinder) fromProfiles(ctx context.Context) string {
	var dirs []string
	if f.Home != "" {
		dirs = append(dirs,
			filepath.Join(f.Home, "Library", "Developer", "Xcode", "UserData", "Provisioning Profiles"),
			filepath.Join(f.Home, "Library", "MobileDevice", "Provisioning Profiles"))
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.mobileprovision"))
		sort.Strings(files)
		for _, file := range files {
			out, err := f.Run.Output(ctx, "security", "cms", "-D", "-i", file)
			if err != nil {
				continue
			}
			if id := teamFromProfileXML(out); id != "" {
				return id
			}
		}
	}
	return ""
}

func teamFromProfileXML(b []byte) string {
	m := profileTeamRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	id, ok := NormalizeTeamID(string(m[1]))
	if !ok {
		return ""
	}
	return id
}

var xcodeTeamRe = regexp.MustCompile(`teamID\s*=\s*"?([A-Za-z0-9]{10})"?`)

func (f TeamFinder) fromXcode(ctx context.Context) string {
	out, err := f.Run.Output(ctx, "defaults", "read", "com.apple.dt.Xcode", "IDEProvisioningTeams")
	if err != nil {
		return ""
	}
	return teamFromXcodePrefs(out)
}

func teamFromXcodePrefs(b []byte) string {
	m := xcodeTeamRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	id, ok := NormalizeTeamID(string(m[1]))
	if !ok {
		return ""
	}
	return id
}

// ---- xcode -----------------------------------------------------------------

const hintInstallXcode = "Install Xcode from the Mac App Store, open it once, then run this in Terminal (it needs your password, so Pocketlab cannot do it for you):\n  sudo xcode-select -s /Applications/Xcode.app/Contents/Developer"

// CheckXcode verifies that a full Xcode is installed and selected and that
// xcodebuild is usable. It returns nil when everything is fine.
func CheckXcode(ctx context.Context, run Runner, statFn func(string) bool) *Issue {
	if statFn == nil {
		statFn = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	out, err := run.Output(ctx, "xcode-select", "-p")
	sel := strings.TrimSpace(string(out))
	if err != nil || sel == "" {
		if statFn("/Applications/Xcode.app") {
			return &Issue{Code: CodeXcodeNotSelected, Message: "Xcode is installed but not selected.", Hint: "Run this in Terminal (it needs your password, so Pocketlab cannot do it for you):\n  sudo xcode-select -s /Applications/Xcode.app/Contents/Developer"}
		}
		return &Issue{Code: CodeXcodeMissing, Message: "Xcode is not installed.", Hint: hintInstallXcode}
	}
	if strings.Contains(sel, "CommandLineTools") || !strings.Contains(sel, ".app/") {
		if statFn("/Applications/Xcode.app") {
			return &Issue{Code: CodeXcodeNotSelected, Message: "xcode-select points at the Command Line Tools, not at Xcode.", Hint: "Run this in Terminal (it needs your password, so Pocketlab cannot do it for you):\n  sudo xcode-select -s /Applications/Xcode.app/Contents/Developer"}
		}
		return &Issue{Code: CodeXcodeMissing, Message: "Only the Command Line Tools are installed; building WebDriverAgent needs the full Xcode.", Hint: hintInstallXcode}
	}
	vout, verr := run.Output(ctx, "xcodebuild", "-version")
	low := strings.ToLower(string(vout))
	if verr != nil {
		if strings.Contains(low, "license") {
			return &Issue{Code: CodeXcodeLicense, Message: "Xcode's license has not been accepted yet.", Hint: hintLicense, Lines: firstLines(string(vout), 3)}
		}
		if strings.Contains(low, "first launch") || strings.Contains(low, "runfirstlaunch") {
			return &Issue{Code: CodeXcodeLicense, Message: "Xcode has not finished its first-launch setup.", Hint: "Open Xcode once and let it install its components, or run `sudo xcodebuild -runFirstLaunch` in Terminal.", Lines: firstLines(string(vout), 3)}
		}
		return &Issue{Code: CodeXcodeMissing, Message: "xcodebuild is not usable: " + strings.TrimSpace(firstLine(string(vout))), Hint: hintInstallXcode}
	}
	return nil
}

// XcodeVersion returns the first line of `xcodebuild -version`.
func XcodeVersion(ctx context.Context, run Runner) string {
	out, err := run.Output(ctx, "xcodebuild", "-version")
	if err != nil {
		return ""
	}
	return firstLine(string(out))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstLines(s string, n int) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
		if len(out) == n {
			break
		}
	}
	return out
}
