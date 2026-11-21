package farms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BrowserStackBaseURL is the BrowserStack API root; overridable for tests.
var BrowserStackBaseURL = "https://api.browserstack.com"

// httpTimeout bounds every provider REST call.
const httpTimeout = 15 * time.Second

// getJSONBasic performs a GET with HTTP Basic auth and decodes JSON into v.
func getJSONBasic(ctx context.Context, url, user, pass string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("provider rejected credentials (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

// listBrowserStack lists App Automate real devices via
// GET /app-automate/devices.json (Basic auth: username + access key).
func (m *Manager) listBrowserStack(ctx context.Context) ([]CloudDevice, error) {
	user, err := m.reveal(BrowserStack, "user")
	if err != nil {
		return nil, err
	}
	key, err := m.reveal(BrowserStack, "key")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		OS         string `json:"os"`
		OSVersion  string `json:"os_version"`
		Device     string `json:"device"`
		RealMobile *bool  `json:"realMobile"`
	}
	if err := getJSONBasic(ctx, BrowserStackBaseURL+"/app-automate/devices.json", user, key, &raw); err != nil {
		return nil, fmt.Errorf("browserstack: list devices: %w", err)
	}
	out := make([]CloudDevice, 0, len(raw))
	for _, d := range raw {
		out = append(out, CloudDevice{
			Provider:  BrowserStack,
			Name:      d.Device,
			OS:        d.OS,
			OSVersion: d.OSVersion,
			DeviceID:  d.Device + "-" + d.OSVersion,
			Real:      d.RealMobile == nil || *d.RealMobile,
		})
	}
	return out, nil
}
