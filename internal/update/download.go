package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Download fetches asset into dir and returns the verified file's path.
// It writes <dir>/<name>.part, resumes with a Range request when a partial file
// exists and the server answers 206 (a 200 restarts), never reads more than
// asset.Size bytes, verifies the SHA-256 and renames to the final name. A
// truncated transfer keeps the .part file for a later resume and returns
// ErrSizeMismatch; a checksum mismatch deletes the file and returns ErrChecksum.
func Download(ctx context.Context, client *http.Client, asset Asset, dir string, progress func(done, total int64)) (string, error) {
	if asset.Size <= 0 || asset.Name == "" || strings.ContainsAny(asset.Name, `/\`) || asset.Name == ".." {
		return "", fmt.Errorf("%w: unusable asset", ErrBadManifest)
	}
	if client == nil {
		client = NewHTTPClient(0)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(dir, asset.Name)
	part := final + ".part"
	report := func(d int64) {
		if progress != nil {
			progress(d, asset.Size)
		}
	}

	// A previous complete download may already be here.
	if st, err := os.Stat(final); err == nil {
		if st.Size() == asset.Size {
			if sum, err := fileSHA256(final); err == nil && strings.EqualFold(sum, asset.SHA256) {
				report(asset.Size)
				return final, nil
			}
		}
		_ = os.Remove(final)
	}

	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		if have > asset.Size {
			_ = os.Remove(part)
			have = 0
		}
	}

	if have < asset.Size {
		n, err := fetchInto(ctx, client, asset, part, have, report)
		if err != nil {
			return "", err
		}
		have = n
	}
	if have != asset.Size {
		return "", fmt.Errorf("%w: got %d of %d bytes", ErrSizeMismatch, have, asset.Size)
	}
	sum, err := fileSHA256(part)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, asset.SHA256) {
		_ = os.Remove(part)
		return "", ErrChecksum
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return final, nil
}

// fetchInto downloads (the rest of) asset into part and returns the resulting file size.
func fetchInto(ctx context.Context, client *http.Client, asset Asset, part string, have int64, report func(int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return have, err
	}
	req.Header.Set("User-Agent", "Pocketlab-Updater")
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return have, err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && contentRangeStart(resp.Header.Get("Content-Range")) == have:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC // server ignored the range: restart
		have = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return 0, errors.New("download: server rejected the resume range; retry")
	default:
		return have, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return have, err
	}
	defer f.Close()
	remaining := asset.Size - have
	buf := make([]byte, 64<<10)
	done := have
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		want := int64(len(buf))
		if want > remaining {
			want = remaining
		}
		n, rerr := resp.Body.Read(buf[:want])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			remaining -= int64(n)
			report(done)
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return done, rerr // keep the .part for a later resume
		}
	}
	return done, nil
}

func contentRangeStart(h string) int64 {
	// "bytes 100-999/1000"
	h = strings.TrimPrefix(strings.TrimSpace(h), "bytes ")
	i := strings.IndexByte(h, '-')
	if i < 0 {
		return -1
	}
	n, err := strconv.ParseInt(h[:i], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
