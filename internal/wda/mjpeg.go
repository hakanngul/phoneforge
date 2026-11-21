package wda

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// MJPEGFrames connects to WDA's MJPEG server (default http://host:9100) and
// calls fn with each JPEG frame until ctx is done, the stream ends, or fn
// returns an error. The frame slice is only valid during the call.
//
// WDA's boundary is non-standard (the Content-Type boundary parameter omits
// the leading "--" that the actual delimiter has), so this uses a small
// tolerant parser instead of mime/multipart.
func MJPEGFrames(ctx context.Context, mjpegURL string, fn func(jpeg []byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mjpegURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: long-lived stream
	if err != nil {
		return fmt.Errorf("wda: mjpeg connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wda: mjpeg HTTP %d", resp.StatusCode)
	}
	br := &partReader{Reader: bufio.NewReaderSize(resp.Body, 256<<10)}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		frame, err := readMJPEGPart(br)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

// partReader remembers whether the previous part's scan consumed the next
// boundary line.
type partReader struct {
	*bufio.Reader
	boundaryConsumed bool
}

func isBoundary(line string) bool { return strings.HasPrefix(line, "--") }

// readMJPEGPart reads one multipart part and returns its body.
func readMJPEGPart(br *partReader) ([]byte, error) {
	// Find the boundary line (unless the previous part already consumed it).
	for !br.boundaryConsumed {
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return nil, err
		}
		t := strings.TrimSpace(line)
		if isBoundary(t) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	br.boundaryConsumed = false
	// Headers.
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return nil, err
		}
		t := strings.TrimSpace(line)
		if t == "" {
			break
		}
		if k, v, ok := strings.Cut(t, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "content-length") {
			if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil {
				length = n
			}
		}
	}
	if length >= 0 {
		if length > 64<<20 {
			return nil, fmt.Errorf("wda: mjpeg frame too large: %d", length)
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	// No Content-Length: read until the next boundary line.
	var body bytes.Buffer
	for {
		line, err := br.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull && len(line) == 0 {
			if body.Len() > 0 && err == io.EOF {
				return bytes.TrimRight(body.Bytes(), "\r\n"), nil
			}
			return nil, err
		}
		if err != bufio.ErrBufferFull && isBoundary(strings.TrimSpace(string(line))) {
			br.boundaryConsumed = true
			return bytes.TrimSuffix(bytes.TrimSuffix(body.Bytes(), []byte("\n")), []byte("\r")), nil
		}
		body.Write(line)
	}
}
