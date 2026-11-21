package androiddrv

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"sync"
	"time"

	"phoneforge/internal/device"
)

const (
	captureTimeout = 30 * time.Second
	dumpPath       = "/sdcard/pf_dump.xml"
)

// Screenshot captures the screen as PNG via `exec-out screencap -p`.
func (d *androidDevice) Screenshot(ctx context.Context) (device.Screenshot, error) {
	ctx, cancel := withTimeout(ctx, captureTimeout)
	defer cancel()
	out, err := d.run(ctx, "exec-out", "screencap", "-p")
	if err != nil {
		return device.Screenshot{}, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		return device.Screenshot{}, fmt.Errorf("androiddrv: decode screenshot: %w", err)
	}
	return device.Screenshot{PNG: out, Width: cfg.Width, Height: cfg.Height}, nil
}

// noStdoutDump remembers serials where `uiautomator dump /dev/stdout` did not
// produce a hierarchy, so those devices go straight to the file path.
var noStdoutDump sync.Map

// UITree reads the hierarchy with `adb exec-out uiautomator dump --compressed
// /dev/stdout`: one adb round trip, no temp file and no separate pull. Where
// that yields nothing parseable (some OEM builds reject /dev/stdout) it falls
// back to dumping to a file on the device and reading it back with
// `exec-out cat`. The dump itself costs ~2 s on slow phones; the UiAutomator2
// bridge (package uia2) is the fast path.
func (d *androidDevice) UITree(ctx context.Context) (device.Element, error) {
	ctx, cancel := withTimeout(ctx, captureTimeout)
	defer cancel()
	tryStdout := false
	if _, skip := noStdoutDump.Load(d.serial); !skip {
		out, err := d.run(ctx, "exec-out", "uiautomator", "dump", "--compressed", "/dev/stdout")
		if err == nil && bytes.Contains(out, []byte("<hierarchy")) {
			if el, perr := parseUIXML(out); perr == nil {
				return el, nil
			}
		}
		if ctx.Err() != nil {
			return device.Element{}, ctx.Err()
		}
		tryStdout = true
	}
	if _, err := d.run(ctx, "shell", "uiautomator", "dump", dumpPath); err != nil {
		return device.Element{}, err
	}
	out, err := d.run(ctx, "exec-out", "cat", dumpPath)
	if err != nil {
		return device.Element{}, err
	}
	el, err := parseUIXML(out)
	if err == nil && tryStdout {
		// The file path works where stdout did not: remember that.
		noStdoutDump.Store(d.serial, true)
	}
	return el, err
}

type xmlNode struct {
	Class      string    `xml:"class,attr"`
	Text       string    `xml:"text,attr"`
	ContentDsc string    `xml:"content-desc,attr"`
	ResourceID string    `xml:"resource-id,attr"`
	Bounds     string    `xml:"bounds,attr"`
	Clickable  string    `xml:"clickable,attr"`
	Enabled    string    `xml:"enabled,attr"`
	Focused    string    `xml:"focused,attr"`
	Children   []xmlNode `xml:"node"`
}

type xmlHierarchy struct {
	Nodes []xmlNode `xml:"node"`
}

func (n xmlNode) toElement() device.Element {
	r, _ := parseBounds(n.Bounds)
	e := device.Element{
		Role:       n.Class,
		Text:       n.Text,
		Label:      n.ContentDsc,
		ResourceID: n.ResourceID,
		Bounds:     r,
		Clickable:  n.Clickable == "true",
		Enabled:    n.Enabled == "true",
		Focused:    n.Focused == "true",
	}
	for _, c := range n.Children {
		e.Children = append(e.Children, c.toElement())
	}
	return e
}

// parseUIXML parses uiautomator dump XML into a synthetic "hierarchy" root.
// Any noise before <?xml or after </hierarchy> (e.g. the "UI hierchary
// dumped to" message) is stripped.
func parseUIXML(data []byte) (device.Element, error) {
	if i := bytes.Index(data, []byte("<?xml")); i >= 0 {
		data = data[i:]
	} else if i := bytes.Index(data, []byte("<hierarchy")); i >= 0 {
		data = data[i:]
	}
	const endTag = "</hierarchy>"
	if i := bytes.LastIndex(data, []byte(endTag)); i >= 0 {
		data = data[:i+len(endTag)]
	}
	var h xmlHierarchy
	if err := xml.Unmarshal(data, &h); err != nil {
		return device.Element{}, fmt.Errorf("androiddrv: parse ui xml: %w", err)
	}
	if len(data) == 0 {
		return device.Element{}, errors.New("androiddrv: empty ui dump")
	}
	root := device.Element{Role: "hierarchy", Enabled: true}
	for _, n := range h.Nodes {
		root.Children = append(root.Children, n.toElement())
	}
	return root, nil
}
