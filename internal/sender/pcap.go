package sender

import (
	"bufio"
	"encoding/binary"
	"io"
	"radarsender/internal/radarupload"
	"time"
)

const pcapReadBufferSize = 32 * 1024

// One frame scratch buffer and bounded read-ahead are used. The caller closes
// the source reader to interrupt a blocked read.
func readPCAP(r io.Reader, ready func(), packet func([]byte, time.Time)) error {
	// Read ahead consumes only bytes already returned by the source; it does not
	// wait to fill the buffer before making a header or first packet available.
	r = bufio.NewReaderSize(r, pcapReadBufferSize)
	h := make([]byte, 24)
	if _, err := io.ReadFull(r, h); err != nil {
		return radarupload.ErrCapture
	}
	var order binary.ByteOrder = binary.LittleEndian
	nano := false
	switch binary.LittleEndian.Uint32(h) {
	case 0xa1b2c3d4:
	case 0xa1b23c4d:
		nano = true
	case 0xd4c3b2a1:
		order = binary.BigEndian
	case 0x4d3cb2a1:
		order = binary.BigEndian
		nano = true
	default:
		return radarupload.ErrCapture
	}
	snap := order.Uint32(h[16:20])
	if order.Uint16(h[4:6]) != 2 || order.Uint16(h[6:8]) != 4 || order.Uint32(h[20:24]) != 1 || snap == 0 || snap > 1<<20 {
		return radarupload.ErrCapture
	}
	ready()
	record := make([]byte, 16)
	frame := make([]byte, 65535)
	for {
		_, err := io.ReadFull(r, record)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return radarupload.ErrCapture
		}
		sec, frac := order.Uint32(record[:4]), order.Uint32(record[4:8])
		length, original := order.Uint32(record[8:12]), order.Uint32(record[12:16])
		limit := uint32(1000000)
		if nano {
			limit = 1000000000
		}
		if length < 14 || length > 65535 || length > snap || original < length || frac >= limit {
			return radarupload.ErrCapture
		}
		if _, err = io.ReadFull(r, frame[:length]); err != nil {
			return radarupload.ErrCapture
		}
		ns := int64(frac)
		if !nano {
			ns *= 1000
		}
		packet(frame[:length], time.Unix(int64(sec), ns))
	}
}
