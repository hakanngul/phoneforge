package iosbridge

import (
	"strings"
	"sync"
	"time"
)

// ring is a bounded line buffer implementing io.Writer. Partial lines are held
// until their newline arrives.
type ring struct {
	mu    sync.Mutex
	max   int
	lines []string
	part  string
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.part + string(p)
	parts := strings.Split(s, "\n")
	r.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		r.add(strings.TrimRight(l, "\r"))
	}
	return len(p), nil
}

// logf records a manager-originated line.
func (r *ring) logf(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(time.Now().Format("15:04:05") + " [phoneforge] " + line)
}

func (r *ring) add(l string) {
	r.lines = append(r.lines, l)
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
}

func (r *ring) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.lines...)
	if r.part != "" {
		out = append(out, r.part)
	}
	return out
}

func (r *ring) reset() {
	r.mu.Lock()
	r.lines, r.part = nil, ""
	r.mu.Unlock()
}
