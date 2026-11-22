package wda

import "testing"

func TestMJPEGDefaults(t *testing.T) {
	f, s, q := MJPEGDefaults()
	if f != 60 || s != 25 || q != 50 {
		t.Fatalf("defaults = %d/%d/%d", f, s, q)
	}
	t.Setenv("PL_MJPEG_FPS", "30")
	t.Setenv("PL_MJPEG_SCALE", "25")
	t.Setenv("PL_MJPEG_QUALITY", "50")
	if f, s, q = MJPEGDefaults(); f != 30 || s != 25 || q != 50 {
		t.Fatalf("overrides = %d/%d/%d", f, s, q)
	}
	t.Setenv("PL_MJPEG_FPS", "61")
	t.Setenv("PL_MJPEG_SCALE", "0")
	t.Setenv("PL_MJPEG_QUALITY", "abc")
	if f, s, q = MJPEGDefaults(); f != 60 || s != 25 || q != 50 {
		t.Fatalf("invalid should fall back, got %d/%d/%d", f, s, q)
	}
}
