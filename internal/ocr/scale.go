package ocr

import (
	"bytes"
	"image"
	_ "image/png" // register the PNG decoder for image.DecodeConfig
)

// Scale returns the factors that convert coordinates measured in png's pixels
// into the device's logical coordinate space (w x h, as reported by
// device.Screenshot). It returns 1,1 when the sizes are unknown or equal.
func Scale(png []byte, w, h int) (sx, sy float64) {
	sx, sy = 1, 1
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return
	}
	if w > 0 && cfg.Width != w {
		sx = float64(w) / float64(cfg.Width)
	}
	if h > 0 && cfg.Height != h {
		sy = float64(h) / float64(cfg.Height)
	}
	return
}
