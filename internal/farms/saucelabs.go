package farms

import (
	"context"
	"fmt"
)

// SauceLabsBaseURL is the Sauce Labs REST root (US West); overridable for
// tests and for other data centers.
var SauceLabsBaseURL = "https://api.us-west-1.saucelabs.com"

// listSauceLabs lists real devices via GET /v1/rdc/devices (Basic auth:
// username + access key).
func (m *Manager) listSauceLabs(ctx context.Context) ([]CloudDevice, error) {
	user, err := m.reveal(SauceLabs, "user")
	if err != nil {
		return nil, err
	}
	key, err := m.reveal(SauceLabs, "key")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID         string `json:"id"`
		Descriptor string `json:"descriptor"`
		Name       string `json:"name"`
		OS         string `json:"os"`
		OSVersion  string `json:"osVersion"`
	}
	if err := getJSONBasic(ctx, SauceLabsBaseURL+"/v1/rdc/devices", user, key, &raw); err != nil {
		return nil, fmt.Errorf("saucelabs: list devices: %w", err)
	}
	out := make([]CloudDevice, 0, len(raw))
	for _, d := range raw {
		id := d.Descriptor
		if id == "" {
			id = d.ID
		}
		out = append(out, CloudDevice{
			Provider:  SauceLabs,
			Name:      d.Name,
			OS:        d.OS,
			OSVersion: d.OSVersion,
			DeviceID:  id,
			Real:      true,
		})
	}
	return out, nil
}
