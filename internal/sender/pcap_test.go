package sender

import (
	"bytes"
	"encoding/binary"
	"io"
	"radarsender/internal/radarupload"
	"testing"
	"time"
)

type countingPCAPReader struct {
	io.Reader
	reads int
}

func (r *countingPCAPReader) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

func TestPCAPHeaderAndFirstPacketDoNotWaitForMoreData(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ready := make(chan struct{})
	type capturedPacket struct {
		frame []byte
		at    time.Time
	}
	packets := make(chan capturedPacket, 1)
	done := make(chan error, 1)
	counted := &countingPCAPReader{Reader: r}
	go func() {
		done <- readPCAP(counted, func() { close(ready) }, func(frame []byte, at time.Time) {
			packets <- capturedPacket{frame: append([]byte(nil), frame...), at: at}
		})
	}()
	write := func(data []byte) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, err := w.Write(data)
			result <- err
		}()
		return result
	}
	awaitWrite := func(result <-chan error) {
		t.Helper()
		select {
		case err := <-result:
			if err != nil {
				t.Fatal("write synthetic PCAP:", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reader did not consume available PCAP data")
		}
	}
	headerWrite := write(radarupload.PCAPHeader())
	select {
	case <-ready:
	case err := <-done:
		t.Fatal("reader stopped before ready:", err)
	case <-time.After(5 * time.Second):
		t.Fatal("header-only open stream did not become ready")
	}
	awaitWrite(headerWrite)
	frame := bytes.Repeat([]byte{0x5a}, 1518)
	at := time.Unix(1700000000, 123456000)
	record, err := radarupload.PCAPRecord(frame, at)
	if err != nil {
		t.Fatal(err)
	}
	packetWrite := write(record)
	select {
	case got := <-packets:
		if !bytes.Equal(got.frame, frame) || !got.at.Equal(at) {
			t.Fatal("first packet data or timestamp changed")
		}
	case err := <-done:
		t.Fatal("reader stopped before first packet:", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first packet callback waited for a second packet or EOF")
	}
	awaitWrite(packetWrite)
	select {
	case err := <-done:
		t.Fatal("reader stopped on an open stream:", err)
	default:
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("clean EOF rejected:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not finish after EOF")
	}
	t.Logf("open-stream first packet delivered with %d source reads including EOF", counted.reads)
}

func TestPCAPReadAheadReducesBacklogReads(t *testing.T) {
	const packetCount = 4096
	for _, size := range []int{60, 512, 1518} {
		frame := bytes.Repeat([]byte{0x5a}, size)
		at := time.Unix(1700000000, 123456000)
		record, err := radarupload.PCAPRecord(frame, at)
		if err != nil {
			t.Fatal(err)
		}
		data := append(radarupload.PCAPHeader(), bytes.Repeat(record, packetCount)...)
		r := &countingPCAPReader{Reader: bytes.NewReader(data)}
		ready, count := false, 0
		err = readPCAP(r, func() { ready = true }, func(got []byte, gotAt time.Time) {
			count++
			if !bytes.Equal(got, frame) || !gotAt.Equal(at) {
				t.Fatal("read-ahead changed frame data or timestamp")
			}
		})
		if err != nil || !ready || count != packetCount {
			t.Fatal("backlog parse failed:", err, ready, count)
		}
		unbufferedMinimum := 2*packetCount + 2 // Global header, two reads per packet, EOF.
		if r.reads >= unbufferedMinimum/20 {
			t.Fatalf("frame=%d: backlog used %d reads; expected less than %d", size, r.reads, unbufferedMinimum/20)
		}
		t.Logf("frame=%d packets=%d underlying reads=%d (unbuffered minimum %d)", size, count, r.reads, unbufferedMinimum)
	}
}

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
	for _, kind := range []string{"link", "length", "snap", "original", "truncated", "record-truncated", "header-truncated", "timestamp"} {
		h := radarupload.PCAPHeader()
		rec, _ := radarupload.PCAPRecord(make([]byte, 60), time.Unix(1700000000, 0))
		switch kind {
		case "link":
			binary.LittleEndian.PutUint32(h[20:], 113)
		case "length":
			binary.LittleEndian.PutUint32(rec[8:], 0xffffffff)
		case "snap":
			binary.LittleEndian.PutUint32(h[16:], 59)
		case "original":
			binary.LittleEndian.PutUint32(rec[12:], 59)
		case "truncated":
			rec = rec[:20]
		case "record-truncated":
			rec = rec[:15]
		case "header-truncated":
			h, rec = h[:23], nil
		case "timestamp":
			binary.LittleEndian.PutUint32(rec[4:], 1000000)
		}
		if readPCAP(bytes.NewReader(append(h, rec...)), func() {}, func([]byte, time.Time) { t.Error("invalid PCAP emitted packet") }) == nil {
			t.Fatal("invalid PCAP accepted", kind)
		}
	}
}

func TestPCAPFrameLengthBoundaries(t *testing.T) {
	for _, size := range []int{13, 14, 65535, 65536} {
		h := radarupload.PCAPHeader()
		record := make([]byte, 16+size)
		binary.LittleEndian.PutUint32(record, 1700000000)
		binary.LittleEndian.PutUint32(record[8:], uint32(size))
		binary.LittleEndian.PutUint32(record[12:], uint32(size))
		copy(record[16:], bytes.Repeat([]byte{0x5a}, size))
		count := 0
		err := readPCAP(bytes.NewReader(append(h, record...)), func() {}, func(frame []byte, at time.Time) {
			count++
			if len(frame) != size || !bytes.Equal(frame, record[16:]) || at.Unix() != 1700000000 {
				t.Fatalf("frame=%d: frame data or timestamp changed", size)
			}
		})
		valid := size >= 14 && size <= 65535
		if valid && (err != nil || count != 1) {
			t.Fatalf("frame=%d: valid complete frame rejected: %v (count=%d)", size, err, count)
		}
		if !valid && (err == nil || count != 0) {
			t.Fatalf("frame=%d: invalid frame accepted: %v (count=%d)", size, err, count)
		}
	}
}
