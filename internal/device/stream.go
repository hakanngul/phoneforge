package device

import (
	"context"
	"io"
)

// VideoStreamer is an optional capability a Device may implement to provide
// live video. StreamH264 writes a raw Annex-B H.264 elementary stream to w
// until ctx is cancelled or a write to w fails.
type VideoStreamer interface {
	StreamH264(ctx context.Context, w io.Writer) error
}
