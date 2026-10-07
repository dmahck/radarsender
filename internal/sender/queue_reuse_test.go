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

func TestCachedQueueRecordsReachHTTPIntactInOrder(t *testing.T) {
	const total, batch = 1024, 64
	template := bytes.Repeat([]byte{0x5a}, 1500)
	binary.BigEndian.PutUint16(template[12:14], 0x88b5)
	at := time.Unix(1720000000, 123456000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/multi/channel/status" {
			_, _ = io.WriteString(w, `{"running":true}`)
			return
		}
		if r.URL.Path != "/multi/channel/ingest" {
			http.NotFound(w, r)
			return
		}
		count := 0
		err := readPCAP(r.Body, func() {}, func(frame []byte, timestamp time.Time) {
			want := append([]byte(nil), template...)
			binary.BigEndian.PutUint32(want[14:18], uint32(count))
			if !bytes.Equal(frame, want) || !timestamp.Equal(at) {
				t.Errorf("record %d changed while capture buffer or record cache was reused", count)
			}
			count++
		})
		if err != nil || count != total {
			t.Errorf("receiver count=%d error=%v", count, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "packets": count})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := radarupload.Connect(ctx, server.URL+"#fixture", radarupload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stream := client.Open(ctx)
	q := newPacketQueue(stream)
	defer func() {
		stream.Abort()
		<-q.done
	}()
	frame := append([]byte(nil), template...)
	for base := 0; base < total; base += batch {
		for i := base; i < base+batch; i++ {
			binary.BigEndian.PutUint32(frame[14:18], uint32(i))
			if !q.Enqueue(frame, 1, at) {
				t.Fatal("queue rejected a bounded batch")
			}
		}
		// Reuse the producer's scratch storage before the consumer completes.
		binary.BigEndian.PutUint32(frame[14:18], 0xffffffff)
		for q.Stats().Packets < uint64(base+batch) {
			select {
			case <-ctx.Done():
				t.Fatal("queued records did not complete their writes")
			case <-q.done:
				t.Fatal("stream ended before all records were sent")
			case <-time.After(time.Millisecond):
			}
		}
	}
	if err := q.Close(); err != nil {
		t.Fatal("authoritative receiver count failed:", err)
	}
	stats := q.Stats()
	if stats.Packets != total || stats.Bytes != total*uint64(len(frame)) || stats.Dropped != 0 || stats.Errors != 0 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
	if q.buffers.slots != 0 || q.buffers.bytes != 0 {
		t.Fatal("closed upload retained cached records")
	}
}
