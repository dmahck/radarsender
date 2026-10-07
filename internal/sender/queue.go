package sender

import (
	"context"
	"errors"
	"radarsender/internal/radarupload"
	"sync"
	"sync/atomic"
	"time"
)

// packetQueue owns a bounded, nonblocking capture queue. Only its sender
// goroutine performs network I/O. No packet is replayed after a transport error.
type packetQueue struct {
	stream                                                    *radarupload.Stream
	mu                                                        sync.Mutex
	queue                                                     [][]byte
	buffers                                                   recordBuffers
	head, count                                               int
	bytes, byteLimit                                          int64
	closed                                                    bool
	err                                                       error
	wake                                                      chan struct{}
	done                                                      chan struct{}
	sentPackets, sentBytes, dropped, droppedBytes, sendErrors atomic.Uint64
}

func newPacketQueue(stream *radarupload.Stream) *packetQueue {
	packets, bytes := 512, 4<<20
	m := &packetQueue{stream: stream, queue: make([][]byte, packets), byteLimit: int64(bytes),
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	go m.sendLoop()
	return m
}

func (m *packetQueue) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *packetQueue) fail(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = radarupload.ErrNetwork
	}
	m.mu.Lock()
	if m.err == nil {
		m.err = err
		m.sendErrors.Add(1)
	}
	m.closed = true
	m.discardLocked()
	m.mu.Unlock()
	m.stream.Abort()
	m.signal()
}

func (m *packetQueue) discardLocked() {
	m.dropped.Add(uint64(m.count))
	// Queue byte limits include PCAP headers, but traffic counters do not.
	m.droppedBytes.Add(uint64(m.bytes - int64(m.count*16)))
	for m.count > 0 {
		m.queue[m.head] = nil
		m.head = (m.head + 1) % len(m.queue)
		m.count--
	}
	m.bytes = 0
	m.buffers.clear()
}

func (m *packetQueue) Enqueue(frame []byte, link uint32, at time.Time) bool {
	if link != 1 {
		m.fail(radarupload.ErrCapture)
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		m.dropped.Add(1)
		m.droppedBytes.Add(uint64(len(frame)))
		return false
	}
	charge := len(frame) + 16
	if len(frame) == 0 || len(frame) > 65535 || m.count >= len(m.queue) || m.bytes+int64(charge) > m.byteLimit {
		m.dropped.Add(1)
		m.droppedBytes.Add(uint64(len(frame)))
		return false
	}
	dst := m.buffers.take(charge)
	record, err := radarupload.PCAPRecordInto(dst, frame, at)
	if err != nil {
		m.buffers.put(dst)
		m.dropped.Add(1)
		m.droppedBytes.Add(uint64(len(frame)))
		return false
	}
	m.queue[(m.head+m.count)%len(m.queue)] = record
	m.count++
	m.bytes += int64(len(record))
	m.signal()
	return true
}

func (m *packetQueue) Stats() Counters {
	return Counters{Packets: m.sentPackets.Load(), Bytes: m.sentBytes.Load(), Dropped: m.dropped.Load(), Errors: m.sendErrors.Load()}
}

func (m *packetQueue) Close() error {
	m.mu.Lock()
	m.closed = true
	m.discardLocked()
	m.mu.Unlock()
	m.signal()
	// Stop remains bounded even if a peer accepts TCP but never consumes PCAP.
	timer := time.AfterFunc(10*time.Second, m.stream.Abort)
	defer timer.Stop()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *packetQueue) sendLoop() {
	defer close(m.done)
	defer m.stream.Abort()
	if _, err := m.stream.Write(radarupload.PCAPHeader()); err != nil {
		m.fail(err)
		return
	}
	for {
		m.mu.Lock()
		if m.count == 0 {
			closed := m.closed
			m.mu.Unlock()
			if closed {
				result, err := m.stream.Close()
				if err == nil && result.Packets != m.sentPackets.Load() {
					err = radarupload.ErrProtocol
				}
				if err != nil {
					m.fail(err)
				}
				return
			}
			select {
			case <-m.wake:
			case <-m.stream.Done():
				err := m.stream.Err()
				if err == nil {
					err = radarupload.ErrEarlyEnd
				}
				m.fail(err)
				return
			}
			continue
		}
		record := m.queue[m.head]
		m.queue[m.head] = nil
		m.head = (m.head + 1) % len(m.queue)
		m.count--
		m.bytes -= int64(len(record))
		m.mu.Unlock()
		if _, err := m.stream.Write(record); err != nil {
			m.dropped.Add(1)
			m.droppedBytes.Add(uint64(len(record) - 16))
			m.fail(err)
			return
		}
		m.sentPackets.Add(1)
		m.sentBytes.Add(uint64(len(record) - 16))
		// io.Pipe.Write returns only after its consumer has consumed the whole
		// record. Recycling before Write returned could corrupt the upload.
		m.mu.Lock()
		if !m.closed {
			m.buffers.put(record)
		}
		m.mu.Unlock()
	}
}
