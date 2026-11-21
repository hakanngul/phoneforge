package androiddrv

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
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

// UITree dumps the UI hierarchy with uiautomator to a file on the device,
// reads it back with `exec-out cat`, and parses it. Dumping to a file is
// more robust than /dev/tty, which fails on several OEM builds.
func (d *androidDevice) UITree(ctx context.Context) (device.Element, error) {
	ctx, cancel := withTimeout(ctx, captureTimeout)
	defer cancel()
	if _, err := d.run(ctx, "shell", "uiautomator", "dump", dumpPath); err != nil {
		return device.Element{}, err
	}
	out, err := d.run(ctx, "exec-out", "cat", dumpPath)
	if err != nil {
		return device.Element{}, err
	}
	return parseUIXML(out)
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
