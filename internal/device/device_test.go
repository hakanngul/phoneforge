package device

import "testing"

func TestIsAppSwitch(t *testing.T) {
	for _, k := range []Key{"app_switch", "recents", "recent_apps", "app_switcher", "appswitch", "App-Switch", "RECENTS"} {
		if !IsAppSwitch(k) {
			t.Errorf("%q should be app switch", k)
		}
	}
	for _, k := range []Key{"home", "back", "", "switch", "bogus"} {
		if IsAppSwitch(k) {
			t.Errorf("%q should not be app switch", k)
		}
	}
}
