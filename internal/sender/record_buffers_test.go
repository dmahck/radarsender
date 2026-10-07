package sender

import (
	"bytes"
	"radarsender/internal/radarupload"
	"testing"
	"time"
)

func TestRecordBuffersAllocateLazilyAndReuseBySize(t *testing.T) {
	var cache recordBuffers
	if cache.bytes != 0 || cache.slots != 0 {
		t.Fatal("empty cache retained record memory")
	}
	record := cache.take(1516)
	if len(record) != 1516 || cap(record) != 2048 || cache.slots != 0 {
		t.Fatal("record was not allocated in the expected size class")
	}
	address := &record[0]
	cache.put(record)
	if cache.bytes != 2048 || cache.slots != 1 {
		t.Fatal("cached capacity accounting is incorrect")
	}
	reused := cache.take(1416)
	if &reused[0] != address || len(reused) != 1416 || cache.bytes != 0 || cache.slots != 0 {
		t.Fatal("matching size class did not recycle the record")
	}
}

func TestRecordBuffersHaveStrictSlotAndCapacityBounds(t *testing.T) {
	for _, size := range []int{76, 1516, 8192} {
		var cache recordBuffers
		// Keep all records checked out first to model a completed capture burst.
		records := make([][]byte, 512)
		for i := range records {
			records[i] = cache.take(size)
		}
		for _, record := range records {
			cache.put(record)
			if cache.bytes > maxCachedRecordBytes || cache.slots > maxCachedRecordSlots {
				t.Fatalf("cache exceeded its bounds for size %d: bytes=%d slots=%d", size, cache.bytes, cache.slots)
			}
		}
		capacity := cap(records[0])
		wantSlots := maxCachedRecordSlots
		if maxCachedRecordBytes/capacity < wantSlots {
			wantSlots = maxCachedRecordBytes / capacity
		}
		if cache.slots != wantSlots || cache.bytes != wantSlots*capacity {
			t.Fatalf("unexpected bounded cache occupancy: bytes=%d slots=%d", cache.bytes, cache.slots)
		}
		for cache.slots > 0 {
			_ = cache.take(size)
		}
		if cache.bytes != 0 {
			t.Fatal("taking cached records did not release its capacity charge")
		}
	}
}

func TestRecordBuffersNeverCacheJumboOrUnclassifiedStorage(t *testing.T) {
	var cache recordBuffers
	for _, size := range []int{8193, 16384, 65551} {
		record := cache.take(size)
		if cap(record) != size {
			t.Fatal("jumbo record has unnecessary spare allocation")
		}
		cache.put(record)
	}
	cache.put(make([]byte, 1516))
	if cache.bytes != 0 || cache.slots != 0 {
		t.Fatal("jumbo or unclassified storage was retained")
	}
}

func TestRecordBuffersClearReleasesAllReferences(t *testing.T) {
	var cache recordBuffers
	for _, size := range []int{76, 1516, 8192} {
		cache.put(cache.take(size))
	}
	cache.clear()
	if cache.bytes != 0 || cache.slots != 0 {
		t.Fatal("cache charge remained after clearing")
	}
	for _, bucket := range cache.buckets {
		if bucket != nil {
			t.Fatal("cache backing slice retained record references")
		}
	}
}

func TestQueueDiscardCountersAndCacheCleanup(t *testing.T) {
	q := &packetQueue{queue: make([][]byte, 8), byteLimit: 4 << 20, wake: make(chan struct{}, 1)}
	q.buffers.put(q.buffers.take(76))
	for _, size := range []int{60, 1500, 8192} {
		if !q.Enqueue(make([]byte, size), 1, time.Unix(1720000000, 0)) {
			t.Fatal("queue refused a frame within its limits")
		}
	}
	q.mu.Lock()
	q.closed = true
	q.discardLocked()
	q.mu.Unlock()
	if q.count != 0 || q.bytes != 0 || q.buffers.bytes != 0 || q.buffers.slots != 0 {
		t.Fatal("discard retained a queued or cached record")
	}
	for _, record := range q.queue {
		if record != nil {
			t.Fatal("discard retained a queue backing reference")
		}
	}
	if q.Stats().Dropped != 3 || q.droppedBytes.Load() != 60+1500+8192 {
		t.Fatal("discard counters included PCAP headers or miscounted frames")
	}
	q.Enqueue(make([]byte, 60), 1, time.Unix(1720000000, 0))
	if q.Stats().Dropped != 4 || q.droppedBytes.Load() != 60+1500+8192+60 || q.buffers.slots != 0 {
		t.Fatal("closed queue allocated storage or lost its drop count")
	}
}

func TestQueueCachedRecordStillOwnsFrameCopy(t *testing.T) {
	q := &packetQueue{queue: make([][]byte, 2), byteLimit: 4096, wake: make(chan struct{}, 1)}
	record := q.buffers.take(1516)
	address := &record[0]
	q.buffers.put(record)
	frame := bytes.Repeat([]byte{0xa5}, 1500)
	if !q.Enqueue(frame, 1, time.Unix(1720000000, 0)) {
		t.Fatal("queue rejected available capacity")
	}
	frame[0] = 99
	if &q.queue[0][0] != address || q.queue[0][16] != 0xa5 || q.buffers.slots != 0 {
		t.Fatal("recycled storage did not independently own its frame")
	}
	if !q.Enqueue(frame, 1, time.Unix(1720000000, 0)) || &q.queue[0][0] == &q.queue[1][0] {
		t.Fatal("queued record was reused before its consumer released it")
	}
}

// One steady-state cycle models record serialization, queue removal and a
// completed write. Network allocations are intentionally excluded.
func BenchmarkRecordBufferCycle(b *testing.B) {
	frame := make([]byte, 1500)
	at := time.Unix(1720000000, 123456000)
	var cache recordBuffers
	cache.put(cache.take(1516))
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record, err := radarupload.PCAPRecordInto(cache.take(len(frame)+16), frame, at)
		if err != nil || len(record) != 1516 {
			b.Fatal("record failed")
		}
		cache.put(record)
	}
}
