package avcapture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrame bounds a single frame so a corrupt length prefix cannot trigger a
// huge allocation.
const MaxFrame = 32 << 20

// ErrBadFrame reports a malformed length prefix (stream desynchronised).
var ErrBadFrame = errors.New("avcapture: bad frame length")

// ReadFrame reads one length-prefixed frame (uint32 big-endian length, then
// that many JPEG bytes) from r. A clean end of stream before any prefix byte
// returns io.EOF; a truncated frame returns io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return nil, fmt.Errorf("%w: %d", ErrBadFrame, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// WriteFrame writes one frame in the helper's wire format.
func WriteFrame(w io.Writer, jpeg []byte) error {
	buf := make([]byte, 4+len(jpeg))
	binary.BigEndian.PutUint32(buf, uint32(len(jpeg)))
	copy(buf[4:], jpeg)
	_, err := w.Write(buf)
	return err
}
