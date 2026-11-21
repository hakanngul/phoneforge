package androiddrv

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// StreamLogs copies `adb logcat -v time` to w line by line until ctx is done.
// If w implements http.Flusher it is flushed after every line.
func (d *androidDevice) StreamLogs(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, d.adbPath, "-s", d.serial, "logcat", "-v", "time")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	fl, _ := w.(http.Flusher)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var werr error
	for sc.Scan() {
		if _, werr = io.WriteString(w, sc.Text()+"\n"); werr != nil {
			break
		}
		if fl != nil {
			fl.Flush()
		}
	}
	if werr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil
	case werr != nil:
		return werr
	case waitErr != nil:
		return waitErr
	}
	return sc.Err()
}
