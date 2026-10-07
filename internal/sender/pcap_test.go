package sender

import (
	"bytes"
	"encoding/binary"
	"radarsender/internal/radarupload"
	"testing"
	"time"
)

func TestPCAPByteOrdersAndTimeUnits(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, nano := range []bool{false, true} {
			header := make([]byte, 24)
			magic := uint32(0xa1b2c3d4)
			frac := uint32(123456)
			if nano {
				magic = 0xa1b23c4d
				frac = 123456000
			}
			order.PutUint32(header, magic)
			order.PutUint16(header[4:], 2)
			order.PutUint16(header[6:], 4)
			order.PutUint32(header[16:], 65535)
			order.PutUint32(header[20:], 1)
			record := make([]byte, 76)
			order.PutUint32(record, 1700000000)
			order.PutUint32(record[4:], frac)
			order.PutUint32(record[8:], 60)
			order.PutUint32(record[12:], 60)
			ready, count := false, 0
			err := readPCAP(bytes.NewReader(append(header, record...)), func() { ready = true }, func(frame []byte, at time.Time) {
				count++
				if len(frame) != 60 || at.Unix() != 1700000000 || at.Nanosecond() != 123456000 {
					t.Fatal("PCAP conversion changed data/time")
				}
			})
			if err != nil || !ready || count != 1 {
				t.Fatal("PCAP rejected", err)
			}
		}
	}
}
func TestPCAPRejectsInvalidLinkAndLengths(t *testing.T) {
	for _, kind := range []string{"link", "length", "truncated", "timestamp"} {
		h := radarupload.PCAPHeader()
		rec, _ := radarupload.PCAPRecord(make([]byte, 60), time.Unix(1700000000, 0))
		switch kind {
		case "link":
			binary.LittleEndian.PutUint32(h[20:], 113)
		case "length":
			binary.LittleEndian.PutUint32(rec[8:], 0xffffffff)
		case "truncated":
			rec = rec[:20]
		case "timestamp":
			binary.LittleEndian.PutUint32(rec[4:], 1000000)
		}
		if readPCAP(bytes.NewReader(append(h, rec...)), func() {}, func([]byte, time.Time) { t.Error("invalid PCAP emitted packet") }) == nil {
			t.Fatal("invalid PCAP accepted", kind)
		}
	}
}
