package sender

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"radarsender/internal/radarupload"
	"testing"
	"time"
)

func TestFirstUnknownFrameReachesHTTPBeforeNextPacketOrClose(t *testing.T) {
	frame := make([]byte, 60)
	binary.BigEndian.PutUint16(frame[12:14], 0x88b5)
	frame[59] = 0xa5
	at := time.Unix(1720000000, 123456000)
	record, err := radarupload.PCAPRecord(frame, at)
	if err != nil {
		t.Fatal(err)
	}
	want := append(radarupload.PCAPHeader(), record...)
	type receivedPacket struct {
		data []byte
		err  error
	}
	received := make(chan receivedPacket, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/multi/channel/status":
			_, _ = io.WriteString(w, `{"running":true}`)
		case "/multi/channel/ingest":
			data := make([]byte, len(want))
			_, readErr := io.ReadFull(r.Body, data)
			received <- receivedPacket{data: data, err: readErr}
			if readErr != nil {
				return
			}
			// Keep the request open until the test observes the first frame.
			n, readErr := io.Copy(io.Discard, r.Body)
			if readErr != nil {
				return
			}
			if n != 0 {
				t.Errorf("unexpected extra upload bytes: %d", n)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	client, err := radarupload.Connect(ctx, server.URL+"#fixture", radarupload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	stream := client.Open(ctx)
	q := newPacketQueue(stream)
	t.Cleanup(func() {
		stream.Abort()
		<-q.done
	})
	s := &captureSession{parent: ctx}
	s.attach(q)
	s.forward(frame, at)

	// This is a liveness guard, not a network latency benchmark. Send no second
	// packet and do not close the upload until the first frame has arrived.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case got := <-received:
		if got.err != nil || !bytes.Equal(got.data, want) {
			t.Fatalf("first frame did not arrive intact: %v", got.err)
		}
	case <-timer.C:
		t.Fatal("first frame held until another packet, identification, or close")
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	stats := q.Stats()
	if stats.Packets != 1 || stats.Bytes != uint64(len(frame)) || stats.Dropped != 0 {
		t.Fatalf("unexpected first-frame counters: %+v", stats)
	}
}

func TestUploadQueueChecksAuthoritativeCount(t *testing.T) {
	for _, ack := range []uint64{1, 2} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/multi/channel/status" {
				_, _ = io.WriteString(w, `{"running":true}`)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if len(b) != 100 {
				t.Errorf("unexpected PCAP size %d", len(b))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": ack})
		}))
		client, err := radarupload.Connect(context.Background(), server.URL+"#fixture", radarupload.Options{})
		if err != nil {
			t.Fatal(err)
		}
		q := newPacketQueue(client.Open(context.Background()))
		q.Enqueue(make([]byte, 60), 1, time.Now())
		deadline := time.Now().Add(2 * time.Second)
		for q.Stats().Packets == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		err = q.Close()
		client.Close()
		server.Close()
		if (ack == 1) != (err == nil) {
			t.Fatalf("ack=%d err=%v", ack, err)
		}
	}
}
func TestQueueBoundedAndOwnsPacketCopy(t *testing.T) {
	q := &packetQueue{queue: make([][]byte, 2), byteLimit: 160, wake: make(chan struct{}, 1)}
	b := make([]byte, 60)
	if !q.Enqueue(b, 1, time.Now()) || !q.Enqueue(b, 1, time.Now()) {
		t.Fatal("queue refused available capacity")
	}
	b[0] = 99
	if q.queue[0][16] != 0 {
		t.Fatal("queue kept borrowed data")
	}
	if q.Enqueue(b, 1, time.Now()) || q.Stats().Dropped != 1 {
		t.Fatal("queue unbounded")
	}
	q.mu.Lock()
	q.closed = true
	q.discardLocked()
	q.mu.Unlock()
	if q.Enqueue(b, 1, time.Now()) || q.Stats().Dropped != 4 {
		t.Fatal("close accounting failed")
	}
}
func TestFullSpeedBurstHasNoRateDrops(t *testing.T) {
	const packets, size = 64, 32768
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/multi/channel/status" {
			_, _ = io.WriteString(w, `{"running":true}`)
			return
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n != 24+packets*(size+16) {
			t.Errorf("unexpected PCAP size %d, err %v", n, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": packets})
	}))
	defer server.Close()
	client, err := radarupload.Connect(context.Background(), server.URL+"#fixture", radarupload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	q := newPacketQueue(client.Open(context.Background()))
	for i := 0; i < packets; i++ {
		if !q.Enqueue(make([]byte, size), 1, time.Now()) {
			t.Error("burst rejected below queue bounds")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.Stats().Packets < packets && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	v := q.Stats()
	if v.Packets != packets || v.Dropped != 0 || v.Bytes != packets*size {
		t.Fatalf("burst was rate limited: %+v", v)
	}
}
