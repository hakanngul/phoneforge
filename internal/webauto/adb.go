// Package webauto automates Chrome and WebViews on Android over the Chrome
// DevTools Protocol, reached through `adb forward` to devtools abstract sockets.
package webauto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes an adb command line (args exclude the adb binary).
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// ExecRunner returns a Runner that shells out to the adb binary at adbPath.
func ExecRunner(adbPath string) Runner {
	if adbPath == "" {
		adbPath = "adb"
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, adbPath, args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return out.Bytes(), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
}

// ParseUnixSockets extracts devtools abstract socket names (without the
// leading '@') from the contents of /proc/net/unix.
func ParseUnixSockets(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue // header or unnamed socket
		}
		name := strings.TrimSpace(f[len(f)-1])
		if !strings.HasPrefix(name, "@") {
			continue
		}
		name = name[1:]
		if !strings.Contains(name, "devtools_remote") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// Target is one debuggable page/webview/worker as reported by /json/list.
type Target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	// Socket is the devtools abstract socket the target was found on.
	Socket string `json:"socket,omitempty"`
	port   int
}

// ParseTargets decodes a /json/list body.
func ParseTargets(data []byte) ([]Target, error) {
	var ts []Target
	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("parse /json/list: %w", err)
	}
	return ts, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func httpDo(ctx context.Context, method, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s", method, url, resp.Status)
	}
	return buf.Bytes(), nil
}

func itoa(i int) string { return strconv.Itoa(i) }
