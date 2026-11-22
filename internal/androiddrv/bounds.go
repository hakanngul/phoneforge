package androiddrv

import (
	"fmt"

	"pocketlab/internal/device"
)

// parseBounds parses a uiautomator bounds string "[x1,y1][x2,y2]".
func parseBounds(s string) (device.Rect, bool) {
	var x1, y1, x2, y2 int
	var tail string
	n, _ := fmt.Sscanf(s, "[%d,%d][%d,%d]%s", &x1, &y1, &x2, &y2, &tail)
	if n < 4 || n > 4 {
		return device.Rect{}, false
	}
	return device.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}, true
}
