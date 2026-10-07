package radarupload

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestPCAPRecordIntoReusesStorageAndCopiesFrame(t *testing.T) {
	frame := []byte{1, 2, 3, 4, 5}
	dst := bytes.Repeat([]byte{0xff}, 128)
	at := time.Unix(1720000000, 123456789)
	record, err := PCAPRecordInto(dst[:0], frame, at)
	if err != nil {
		t.Fatal(err)
	}
	if &record[0] != &dst[0] || len(record) != 16+len(frame) {
		t.Fatal("record failed to reuse sufficient destination storage")
	}
	if seconds := binary.LittleEndian.Uint32(record); seconds != 1720000000 {
		t.Fatalf("seconds = %d", seconds)
	}
	if micros := binary.LittleEndian.Uint32(record[4:]); micros != 123456 {
		t.Fatalf("microseconds = %d", micros)
	}
	if binary.LittleEndian.Uint32(record[8:]) != 5 || binary.LittleEndian.Uint32(record[12:]) != 5 || !bytes.Equal(record[16:], frame) {
		t.Fatalf("record contents changed: %x", record)
	}
	frame[0] = 99
	if record[16] != 1 {
		t.Fatal("record retained borrowed frame data")
	}
	fresh, err := PCAPRecord([]byte{1, 2, 3, 4, 5}, at)
	if err != nil || !bytes.Equal(record, fresh) {
		t.Fatal("existing PCAPRecord output changed")
	}
	if dst[len(record)] != 0xff {
		t.Fatal("destination bytes beyond the record changed")
	}
}

func TestPCAPRecordIntoGrowsAndSupportsOverlap(t *testing.T) {
	at := time.Unix(1720000000, 0)
	frame := bytes.Repeat([]byte{0xa5}, 60)
	small := make([]byte, 8)
	record, err := PCAPRecordInto(small, frame, at)
	if err != nil || len(record) != 76 || !bytes.Equal(record[16:], frame) || &record[0] == &small[0] {
		t.Fatal("insufficient destination was not replaced")
	}
	overlapping := make([]byte, 128)
	copy(overlapping, frame)
	record, err = PCAPRecordInto(overlapping[:0], overlapping[:len(frame)], at)
	if err != nil || !bytes.Equal(record[16:], frame) {
		t.Fatal("overlapping frame was overwritten before it was copied")
	}
}

func TestPCAPRecordIntoValidationLeavesDestinationUntouched(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame []byte
		at    time.Time
	}{
		{"empty", nil, time.Unix(0, 0)},
		{"oversized", make([]byte, 65536), time.Unix(0, 0)},
		{"before_epoch", []byte{1}, time.Unix(-1, 0)},
		{"seconds_overflow", []byte{1}, time.Unix(1<<32, 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dst := bytes.Repeat([]byte{0x5a}, 32)
			record, err := PCAPRecordInto(dst, test.frame, test.at)
			if err != ErrCapture || record != nil || !bytes.Equal(dst, bytes.Repeat([]byte{0x5a}, 32)) {
				t.Fatal("invalid input mutated its destination or changed validation")
			}
		})
	}
	for _, at := range []time.Time{time.Unix(0, 0), time.Unix(0xffffffff, 999999999)} {
		if _, err := PCAPRecordInto(nil, []byte{1}, at); err != nil {
			t.Fatalf("valid boundary timestamp was rejected: %v", err)
		}
	}
}

func BenchmarkPCAPRecord(b *testing.B) {
	frame := make([]byte, 1500)
	at := time.Unix(1720000000, 123456000)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		record, err := PCAPRecord(frame, at)
		if err != nil || len(record) != 1516 {
			b.Fatal("record failed")
		}
	}
}

func BenchmarkPCAPRecordInto(b *testing.B) {
	frame := make([]byte, 1500)
	dst := make([]byte, 1516)
	at := time.Unix(1720000000, 123456000)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		record, err := PCAPRecordInto(dst, frame, at)
		if err != nil || len(record) != 1516 {
			b.Fatal("record failed")
		}
	}
}
