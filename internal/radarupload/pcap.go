package radarupload

import (
	"encoding/binary"
	"time"
)

// PCAPHeader produces exactly the classic little-endian Ethernet PCAP format
// accepted by the gateway. The capture source is converted before transmission.
func PCAPHeader() []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b, 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(b[4:], 2)
	binary.LittleEndian.PutUint16(b[6:], 4)
	binary.LittleEndian.PutUint32(b[16:], 65535)
	binary.LittleEndian.PutUint32(b[20:], 1)
	return b
}

func PCAPRecord(frame []byte, at time.Time) ([]byte, error) {
	return PCAPRecordInto(nil, frame, at)
}

// PCAPRecordInto writes a record into dst when its capacity is sufficient.
// The returned slice owns a copy of frame; callers must not reuse dst until
// its consumer has finished. Invalid input leaves dst unchanged.
func PCAPRecordInto(dst, frame []byte, at time.Time) ([]byte, error) {
	seconds := at.Unix()
	if len(frame) == 0 || len(frame) > 65535 || seconds < 0 || seconds > 0xffffffff {
		return nil, ErrCapture
	}
	size := 16 + len(frame)
	if cap(dst) < size {
		dst = make([]byte, size)
	} else {
		dst = dst[:size]
	}
	b := dst
	// Copy first so a caller can also provide overlapping source storage.
	copy(b[16:], frame)
	binary.LittleEndian.PutUint32(b, uint32(seconds))
	binary.LittleEndian.PutUint32(b[4:], uint32(at.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(b[8:], uint32(len(frame)))
	binary.LittleEndian.PutUint32(b[12:], uint32(len(frame)))
	return b, nil
}
