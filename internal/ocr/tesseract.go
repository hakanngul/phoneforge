package ocr

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"phoneforge/internal/device"
)

func recognizeTesseract(ctx context.Context, png []byte, o Options) ([]Result, error) {
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, "tesseract", "stdin", "stdout", "-l", "eng", "tsv")
	cmd.Stdin = bytes.NewReader(png)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ocr: tesseract failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseTesseractTSV(stdout.String(), o.Words)
}

// ParseTesseractTSV parses tesseract TSV output. Words (level 5) are returned
// as-is when words is true; otherwise words on the same line are merged.
// Columns: level page block par line word left top width height conf text.
func ParseTesseractTSV(tsv string, words bool) ([]Result, error) {
	type lineKey struct{ page, block, par, line int }
	var out []Result
	var order []lineKey
	lines := map[lineKey]*Result{}
	confSum := map[lineKey]float64{}
	confN := map[lineKey]int{}

	for i, row := range strings.Split(strings.ReplaceAll(tsv, "\r\n", "\n"), "\n") {
		if row == "" || (i == 0 && strings.HasPrefix(row, "level")) {
			continue
		}
		f := strings.Split(row, "\t")
		if len(f) < 12 {
			continue
		}
		n := make([]int, 10)
		for j := 0; j < 10; j++ {
			v, err := strconv.Atoi(f[j])
			if err != nil {
				return nil, fmt.Errorf("ocr: bad tsv row %d: %v", i+1, err)
			}
			n[j] = v
		}
		conf, err := strconv.ParseFloat(f[10], 64)
		if err != nil {
			return nil, fmt.Errorf("ocr: bad tsv confidence row %d: %v", i+1, err)
		}
		text := strings.TrimSpace(strings.Join(f[11:], "\t"))
		if n[0] != 5 || text == "" || conf < 0 {
			continue
		}
		r := device.Rect{X: n[6], Y: n[7], W: n[8], H: n[9]}
		if words {
			out = append(out, Result{Text: text, Bounds: r, Confidence: conf / 100})
			continue
		}
		k := lineKey{n[1], n[2], n[3], n[4]}
		if cur, ok := lines[k]; ok {
			cur.Text += " " + text
			x2 := max(cur.Bounds.X+cur.Bounds.W, r.X+r.W)
			y2 := max(cur.Bounds.Y+cur.Bounds.H, r.Y+r.H)
			cur.Bounds.X = min(cur.Bounds.X, r.X)
			cur.Bounds.Y = min(cur.Bounds.Y, r.Y)
			cur.Bounds.W = x2 - cur.Bounds.X
			cur.Bounds.H = y2 - cur.Bounds.Y
		} else {
			lines[k] = &Result{Text: text, Bounds: r}
			order = append(order, k)
		}
		confSum[k] += conf / 100
		confN[k]++
	}
	if !words {
		for _, k := range order {
			r := lines[k]
			r.Confidence = confSum[k] / float64(confN[k])
			out = append(out, *r)
		}
	}
	return out, nil
}
