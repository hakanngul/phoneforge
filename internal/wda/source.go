package wda

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pocketlab/internal/device"
)

// Source fetches the UI hierarchy (XML) and parses it into a device.Element
// tree in logical points. Falls back to JSON parsing when WDA returns an
// object instead of an XML string.
func (c *Client) Source(ctx context.Context) (device.Element, error) {
	q := url.Values{"format": {"xml"}}
	if len(c.ExcludedAttributes) > 0 {
		q.Set("excluded_attributes", strings.Join(c.ExcludedAttributes, ","))
	}
	v, err := c.do(ctx, http.MethodGet, "/session/{sid}/source?"+q.Encode(), nil)
	if err != nil {
		return device.Element{}, err
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return parseSourceXML([]byte(s))
	}
	return parseSourceJSON(v)
}

// clickableTypes are element types that are interactive by nature.
var clickableTypes = map[string]bool{
	"Button": true, "Cell": true, "Link": true, "Switch": true, "TextField": true,
	"SecureTextField": true, "TextView": true, "SearchField": true, "Slider": true,
	"Stepper": true, "Toggle": true, "Key": true, "Tab": true, "TabBarButton": true,
	"MenuItem": true, "Picker": true, "PickerWheel": true, "SegmentedControl": true,
	"Checkbox": true, "RadioButton": true, "Icon": true, "PageIndicator": true,
}

func shortType(t string) string { return strings.TrimPrefix(t, "XCUIElementType") }

func isClickable(typ string, accessible bool) bool {
	st := shortType(typ)
	if clickableTypes[st] {
		return true
	}
	return accessible && st != "Application" && st != "Window" && st != "Other" && st != "StaticText" && st != "Image"
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func elementFromXML(t xml.StartElement) device.Element {
	attrs := map[string]string{}
	for _, a := range t.Attr {
		attrs[a.Name.Local] = a.Value
	}
	typ := attrs["type"]
	if typ == "" {
		typ = t.Name.Local
	}
	e := device.Element{
		Role:       typ,
		Label:      attrs["label"],
		ResourceID: attrs["name"],
		Text:       attrs["value"],
		Enabled:    attrs["enabled"] != "false",
		Focused:    attrs["hasFocus"] == "true" || attrs["focused"] == "true",
	}
	if e.Text == "" {
		e.Text = e.Label
	}
	e.Bounds = device.Rect{
		X: int(parseFloat(attrs["x"]) + 0.5), Y: int(parseFloat(attrs["y"]) + 0.5),
		W: int(parseFloat(attrs["width"]) + 0.5), H: int(parseFloat(attrs["height"]) + 0.5),
	}
	e.Clickable = isClickable(typ, attrs["accessible"] == "true")
	return e
}

func parseSourceXML(data []byte) (device.Element, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var stack []*device.Element
	var root *device.Element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return device.Element{}, fmt.Errorf("wda: parse source: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := elementFromXML(t)
			stack = append(stack, &el)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			el := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				root = el
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, *el)
			}
		}
	}
	if root == nil {
		return device.Element{}, errors.New("wda: empty hierarchy")
	}
	// WDA wraps the tree in <AppiumAUT>; unwrap a lone application child.
	if root.Role == "AppiumAUT" {
		if len(root.Children) == 1 {
			return root.Children[0], nil
		}
		root.Enabled = true
	}
	return *root, nil
}

type jsonNode struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Label   string `json:"label"`
	Value   any    `json:"value"`
	Enabled *bool  `json:"isEnabled"`
	Focused bool   `json:"hasFocus"`
	Rect    *struct {
		X, Y, Width, Height float64
	} `json:"rect"`
	Children []jsonNode `json:"children"`
}

func (n jsonNode) element() device.Element {
	typ := n.Type
	if typ != "" && !strings.HasPrefix(typ, "XCUIElementType") {
		typ = "XCUIElementType" + typ
	}
	e := device.Element{Role: typ, Label: n.Label, ResourceID: n.Name, Enabled: n.Enabled == nil || *n.Enabled, Focused: n.Focused}
	if s, ok := n.Value.(string); ok {
		e.Text = s
	} else if n.Value != nil {
		e.Text = fmt.Sprint(n.Value)
	}
	if e.Text == "" {
		e.Text = n.Label
	}
	if n.Rect != nil {
		e.Bounds = device.Rect{X: int(n.Rect.X + 0.5), Y: int(n.Rect.Y + 0.5), W: int(n.Rect.Width + 0.5), H: int(n.Rect.Height + 0.5)}
	}
	e.Clickable = isClickable(typ, e.Label != "" && shortType(typ) != "Other")
	for _, ch := range n.Children {
		e.Children = append(e.Children, ch.element())
	}
	return e
}

func parseSourceJSON(data []byte) (device.Element, error) {
	var n jsonNode
	if err := json.Unmarshal(data, &n); err != nil {
		return device.Element{}, fmt.Errorf("wda: parse json source: %w", err)
	}
	if n.Type == "" && len(n.Children) == 0 {
		return device.Element{}, errors.New("wda: empty hierarchy")
	}
	return n.element(), nil
}
