package iosbridge

import (
	"regexp"
	"strings"
)

// Structured error codes for the WDA build / install / launch flow. The UI
// renders each with its Hint, so keep the codes stable.
const (
	CodeRequiresMac        = "WDA_REQUIRES_MACOS"
	CodeXcodeMissing       = "XCODE_MISSING"
	CodeXcodeNotSelected   = "XCODE_NOT_SELECTED"
	CodeXcodeLicense       = "XCODE_LICENSE"
	CodeTeamNotFound       = "TEAM_NOT_FOUND"
	CodeTeamInvalid        = "TEAM_INVALID"
	CodeSourceMissing      = "WDA_SOURCE_MISSING"
	CodeProvisioning       = "PROVISIONING_FAILED"
	CodeDeviceNotRegisterd = "DEVICE_NOT_REGISTERED"
	CodeAppIDLimit         = "APP_ID_LIMIT"
	CodeDeveloperModeOff   = "DEVELOPER_MODE_OFF"
	CodeDeveloperNotTrust  = "DEVELOPER_NOT_TRUSTED"
	CodeDeviceNotTrusted   = "DEVICE_NOT_TRUSTED"
	CodeDeviceLocked       = "DEVICE_LOCKED"
	CodeBuildFailed        = "BUILD_FAILED"
	CodeBuildBusy          = "BUILD_BUSY"
	CodeCanceled           = "BUILD_CANCELED"
)

// Issue is a structured, user-actionable problem. It implements error.
type Issue struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Hint    string   `json:"hint,omitempty"`
	Lines   []string `json:"lines,omitempty"` // relevant log lines
}

func (i *Issue) Error() string { return i.Message }

// Hints shared between the build log classifier and the install/launch path.
const (
	hintDeveloperMode  = "On the iPhone open Settings > Privacy & Security > Developer Mode, switch it on and restart the phone when asked."
	hintTrustDeveloper = "On the iPhone open Settings > General > VPN & Device Management, tap your Apple Development profile and choose Trust."
	hintTrustComputer  = "Unlock the iPhone and tap \"Trust\" on the \"Trust This Computer?\" prompt, then try again."
	hintProvisioning   = "Open Xcode > Settings > Accounts and make sure your Apple ID is signed in, then connect the iPhone once with a cable (unlocked, tapped Trust) so Xcode registers it with your team."
	hintAppIDLimit     = "Free Apple accounts can only create a few app IDs per week. Wait a few days, or use a paid Apple Developer account. Setting PF_BUNDLE_PREFIX to a bundle id you already used earlier also works."
	hintLicense        = "Run `sudo xcodebuild -license accept` in Terminal (it needs your password, so PhoneForge cannot do it for you), then retry."
)

type rule struct {
	re   *regexp.Regexp
	code string
	msg  string
	hint string
}

func r(p string) *regexp.Regexp { return regexp.MustCompile("(?i)" + p) }

// Order matters: the first matching rule wins.
var logRules = []rule{
	{r(`developer mode (is )?(disabled|not enabled|is off|must be enabled)|enable developer mode|developer mode is turned off`),
		CodeDeveloperModeOff, "Developer Mode is off on the iPhone.", hintDeveloperMode},
	{r(`agreed to the xcode license|xcode license|license agreement|first launch tasks|runFirstLaunch`),
		CodeXcodeLicense, "Xcode has not finished its first-launch setup or license acceptance.", hintLicense + " If it asks for first-launch components, run `xcodebuild -runFirstLaunch`."},
	{r(`maximum number of (apps|app ids|registered)|cannot register more than|reached the maximum|too many (app ids|apps)|app id[s]? (limit|you can register)|failed registering bundle identifier`),
		CodeAppIDLimit, "Your Apple account hit its App ID limit.", hintAppIDLimit},
	{r(`no devices|has no devices|no registered devices|not registered|device is not registered|isn.t registered|unable to find a device|doesn.t include the currently selected device|does not include the selected device`),
		CodeDeviceNotRegisterd, "The iPhone is not registered with your development team.", hintProvisioning},
	{r(`untrusted developer|has not been explicitly trusted|profile has not been explicitly trusted|invalid code signature, inadequate entitlements`),
		CodeDeveloperNotTrust, "The iPhone does not trust your developer profile yet.", hintTrustDeveloper},
	{r(`requires a development team|no account for team|no profiles for|provisioning profile|no signing certificate|code signing is required|failed to create provisioning profile|automatic signing|signing for .* requires|communication with apple failed|team .* is not enabled`),
		CodeProvisioning, "Xcode could not create the signing profile for WebDriverAgent.", hintProvisioning},
	{r(`not paired|pairing|passwordprotected|password protected|user denied|trust this computer|invalidhostid`),
		CodeDeviceNotTrusted, "The iPhone is not paired or has not trusted this Mac.", hintTrustComputer},
	{r(`device is locked|phone is locked|screen is locked|device locked`),
		CodeDeviceLocked, "The iPhone is locked.", "Unlock the iPhone and keep the screen on, then try again."},
}

// ClassifyText maps free-form error output (xcodebuild log, install or
// launch error text) to a structured Issue. It returns nil when nothing
// recognisable is found.
func ClassifyText(text string) *Issue {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for _, rl := range logRules {
		var hits []string
		for _, ln := range lines {
			if rl.re.MatchString(ln) {
				hits = append(hits, strings.TrimSpace(ln))
			}
		}
		if len(hits) > 0 {
			return &Issue{Code: rl.code, Message: rl.msg, Hint: rl.hint, Lines: dedupTail(hits, 8)}
		}
	}
	return nil
}

// ClassifyBuildLog classifies a failed xcodebuild log. When no rule matches it
// returns a generic BUILD_FAILED with the last "error:" lines.
func ClassifyBuildLog(lines []string) *Issue {
	// Only look from the first "error:" on: earlier lines are normal build
	// chatter (e.g. "Provisioning Profile: ...") that would match the rules.
	slice := lines
	for i, ln := range lines {
		if strings.Contains(ln, "error:") {
			slice = lines[i:]
			break
		}
	}
	if is := ClassifyText(strings.Join(slice, "\n")); is != nil {
		return is
	}
	var errs []string
	for _, ln := range lines {
		if strings.Contains(ln, "error:") || strings.Contains(ln, "** BUILD FAILED **") {
			errs = append(errs, strings.TrimSpace(ln))
		}
	}
	return &Issue{
		Code:    CodeBuildFailed,
		Message: "The WebDriverAgent build failed.",
		Hint:    "See the log lines below and the full log file in ~/.phoneforge/wda. Make sure Xcode is fully installed and signed in under Xcode > Settings > Accounts.",
		Lines:   dedupTail(errs, 10),
	}
}

// ClassifyInstallError turns an install or launch error into a structured
// Issue when it recognises a phone-side cause.
func ClassifyInstallError(err error) *Issue {
	if err == nil {
		return nil
	}
	s := err.Error()
	if is := ClassifyText(s); is != nil {
		return is
	}
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "device not found"):
		return &Issue{Code: CodeDeviceNotTrusted, Message: "The iPhone is not reachable (not connected, locked, or not trusted).", Hint: hintTrustComputer, Lines: []string{s}}
	case strings.Contains(low, "install failed") || strings.Contains(low, "cannot open install service"):
		return &Issue{Code: CodeDeveloperNotTrust, Message: "The iPhone refused the helper app.", Hint: "Make sure the phone is unlocked and Developer Mode is on (Settings > Privacy & Security > Developer Mode). " + hintTrustDeveloper, Lines: []string{s}}
	}
	return nil
}

func dedupTail(in []string, n int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
