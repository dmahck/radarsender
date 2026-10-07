package sender

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"
)

func TestFirstUnknownFrameImmediatelyQueued(t *testing.T) {
	s := &captureSession{parent: context.Background()}
	q := &packetQueue{queue: make([][]byte, 2), byteLimit: 160, wake: make(chan struct{}, 1)}
	s.attach(q)
	frame := make([]byte, 60)
	binary.BigEndian.PutUint16(frame[12:14], 0x88b5) // Unknown, non-game-specific EtherType.
	frame[59] = 0xa5
	at := time.Unix(1720000000, 123456000)
	s.forward(frame, at)

	// Check synchronously: no second packet, classifier, timer, or flush is needed.
	if q.count != 1 {
		t.Fatal("first unknown frame was held for identification")
	}
	select {
	case <-q.wake:
	default:
		t.Fatal("first frame did not wake the sender")
	}
	record := q.queue[q.head]
	if len(record) != 16+len(frame) || !bytes.Equal(record[16:], frame) {
		t.Fatal("first frame was altered")
	}
	if binary.LittleEndian.Uint32(record[0:4]) != uint32(at.Unix()) ||
		binary.LittleEndian.Uint32(record[4:8]) != 123456 ||
		binary.LittleEndian.Uint32(record[8:12]) != uint32(len(frame)) ||
		binary.LittleEndian.Uint32(record[12:16]) != uint32(len(frame)) {
		t.Fatal("capture timestamp or frame length was altered")
	}
	stats, _ := s.stats()
	if stats.Captured != 1 || stats.Dropped != 0 || q.Stats().Dropped != 0 {
		t.Fatal("first frame was counted as dropped")
	}
}

func TestOfflineCaptureDiscardsWithoutQueueing(t *testing.T) {
	s := &captureSession{parent: context.Background()}
	packet := make([]byte, 60)
	for i := 0; i < 1000; i++ {
		s.forward(packet, time.Now())
	}
	stats, _ := s.stats()
	if stats.Captured != 1000 || stats.OfflineDropped != 1000 || stats.Dropped != 1000 || s.queue != nil {
		t.Fatal("offline frames retained or not accounted")
	}
	q := &packetQueue{queue: make([][]byte, 2), byteLimit: 160, wake: make(chan struct{}, 1)}
	s.attach(q)
	s.forward(packet, time.Now())
	if q.count != 1 {
		t.Fatal("offline frames replayed on attach")
	}
	s.detach()
	s.forward(packet, time.Now())
	stats, _ = s.stats()
	if q.count != 1 || stats.Captured != 1002 || stats.OfflineDropped != 1001 {
		t.Fatal("detached queue still receives frames")
	}
}
