package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadOK(t *testing.T) {
	f := newFeed(t, "0.4.2")
	dir := t.TempDir()
	var last, total int64
	p, err := Download(context.Background(), nil, f.asset(), dir, func(d, tt int64) { last, total = d, tt })
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(f.archive) || filepath.Dir(p) != dir {
		t.Fatal("downloaded content differs")
	}
	if _, err := os.Stat(p + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part should be gone")
	}
	if last != total || total != int64(len(f.archive)) {
		t.Fatalf("progress %d/%d", last, total)
	}
	// a second call reuses the verified file without a request
	n := f.reqs.Load()
	if _, err := Download(context.Background(), nil, f.asset(), dir, nil); err != nil || f.reqs.Load() != n {
		t.Fatalf("cached download: %v reqs %d->%d", err, n, f.reqs.Load())
	}
}

func TestDownloadSHAMismatch(t *testing.T) {
	f := newFeed(t, "0.4.2")
	a := f.asset()
	a.SHA256 = sha([]byte("something else"))
	dir := t.TempDir()
	if _, err := Download(context.Background(), nil, a, dir, nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("mismatching download must be deleted: %v", ents)
	}
}

func TestDownloadTruncatedThenResumed(t *testing.T) {
	f := newFeed(t, "0.4.2")
	dir := t.TempDir()
	f.mu.Lock()
	f.abortAt = 100_000
	f.mu.Unlock()
	if _, err := Download(context.Background(), nil, f.asset(), dir, nil); err == nil {
		t.Fatal("truncated transfer must fail")
	}
	part := filepath.Join(dir, f.asset().Name+".part")
	st, err := os.Stat(part)
	if err != nil || st.Size() == 0 || st.Size() >= int64(len(f.archive)) {
		t.Fatalf("partial file expected: %v %v", st, err)
	}
	have := st.Size()
	if _, err := os.Stat(filepath.Join(dir, f.asset().Name)); err == nil {
		t.Fatal("no final file after a truncated download")
	}

	p, err := Download(context.Background(), nil, f.asset(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(f.archive) {
		t.Fatal("resumed content differs")
	}
	f.mu.Lock()
	last := f.ranges[len(f.ranges)-1]
	f.mu.Unlock()
	if !strings.HasPrefix(last, "bytes=") || !strings.HasSuffix(last, "-") {
		t.Fatalf("resume must send a Range header, got %q", last)
	}
	_ = have
}

func TestDownloadServerIgnoresRange(t *testing.T) {
	f := newFeed(t, "0.4.2")
	f.mu.Lock()
	f.noRange = true
	f.mu.Unlock()
	dir := t.TempDir()
	// a stale partial file with garbage content
	_ = os.WriteFile(filepath.Join(dir, f.asset().Name+".part"), []byte("garbage-prefix"), 0o600)
	p, err := Download(context.Background(), nil, f.asset(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(f.archive) {
		t.Fatal("a 200 answer to a Range request must restart the download")
	}
}

func TestDownloadShortBodyIsSizeMismatch(t *testing.T) {
	f := newFeed(t, "0.4.2")
	a := f.asset()
	a.Size = int64(len(f.archive)) + 1000 // manifest claims more than the server has
	if _, err := Download(context.Background(), nil, a, t.TempDir(), nil); err == nil {
		t.Fatal("short body must fail")
	}
}

func TestDownloadNeverReadsPastSize(t *testing.T) {
	f := newFeed(t, "0.4.2")
	f.mu.Lock()
	f.extra = []byte("EXTRA-BYTES-AFTER-THE-ARCHIVE")
	f.noRange = true
	f.mu.Unlock()
	p, err := Download(context.Background(), nil, f.asset(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != int64(len(f.archive)) {
		t.Fatalf("file size %d, want %d", st.Size(), len(f.archive))
	}
}

func TestDownloadHTTPError(t *testing.T) {
	f := newFeed(t, "0.4.2")
	a := f.asset()
	a.URL = f.srv.URL + "/nope/file.zip"
	if _, err := Download(context.Background(), nil, a, t.TempDir(), nil); err == nil {
		t.Fatal("404 must fail")
	}
}
