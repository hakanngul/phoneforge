package cli

import (
	"context"
	"fmt"
	"strings"

	"pocketlab/internal/device"
)

// resolveDevice picks the target device. With an empty want it returns the only
// connected device. Otherwise want may be an id, serial, name or model; an
// exact match wins, then a unique prefix, then a unique substring (all case
// insensitive), so "iphone" or "sm-g" work when they identify one device.
func resolveDevice(ctx context.Context, c *client, want string) (device.Info, error) {
	var devs []device.Info
	if err := c.do(ctx, "GET", "/devices", nil, &devs); err != nil {
		return device.Info{}, err
	}
	return pickDevice(devs, want)
}

func pickDevice(devs []device.Info, want string) (device.Info, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		var conn []device.Info
		for _, d := range devs {
			if d.State == "" || d.State == "connected" {
				conn = append(conn, d)
			}
		}
		switch len(conn) {
		case 0:
			return device.Info{}, deviceErrf("no connected devices")
		case 1:
			return conn[0], nil
		}
		return device.Info{}, deviceErrf("%d devices connected; choose one with --device (or POCKETLAB_DEVICE):\n%s", len(conn), listCandidates(conn))
	}
	lw := strings.ToLower(want)
	fields := func(d device.Info) []string {
		return []string{strings.ToLower(d.ID), strings.ToLower(d.Serial), strings.ToLower(d.Name), strings.ToLower(d.Model)}
	}
	tiers := []func(f string) bool{
		func(f string) bool { return f != "" && f == lw },
		func(f string) bool { return f != "" && strings.HasPrefix(f, lw) },
		func(f string) bool { return f != "" && strings.Contains(f, lw) },
	}
	for _, match := range tiers {
		var hit []device.Info
		for _, d := range devs {
			for _, f := range fields(d) {
				if match(f) {
					hit = append(hit, d)
					break
				}
			}
		}
		switch len(hit) {
		case 0:
			continue
		case 1:
			return hit[0], nil
		}
		return device.Info{}, deviceErrf("%q matches %d devices; be more specific:\n%s", want, len(hit), listCandidates(hit))
	}
	if len(devs) == 0 {
		return device.Info{}, deviceErrf("device %q not found (no devices connected)", want)
	}
	return device.Info{}, deviceErrf("device %q not found; connected:\n%s", want, listCandidates(devs))
}

func listCandidates(devs []device.Info) string {
	var sb strings.Builder
	for _, d := range devs {
		fmt.Fprintf(&sb, "  %s  (%s, %s)\n", d.ID, d.Name, platformLabel(d))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func platformLabel(d device.Info) string {
	if d.Simulator {
		return string(d.Platform) + " simulator"
	}
	return string(d.Platform)
}
