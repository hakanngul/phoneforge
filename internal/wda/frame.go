package wda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG decoder for WDA screenshots
	"net/http"
	"strings"
)

// ScreenshotJPEG fetches one WDA screenshot (no session needed) and returns it
// as a JPEG scaled to at most maxWidth pixels wide (0 keeps the size). It is
// used to give MJPEG viewers a first frame without waiting for WDA's stream.
func (c *Client) ScreenshotJPEG(ctx context.Context, maxWidth, quality int) ([]byte, error) {
	data, err := c.doGlobal(ctx, http.MethodGet, "/screenshot", nil)
	if err != nil {
		return nil, err
	}
	var b64 string
	if err := json.Unmarshal(data, &b64); err != nil {
		return nil, fmt.Errorf("wda: screenshot is not a string: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(b64))
	if err != nil {
		return nil, fmt.Errorf("wda: decode screenshot: %w", err)
	}
	return ImageToJPEG(raw, maxWidth, quality)
}

// ImageToJPEG decodes a PNG/JPEG, box-scales it to at most maxWidth wide and
// re-encodes it as JPEG.
func ImageToJPEG(raw []byte, maxWidth, quality int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("wda: decode image: %w", err)
	}
	if quality <= 0 || quality > 100 {
		quality = 60
	}
	out := scaleBox(src, maxWidth)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scaleBox downsamples src to width maxWidth by averaging source boxes. It
// returns src unchanged when it is already narrow enough.
func scaleBox(src image.Image, maxWidth int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if maxWidth <= 0 || sw <= maxWidth || sw == 0 || sh == 0 {
		return src
	}
	dw := maxWidth
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	var pix func(x, y int) (uint32, uint32, uint32) // 8-bit components
	switch s := src.(type) {
	case *image.RGBA:
		pix = func(x, y int) (uint32, uint32, uint32) {
			i := s.PixOffset(x+b.Min.X, y+b.Min.Y)
			return uint32(s.Pix[i]), uint32(s.Pix[i+1]), uint32(s.Pix[i+2])
		}
	case *image.NRGBA:
		pix = func(x, y int) (uint32, uint32, uint32) {
			i := s.PixOffset(x+b.Min.X, y+b.Min.Y)
			return uint32(s.Pix[i]), uint32(s.Pix[i+1]), uint32(s.Pix[i+2])
		}
	default:
		pix = func(x, y int) (uint32, uint32, uint32) {
			r, g, bl, _ := src.At(x+b.Min.X, y+b.Min.Y).RGBA()
			return r >> 8, g >> 8, bl >> 8
		}
	}
	for dy := 0; dy < dh; dy++ {
		sy0, sy1 := dy*sh/dh, (dy+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0, sx1 := dx*sw/dw, (dx+1)*sw/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var sr, sg, sb, n uint32
			for y := sy0; y < sy1; y++ {
				for x := sx0; x < sx1; x++ {
					r, g, bl := pix(x, y)
					sr, sg, sb, n = sr+r, sg+g, sb+bl, n+1
				}
			}
			dst.SetRGBA(dx, dy, color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), 255})
		}
	}
	return dst
}
