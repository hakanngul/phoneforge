package wda

import (
	"os"
	"strconv"
)

// Default WDA MJPEG settings, measured on a real iPhone 15 (60/25/50 -> ~26 fps at ~1.3 MB/s).
const (
	DefaultMJPEGFPS     = 60
	DefaultMJPEGScaling = 25
	DefaultMJPEGQuality = 50
)

// MJPEGDefaults returns the framerate, scaling and quality to apply on a new
// WDA session. Each value can be overridden with PL_MJPEG_FPS (1-60),
// PL_MJPEG_SCALE (1-100) and PL_MJPEG_QUALITY (1-100); unset, non-numeric or
// out-of-range values fall back to the default.
func MJPEGDefaults() (fps, scaling, quality int) {
	return envInt("PL_MJPEG_FPS", DefaultMJPEGFPS, 1, 60),
		envInt("PL_MJPEG_SCALE", DefaultMJPEGScaling, 1, 100),
		envInt("PL_MJPEG_QUALITY", DefaultMJPEGQuality, 1, 100)
}

func envInt(key string, def, lo, hi int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}
