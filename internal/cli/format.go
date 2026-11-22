package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"pocketlab/internal/device"
)

// table prints rows with aligned columns.
func table(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func formatDevices(w io.Writer, devs []device.Info) {
	if len(devs) == 0 {
		fmt.Fprintln(w, "no devices connected")
		return
	}
	rows := make([][]string, 0, len(devs))
	for _, d := range devs {
		rows = append(rows, []string{d.ID, dash(d.Name), platformLabel(d), dash(d.OSVersion), dash(d.State)})
	}
	table(w, []string{"ID", "NAME", "PLATFORM", "OS", "STATE"}, rows)
}

func formatDeviceInfo(w io.Writer, d device.Info) {
	kv := [][2]string{
		{"id", d.ID}, {"name", d.Name}, {"platform", platformLabel(d)}, {"model", d.Model},
		{"os", d.OSVersion}, {"serial", d.Serial}, {"state", d.State},
	}
	for _, p := range kv {
		fmt.Fprintf(w, "%-9s %s\n", p[0]+":", dash(p[1]))
	}
}

func formatApps(w io.Writer, apps []device.AppInfo) {
	if len(apps) == 0 {
		fmt.Fprintln(w, "no apps")
		return
	}
	rows := make([][]string, 0, len(apps))
	for _, a := range apps {
		v := a.Version
		if a.Build != "" && a.Build != a.Version {
			v += " (" + a.Build + ")"
		}
		rows = append(rows, []string{a.BundleID, dash(a.Name), dash(v)})
	}
	table(w, []string{"BUNDLE ID", "NAME", "VERSION"}, rows)
}

// truncate clips s to n runes.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
