package webauto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Page is an attached CDP target.
type Page struct {
	Target Target
	c      *Client
}

// NewPage wraps an established client (exported for tests/integration).
func NewPage(t Target, c *Client) *Page { return &Page{Target: t, c: c} }

// Call issues a raw CDP command.
func (p *Page) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.c.Call(ctx, method, params)
}

// Closed reports whether the underlying connection died.
func (p *Page) Closed() bool { return p.c.Closed() }

// Close detaches from the page (does not close the tab).
func (p *Page) Close() error { return p.c.Close() }

type evalResult struct {
	Result struct {
		Type        string          `json:"type"`
		Value       json.RawMessage `json:"value"`
		Description string          `json:"description"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// Evaluate runs JS in the page (returnByValue, awaitPromise) and returns the
// JSON-encoded value (null for undefined).
func (p *Page) Evaluate(ctx context.Context, js string) (json.RawMessage, error) {
	raw, err := p.c.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": js, "returnByValue": true, "awaitPromise": true, "userGesture": true,
	})
	if err != nil {
		return nil, err
	}
	var er evalResult
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, err
	}
	if er.ExceptionDetails != nil {
		msg := er.ExceptionDetails.Text
		if er.ExceptionDetails.Exception != nil && er.ExceptionDetails.Exception.Description != "" {
			msg = er.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("js exception: %s", msg)
	}
	if len(er.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return er.Result.Value, nil
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

// Navigate loads url.
func (p *Page) Navigate(ctx context.Context, url string) error {
	raw, err := p.c.Call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return err
	}
	var r struct {
		ErrorText string `json:"errorText"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.ErrorText != "" {
		return fmt.Errorf("navigate: %s", r.ErrorText)
	}
	return nil
}

// Reload reloads the page.
func (p *Page) Reload(ctx context.Context) error {
	_, err := p.c.Call(ctx, "Page.reload", nil)
	return err
}

// Back navigates one entry back in history.
func (p *Page) Back(ctx context.Context) error {
	raw, err := p.c.Call(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return err
	}
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return err
	}
	if h.CurrentIndex <= 0 || h.CurrentIndex > len(h.Entries)-1 {
		return errors.New("no history to go back to")
	}
	_, err = p.c.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[h.CurrentIndex-1].ID})
	return err
}

// Click scrolls the element into view and clicks its center with real mouse
// events, falling back to el.click() when the rect is unusable.
func (p *Page) Click(ctx context.Context, selector string) error {
	js := `(()=>{const e=document.querySelector(` + jsString(selector) + `);if(!e)return null;
e.scrollIntoView({block:'center',inline:'center'});const r=e.getBoundingClientRect();
return {x:r.left+r.width/2,y:r.top+r.height/2,w:r.width,h:r.height};})()`
	v, err := p.Evaluate(ctx, js)
	if err != nil {
		return err
	}
	if string(v) == "null" {
		return fmt.Errorf("element not found: %s", selector)
	}
	var pt struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := json.Unmarshal(v, &pt); err != nil {
		return err
	}
	if pt.W > 0 && pt.H > 0 {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			_, err = p.c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
				"type": typ, "x": pt.X, "y": pt.Y, "button": "left", "clickCount": 1,
			})
			if err != nil {
				break
			}
		}
		if err == nil {
			return nil
		}
	}
	_, err = p.Evaluate(ctx, `document.querySelector(`+jsString(selector)+`).click()`)
	return err
}

// Type focuses the element and inserts text.
func (p *Page) Type(ctx context.Context, selector, text string) error {
	v, err := p.Evaluate(ctx, `(()=>{const e=document.querySelector(`+jsString(selector)+`);if(!e)return false;e.scrollIntoView({block:'center'});e.focus();return true;})()`)
	if err != nil {
		return err
	}
	if string(v) != "true" {
		return fmt.Errorf("element not found: %s", selector)
	}
	_, err = p.c.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// WaitFor polls until selector matches or timeout elapses.
func (p *Page) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	js := `!!document.querySelector(` + jsString(selector) + `)`
	for {
		v, err := p.Evaluate(ctx, js)
		if err == nil && string(v) == "true" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s", selector)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Text returns the element's innerText (whole body when selector is empty).
func (p *Page) Text(ctx context.Context, selector string) (string, error) {
	if selector == "" {
		selector = "body"
	}
	v, err := p.Evaluate(ctx, `(()=>{const e=document.querySelector(`+jsString(selector)+`);return e?e.innerText:null;})()`)
	if err != nil {
		return "", err
	}
	if string(v) == "null" {
		return "", fmt.Errorf("element not found: %s", selector)
	}
	var s string
	err = json.Unmarshal(v, &s)
	return s, err
}

// HTML returns the document's outerHTML.
func (p *Page) HTML(ctx context.Context) (string, error) {
	v, err := p.Evaluate(ctx, `document.documentElement.outerHTML`)
	if err != nil {
		return "", err
	}
	var s string
	err = json.Unmarshal(v, &s)
	return s, err
}

// Screenshot captures a PNG.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	raw, err := p.c.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(r.Data))
}
