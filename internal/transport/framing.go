package transport

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	KindJSON   byte = 1
	KindBinary byte = 2
	KindPing   byte = 3
	KindPong   byte = 4
	KindClose  byte = 5

	MaxFramePayload = 16 * 1024 * 1024
)

type Frame struct {
	Kind    byte
	Payload []byte
}

func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFramePayload {
		return Frame{}, fmt.Errorf("frame payload too large: %d", n)
	}
	payload := make([]byte, int(n))
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Kind: hdr[0], Payload: payload}, nil
}

func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > MaxFramePayload {
		return fmt.Errorf("frame payload too large: %d", len(payload))
	}
	var hdr [5]byte
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}
