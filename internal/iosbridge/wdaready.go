package iosbridge

import (
	"fmt"

	"github.com/danielpaulus/go-ios/ios"
)

// Readiness reports what could be learned about an iPhone before installing
// the WDA helper. Unknown fields stay nil.
type Readiness struct {
	Reachable     bool   `json:"reachable"`               // usbmuxd lists the device
	Paired        *bool  `json:"paired,omitempty"`        // lockdown session works (phone trusts this Mac)
	DeveloperMode *bool  `json:"developerMode,omitempty"` // iOS 16+: Developer Mode on
	Issue         *Issue `json:"issue,omitempty"`
}

const amfiDomain = "com.apple.security.mac.amfi"

// CheckReadiness probes the phone over lockdown: reachability, pairing/trust
// and (iOS 16+) Developer Mode. It never changes anything on the phone.
func CheckReadiness(udid string) Readiness {
	var rd Readiness
	dev, err := ios.GetDevice(udid)
	if err != nil {
		rd.Issue = &Issue{Code: CodeDeviceNotTrusted, Message: "The iPhone is not reachable over USB.", Hint: "Connect it with a cable, unlock it and tap \"Trust\" if asked. " + hintTrustComputer, Lines: []string{err.Error()}}
		return rd
	}
	rd.Reachable = true
	conn, err := ios.ConnectLockdownWithSession(dev)
	if err != nil {
		f := false
		rd.Paired = &f
		rd.Issue = &Issue{Code: CodeDeviceNotTrusted, Message: "This Mac is not trusted by the iPhone (or the phone is locked).", Hint: hintTrustComputer, Lines: []string{err.Error()}}
		return rd
	}
	defer conn.Close()
	t := true
	rd.Paired = &t
	v, err := conn.GetValueForDomain("DeveloperModeStatus", amfiDomain)
	if err == nil {
		switch x := v.(type) {
		case bool:
			rd.DeveloperMode = &x
			if !x {
				rd.Issue = &Issue{Code: CodeDeveloperModeOff, Message: "Developer Mode is off on the iPhone.", Hint: hintDeveloperMode}
			}
		default:
			_ = fmt.Sprint(x) // key missing (iOS < 16) or unexpected: leave unknown
		}
	}
	return rd
}
