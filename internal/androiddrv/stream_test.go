package androiddrv

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"pocketlab/internal/device"
)

var _ device.VideoStreamer = (*androidDevice)(nil)

func TestParseWMSize(t *testing.T) {
	cases := []struct {
		in   string
		w, h int
		ok   bool
	}{
		{"Physical size: 1080x2400\n", 1080, 2400, true},
		{"Physical size: 1080x2400\r\nOverride size: 720x1600\r\n", 1080, 2400, true},
		{"  Physical size: 720x1280  ", 720, 1280, true},
		{"", 0, 0, false},
		{"error: closed", 0, 0, false},
		{"Physical size: axb", 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := parseWMSize(c.in)
		if w != c.w || h != c.h || ok != c.ok {
			t.Errorf("parseWMSize(%q) = %d,%d,%v want %d,%d,%v", c.in, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

func TestEvenHalf(t *testing.T) {
	cases := [][4]int{
		{1080, 2400, 540, 1200},
		{1080, 2340, 540, 1170 - 1170%2},
		{721, 1281, 360, 640},
		{2, 3, 2, 2},
	}
	for _, c := range cases {
		w, h := evenHalf(c[0], c[1])
		if w != c[2] || h != c[3] || w%2 != 0 || h%2 != 0 {
			t.Errorf("evenHalf(%d,%d) = %d,%d want %d,%d", c[0], c[1], w, h, c[2], c[3])
		}
	}
}

func TestFlushWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	w := newFlushWriter(rec)
	if _, ok := w.(flushWriter); !ok {
		t.Fatal("expected flushWriter for http.Flusher")
	}
	if _, err := w.Write([]byte("x")); err != nil || !rec.Flushed {
		t.Fatalf("write/flush: err=%v flushed=%v", err, rec.Flushed)
	}
	var buf bytes.Buffer
	if _, ok := newFlushWriter(&buf).(flushWriter); ok {
		t.Fatal("plain writer should not be wrapped")
	}
}

func TestStreamBitRateEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 3000000}, {"abc", 3000000}, {"499999", 3000000}, {"20000001", 3000000},
		{"500000", 500000}, {"20000000", 20000000}, {" 8000000 ", 8000000},
	}
	for _, c := range cases {
		t.Setenv("PL_ANDROID_BITRATE", c.env)
		if got := streamBitRate(); got != c.want {
			t.Errorf("bitrate(%q) = %d want %d", c.env, got, c.want)
		}
	}
}

func TestStreamScaleEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 50}, {"x", 50}, {"24", 50}, {"101", 50}, {"25", 25}, {"100", 100}, {"75", 75},
	}
	for _, c := range cases {
		t.Setenv("PL_ANDROID_SCALE", c.env)
		if got := streamScale(); got != c.want {
			t.Errorf("scale(%q) = %d want %d", c.env, got, c.want)
		}
	}
}

func TestScaleSize(t *testing.T) {
	cases := [][5]int{
		{1080, 2400, 50, 540, 1200},
		{1080, 2400, 100, 1080, 2400},
		{1080, 2400, 25, 270, 600},
		{1080, 2400, 75, 810, 1800},
		{1081, 2401, 100, 1080, 2400},
		{1080, 2340, 33, 356, 772},
		{4, 4, 25, 2, 2},
	}
	for _, c := range cases {
		w, h := scaleSize(c[0], c[1], c[2])
		if w != c[3] || h != c[4] {
			t.Errorf("scaleSize(%d,%d,%d) = %d,%d want %d,%d", c[0], c[1], c[2], w, h, c[3], c[4])
		}
	}
}
