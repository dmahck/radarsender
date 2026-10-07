package sender

// recordBuffers retains only small PCAP record buffers, allocated on demand.
// Limits count capacities (not lengths), so a burst cannot leave a large idle
// heap behind. A queue owns this cache; every operation requires its mutex.
const (
	maxCachedRecordBytes = 256 << 10
	maxCachedRecordSlots = 64
	maxCachedRecordSize  = 8192
)

var recordBufferSizes = [...]int{128, 256, 512, 1024, 2048, 4096, maxCachedRecordSize}

type recordBuffers struct {
	buckets [len(recordBufferSizes)][][]byte
	bytes   int
	slots   int
}

func recordBufferClass(size int) int {
	for i, capacity := range recordBufferSizes {
		if size <= capacity {
			return i
		}
	}
	return -1
}

func (b *recordBuffers) take(size int) []byte {
	class := recordBufferClass(size)
	if class < 0 {
		// Jumbo records never occupy the cache.
		return make([]byte, size)
	}
	capacity := recordBufferSizes[class]
	bucket := b.buckets[class]
	if n := len(bucket); n > 0 {
		record := bucket[n-1]
		bucket[n-1] = nil
		b.buckets[class] = bucket[:n-1]
		b.bytes -= capacity
		b.slots--
		return record[:size]
	}
	return make([]byte, size, capacity)
}

func (b *recordBuffers) put(record []byte) {
	capacity := cap(record)
	class := recordBufferClass(capacity)
	if class < 0 || capacity != recordBufferSizes[class] || b.slots >= maxCachedRecordSlots || b.bytes+capacity > maxCachedRecordBytes {
		return
	}
	b.buckets[class] = append(b.buckets[class], record[:0])
	b.bytes += capacity
	b.slots++
}

func (b *recordBuffers) clear() {
	*b = recordBuffers{}
}
