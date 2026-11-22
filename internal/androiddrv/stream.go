package androiddrv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/device"
)

var _ device.VideoStreamer = (*androidDevice)(nil)

const (
	defaultStreamBitRate = 3000000
	minStreamBitRate     = 500000
	maxStreamBitRate     = 20000000
	defaultStreamScale   = 50
	minStreamScale       = 25
	maxStreamScale       = 100
	streamQuickExit      = time.Second
	streamMaxQuickRun    = 3
)

var wmSizeRe = regexp.MustCompile(`(?m)^\s*Physical size:\s*(\d+)x(\d+)\s*$`)

// parseWMSize parses `adb shell wm size` output ("Physical size: 1080x2400").
func parseWMSize(out string) (w, h int, ok bool) {
	m := wmSizeRe.FindStringSubmatch(strings.ReplaceAll(out, "\r", ""))
	if m == nil {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(m[1])
	h, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// streamBitRate returns the screenrecord bit-rate cap in bits/s from
// PL_ANDROID_BITRATE (500000-20000000), falling back to 3 Mbps.
func streamBitRate() int {
	v, err := strconv.Atoi(strings.TrimSpace(brand.Getenv("PL_ANDROID_BITRATE")))
	if err != nil || v < minStreamBitRate || v > maxStreamBitRate {
		return defaultStreamBitRate
	}
	return v
}

// streamScale returns the capture scale in percent of physical size from
// PL_ANDROID_SCALE (25-100), falling back to 50.
func streamScale() int {
	v, err := strconv.Atoi(strings.TrimSpace(brand.Getenv("PL_ANDROID_SCALE")))
	if err != nil || v < minStreamScale || v > maxStreamScale {
		return defaultStreamScale
	}
	return v
}

// scaleSize scales both dimensions by pct percent and rounds each down to an
// even number (minimum 2).
func scaleSize(w, h, pct int) (int, int) {
	f := func(v int) int {
		v = v * pct / 100
		v -= v % 2
		if v < 2 {
			v = 2
		}
		return v
	}
	return f(w), f(h)
}

// evenHalf halves both dimensions and rounds each down to an even number
// (minimum 2).
func evenHalf(w, h int) (int, int) { return scaleSize(w, h, 50) }

// flushWriter flushes the underlying writer after every Write if it
// implements http.Flusher.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if n > 0 && fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func newFlushWriter(w io.Writer) io.Writer {
	if f, ok := w.(http.Flusher); ok {
		return flushWriter{w: w, f: f}
	}
	return w
}

// streamSize returns the "--size" value (e.g. "540x1200") or "" if unknown.
func (d *androidDevice) streamSize(ctx context.Context) string {
	sctx, cancel := context.WithTimeout(ctx, propTimeout)
	defer cancel()
	out, err := d.run(sctx, "shell", "wm", "size")
	if err != nil {
		return ""
	}
	w, h, ok := parseWMSize(string(out))
	if !ok {
		return ""
	}
	hw, hh := scaleSize(w, h, streamScale())
	return fmt.Sprintf("%dx%d", hw, hh)
}

// StreamH264 streams device video as raw Annex-B H.264 to w using
// `adb -s <serial> exec-out screenrecord --output-format=h264 --bit-rate
// <PL_ANDROID_BITRATE, default 3000000> [--size WxH] -`. screenrecord is restarted transparently when it
// reaches its time limit. It returns when ctx is done (nil error), when w
// fails, or after three consecutive runs that end in under one second.
func (d *androidDevice) StreamH264(ctx context.Context, w io.Writer) error {
	args := []string{"-s", d.serial, "exec-out", "screenrecord", "--output-format=h264", "--bit-rate", strconv.Itoa(streamBitRate())}
	if size := d.streamSize(ctx); size != "" {
		args = append(args, "--size", size)
	}
	args = append(args, "-")

	out := newFlushWriter(w)
	quick := 0
	for ctx.Err() == nil {
		start := time.Now()
		werr, err := d.streamOnce(ctx, args, out)
		if ctx.Err() != nil {
			return nil
		}
		if werr != nil {
			return werr
		}
		if time.Since(start) < streamQuickExit {
			quick++
			if quick >= streamMaxQuickRun {
				return fmt.Errorf("screenrecord exited immediately %d times in a row: %v", quick, err)
			}
		} else {
			quick = 0
		}
	}
	return nil
}

// streamOnce runs one screenrecord session. werr is a write failure on w
// (fatal); err is the process outcome.
func (d *androidDevice) streamOnce(ctx context.Context, args []string, w io.Writer) (werr, err error) {
	cmd := exec.CommandContext(ctx, d.adbPath, args...)
	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		return nil, perr
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if perr := cmd.Start(); perr != nil {
		return nil, perr
	}
	_, cerr := io.Copy(w, stdout)
	if cerr != nil {
		// Write error (reader errors from a pipe are not expected); stop adb.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if cerr != nil {
		return cerr, waitErr
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(waitErr.Error() + ": " + msg)
		}
	}
	return nil, waitErr
}
