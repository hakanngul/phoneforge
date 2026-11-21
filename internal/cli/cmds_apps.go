package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"phoneforge/internal/device"
)

func cmdApps(c *call) error {
	fs := newFlagSet(c.name)
	filter := fs.String("filter", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	var apps []device.AppInfo
	if err := c.client().do(ctx, "GET", devPath(id, "/apps"), nil, &apps); err != nil {
		return err
	}
	if f := strings.ToLower(*filter); f != "" {
		kept := apps[:0]
		for _, a := range apps {
			if strings.Contains(strings.ToLower(a.BundleID), f) || strings.Contains(strings.ToLower(a.Name), f) {
				kept = append(kept, a)
			}
		}
		apps = kept
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].BundleID) < strings.ToLower(apps[j].BundleID) })
	if c.g.json {
		if apps == nil {
			apps = []device.AppInfo{}
		}
		return c.printJSON(apps)
	}
	formatApps(c.out, apps)
	return nil
}

// bundleCmd implements launch / stop.
func bundleCmd(c *call, path, verb string) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: phoneforge %s <bundle-id|package>", c.name)
	}
	return c.post(path, map[string]string{"bundleId": pos[0]}, 60*time.Second, verb+" "+pos[0])
}

func cmdLaunch(c *call) error { return bundleCmd(c, "/launch-app", "launched") }
func cmdStop(c *call) error   { return bundleCmd(c, "/stop-app", "stopped") }

func cmdUninstall(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: phoneforge uninstall <bundle-id|package>")
	}
	ctx, cancel := c.ctxFor(2 * time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	if err := c.client().do(ctx, "DELETE", devPath(id, "/apps/"+url.PathEscape(pos[0])), nil, nil); err != nil {
		return err
	}
	return c.actionResult("uninstalled " + pos[0])
}

func cmdInstall(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: phoneforge install <file.apk|.ipa|.app>")
	}
	path, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	ctx, cancel := c.ctxFor(10 * time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	cl := c.client()
	if st.IsDir() {
		// An .app bundle cannot be uploaded; the server installs it from this path
		// (so the server must run on the same machine).
		if err := cl.do(ctx, "POST", devPath(id, "/install-app"), map[string]string{"path": path}, nil); err != nil {
			return err
		}
		return c.actionResult("installed " + filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	resp, err := cl.request(ctx, "POST", devPath(id, "/install-app"), pr, mw.FormDataContentType())
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return c.actionResult("installed " + filepath.Base(path))
}

func cmdOpen(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: phoneforge open <url>")
	}
	return c.post("/open-url", map[string]string{"url": pos[0]}, 60*time.Second, "opened "+pos[0])
}

// ocrResult mirrors the server's OCR row.
type ocrResult struct {
	Text       string      `json:"text"`
	Bounds     device.Rect `json:"bounds"`
	Confidence float64     `json:"confidence"`
}

func cmdOCR(c *call) error {
	fs := newFlagSet(c.name)
	find := fs.String("find", "", "")
	exact := fs.Bool("exact", false, "")
	regex := fs.Bool("regex", false, "")
	words := fs.Bool("words", false, "")
	cs := fs.Bool("case-sensitive", false, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(2 * time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	base := "/ocr/devices/" + url.PathEscape(id)
	var raw json.RawMessage
	key := "results"
	if *find != "" {
		key = "matches"
		err = c.client().do(ctx, "POST", base+"/find", map[string]any{"text": *find, "exact": *exact, "regex": *regex, "caseSensitive": *cs, "words": *words}, &raw)
	} else {
		err = c.client().do(ctx, "POST", base, map[string]any{"words": *words}, &raw)
	}
	if err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	var rows []ocrResult
	_ = json.Unmarshal(env[key], &rows)
	if len(rows) == 0 {
		if *find != "" {
			fmt.Fprintf(c.out, "no on-screen text matched %q\n", *find)
		} else {
			fmt.Fprintln(c.out, "no text recognised")
		}
		if *find != "" {
			return &exitError{ExitError, fmt.Errorf("no match")}
		}
		return nil
	}
	fmt.Fprintln(c.out, "# bounds are in screenshot pixels: x,y = box origin")
	for _, r := range rows {
		fmt.Fprintf(c.out, "%q @(%d,%d) %dx%d conf=%.2f\n", r.Text, r.Bounds.X, r.Bounds.Y, r.Bounds.W, r.Bounds.H, r.Confidence)
	}
	return nil
}

func cmdWait(c *call) error {
	fs := newFlagSet(c.name)
	text := fs.String("text", "", "")
	rid := fs.String("id", "", "")
	ocr := fs.Bool("ocr", false, "")
	gone := fs.Bool("gone", false, "")
	interval := fs.Duration("interval", 250*time.Millisecond, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	if *text == "" && *rid == "" {
		return usagef("usage: phoneforge wait --text TEXT | --id RESOURCE_ID [--gone] [--ocr] [--timeout 10s]")
	}
	if *ocr && *text == "" {
		return usagef("wait: --ocr needs --text")
	}
	ctx, cancel := c.ctxFor(10 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	start := time.Now()
	present := func() (bool, error) {
		el, _, err := fetchTree(c, ctx, id)
		if err != nil {
			return false, err
		}
		if len(findElements(el, elemQuery{Text: *text, ID: *rid})) > 0 {
			return true, nil
		}
		if *ocr {
			var res struct {
				Matches []ocrResult `json:"matches"`
			}
			if err := c.client().do(ctx, "POST", "/ocr/devices/"+url.PathEscape(id)+"/find", map[string]any{"text": *text}, &res); err != nil {
				return false, err
			}
			return len(res.Matches) > 0, nil
		}
		return false, nil
	}
	what := fmt.Sprintf("text %q", *text)
	if *rid != "" {
		what = fmt.Sprintf("id %q", *rid)
	}
	for {
		ok, err := present()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		if ok != *gone {
			el := time.Since(start).Round(time.Millisecond)
			verb := "found"
			if *gone {
				verb = "gone:"
			}
			if c.g.json {
				return c.printJSON(map[string]any{"ok": true, "elapsedMs": el.Milliseconds()})
			}
			fmt.Fprintf(c.out, "%s %s after %s\n", verb, what, el)
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(*interval):
			continue
		}
		break
	}
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	state := "did not appear"
	if *gone {
		state = "did not disappear"
	}
	return fmt.Errorf("wait: %s %s within %s", what, state, time.Since(start).Round(time.Millisecond))
}

var _ = bytes.NewReader
