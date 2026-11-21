package crashreport

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// simProvider reads iOS Simulator crash reports. Simulator apps run as host
// processes, so macOS writes their reports to ~/Library/Logs/DiagnosticReports;
// a report belongs to this simulator when its text mentions the simulator's
// CoreSimulator/Devices/<udid> path.
type simProvider struct {
	dir  string
	udid string
}

func newSimProvider(dir, udid string) Provider {
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Library", "Logs", "DiagnosticReports")
		}
	}
	return &simProvider{dir: dir, udid: strings.ToLower(udid)}
}

func reportFile(name string) bool {
	return strings.HasSuffix(name, ".ips") || strings.HasSuffix(name, ".crash")
}

func readPrefix(path string, n int) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(n)))
	return string(b), st.Size(), err
}

func (p *simProvider) List(ctx context.Context) ([]Report, error) {
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", p.dir, err)
	}
	var out []Report
	for _, e := range ents {
		if e.IsDir() || !reportFile(e.Name()) {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		head, size, err := readPrefix(filepath.Join(p.dir, e.Name()), iosHeadBytes)
		if err != nil || !strings.Contains(strings.ToLower(head), p.udid) {
			continue
		}
		r := ParseIPS(e.Name(), head)
		r.Size = int(size)
		out = append(out, r)
	}
	return out, nil
}

func (p *simProvider) path(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || !reportFile(name) {
		return "", ErrNotFound
	}
	fp := filepath.Join(p.dir, name)
	if _, err := os.Stat(fp); err != nil {
		return "", ErrNotFound
	}
	return fp, nil
}

func (p *simProvider) Get(ctx context.Context, name string) (string, error) {
	fp, err := p.path(name)
	if err != nil {
		return "", err
	}
	txt, _, err := readPrefix(fp, iosMaxText)
	return txt, err
}

func (p *simProvider) Delete(ctx context.Context, name string) error {
	fp, err := p.path(name)
	if err != nil {
		return err
	}
	return os.Remove(fp)
}
