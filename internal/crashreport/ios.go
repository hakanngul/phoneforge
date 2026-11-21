package crashreport

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/afc"
	gocrash "github.com/danielpaulus/go-ios/ios/crashreport"

	"phoneforge/internal/device"
)

const (
	iosService   = "com.apple.crashreportcopymobile"
	iosHeadBytes = 48 << 10
	iosMaxFiles  = 400
	iosTimeout   = 90 * time.Second
	iosMaxText   = 16 << 20
)

type iosProvider struct{ serial string }

func newIOSProvider(serial string) Provider { return &iosProvider{serial: serial} }

func runCtx[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() { v, err := fn(); ch <- res{v, err} }()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (p *iosProvider) entry() (ios.DeviceEntry, error) {
	e, err := ios.GetDevice(p.serial)
	if err != nil {
		return e, fmt.Errorf("iOS device not found (connected, unlocked, trusted?): %v: %w", err, device.ErrNotFound)
	}
	return e, nil
}

func (p *iosProvider) List(ctx context.Context) ([]Report, error) {
	ctx, cancel := context.WithTimeout(ctx, iosTimeout)
	defer cancel()
	return runCtx(ctx, func() ([]Report, error) {
		e, err := p.entry()
		if err != nil {
			return nil, err
		}
		paths, err := gocrash.ListReports(e, "*") // also moves new reports into the copy area
		if err != nil {
			return nil, fmt.Errorf("iOS crash report listing: %w", err)
		}
		if len(paths) > iosMaxFiles {
			paths = paths[len(paths)-iosMaxFiles:]
		}
		conn, err := ios.ConnectToService(e, iosService)
		if err != nil {
			return nil, fmt.Errorf("iOS crash report service: %w", err)
		}
		defer conn.Close()
		c := afc.NewFromConn(conn)
		out := make([]Report, 0, len(paths))
		for _, fp := range paths {
			head, size, rerr := readHead(c, fp, iosHeadBytes)
			if rerr != nil {
				continue
			}
			r := ParseIPS(path.Base(fp), head)
			r.Size = int(size)
			out = append(out, r)
		}
		return out, nil
	})
}

func readHead(c *afc.Client, p string, n int) (string, int64, error) {
	st, err := c.Stat(p)
	if err != nil {
		return "", 0, err
	}
	f, err := c.Open(p, afc.READ_ONLY)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	buf := make([]byte, 0, n)
	tmp := make([]byte, 32<<10)
	for len(buf) < n {
		k, rerr := f.Read(tmp)
		buf = append(buf, tmp[:k]...)
		if rerr != nil {
			break
		}
	}
	return string(buf), st.Size, nil
}

// find resolves a report name to its AFC path.
func (p *iosProvider) find(e ios.DeviceEntry, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == ".." {
		return "", fmt.Errorf("invalid report name: %w", ErrNotFound)
	}
	paths, err := gocrash.ListReports(e, "*")
	if err != nil {
		return "", fmt.Errorf("iOS crash report listing: %w", err)
	}
	for _, fp := range paths {
		if path.Base(fp) == name {
			return fp, nil
		}
	}
	return "", ErrNotFound
}

func (p *iosProvider) Get(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, iosTimeout)
	defer cancel()
	return runCtx(ctx, func() (string, error) {
		e, err := p.entry()
		if err != nil {
			return "", err
		}
		fp, err := p.find(e, name)
		if err != nil {
			return "", err
		}
		conn, err := ios.ConnectToService(e, iosService)
		if err != nil {
			return "", fmt.Errorf("iOS crash report service: %w", err)
		}
		defer conn.Close()
		c := afc.NewFromConn(conn)
		f, err := c.Open(fp, afc.READ_ONLY)
		if err != nil {
			return "", err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, iosMaxText))
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}

// globEscape escapes filepath.Match metacharacters.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (p *iosProvider) Delete(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, iosTimeout)
	defer cancel()
	_, err := runCtx(ctx, func() (struct{}, error) {
		e, err := p.entry()
		if err != nil {
			return struct{}{}, err
		}
		if _, err := p.find(e, name); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, gocrash.RemoveReports(e, ".", globEscape(name))
	})
	return err
}
