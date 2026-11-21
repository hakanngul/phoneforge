package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiPrefix = "/api/v1"

// apiError is a non-2xx answer from the server.
type apiError struct {
	Status int
	Code   string
	Msg    string
}

func (e *apiError) Error() string {
	m := e.Msg
	if m == "" {
		m = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s (%s, HTTP %d)", m, e.Code, e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", m, e.Status)
}

type client struct {
	base  string
	token string
	hc    *http.Client
}

func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultServer
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, apiPrefix)
	return s
}

func newClient(server, token string) *client {
	return &client{
		base:  normalizeServer(server),
		token: token,
		hc: &http.Client{Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			DisableCompression:  true,
			MaxIdleConnsPerHost: 2,
		}},
	}
}

// unreachableError is returned when the server cannot be contacted.
func (c *client) unreachable(err error) error {
	return &exitError{ExitUnreachable, fmt.Errorf("cannot reach PhoneForge at %s: %v\nhint: start the server with `phoneforged` (see RUN.md), or set --server / PHONEFORGE_URL", c.base, rootCause(err))}
}

func rootCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (c *client) request(ctx context.Context, method, path string, body io.Reader, ctype string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPrefix+path, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.token != "" {
		req.Header.Set("X-API-Key", c.token)
	}
	if path == "/server/shutdown" {
		req.Header.Set("X-PhoneForge-Action", "shutdown")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, ctx.Err())
		}
		return nil, c.unreachable(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		ae := &apiError{Status: resp.StatusCode}
		var j struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(b, &j) == nil && (j.Error != "" || j.Code != "") {
			ae.Msg, ae.Code = j.Error, j.Code
		} else {
			ae.Msg = strings.TrimSpace(string(b))
			if len(ae.Msg) > 300 {
				ae.Msg = ae.Msg[:300]
			}
		}
		return nil, ae
	}
	return resp, nil
}

// raw performs a request and returns the body and headers.
func (c *client) raw(ctx context.Context, method, path string, in any) ([]byte, http.Header, error) {
	var body io.Reader
	ctype := ""
	switch v := in.(type) {
	case nil:
	case []byte:
		body, ctype = bytes.NewReader(v), "application/json"
	default:
		b, err := json.Marshal(in)
		if err != nil {
			return nil, nil, err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	resp, err := c.request(ctx, method, path, body, ctype)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	return b, resp.Header, nil
}

// do performs a JSON request and decodes the answer into out (may be nil).
func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	b, _, err := c.raw(ctx, method, path, in)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if rm, ok := out.(*json.RawMessage); ok {
		*rm = append((*rm)[:0], b...)
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: bad JSON from server: %w", method, path, err)
	}
	return nil
}

func devPath(id, suffix string) string {
	return "/devices/" + url.PathEscape(id) + suffix
}

// sse reads a text/event-stream response, calling fn for every frame. It
// returns when the stream ends, fn returns false, or ctx is cancelled.
func (c *client) sse(ctx context.Context, method, path string, in any, fn func(event, data string) bool) error {
	var body io.Reader
	ctype := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	resp, err := c.request(ctx, method, path, body, ctype)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	var event string
	var data []string
	flush := func() bool {
		if len(data) == 0 && event == "" {
			return true
		}
		ok := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return ok
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	flush()
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
