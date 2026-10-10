package sender

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Capture outlives individual HTTP uploads. Frames without a live upload are
// counted and discarded immediately, never retained for replay after reconnect.
type captureSession struct {
	parent            context.Context
	mu                sync.Mutex
	iface             string
	cancel            context.CancelFunc
	ctx               context.Context
	done              chan struct{}
	err               error
	running           bool
	queue             *packetQueue
	captured, offline uint64
	flows             flowSet
}

func (s *captureSession) ensure(name string) (context.Context, error) {
	s.mu.Lock()
	same := s.iface == name && s.done != nil
	current := s.ctx
	s.mu.Unlock()
	if same {
		return current, s.failure()
	}
	s.close()
	ctx, cancel := context.WithCancel(s.parent)
	ready, done := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.iface, s.ctx, s.cancel, s.done, s.err = name, ctx, cancel, done, nil
	s.mu.Unlock()
	go func() {
		err := capturePackets(ctx, name, func() { s.mu.Lock(); s.running = true; s.mu.Unlock(); close(ready) }, s.forward)
		if ctx.Err() == nil && err == nil {
			err = errors.New("tcpdump 意外停止，请检查 LAN 接口")
		}
		s.mu.Lock()
		s.err = err
		s.running = false
		s.mu.Unlock()
		cancel()
		close(done)
	}()
	select {
	case <-ready:
		return ctx, s.failure()
	case <-done:
		return ctx, s.failure()
	case <-s.parent.Done():
		return ctx, s.parent.Err()
	}
}

func (s *captureSession) forward(frame []byte, at time.Time) {
	if !s.flows.keep(frame) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured++
	if s.queue == nil {
		s.offline++
		return
	}
	s.queue.Enqueue(frame, 1, at)
}

func (s *captureSession) attach(q *packetQueue) {
	s.mu.Lock()
	s.queue = q
	s.mu.Unlock()
}
func (s *captureSession) detach() {
	s.mu.Lock()
	s.queue = nil
	s.mu.Unlock()
}
func (s *captureSession) stats() (Counters, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.running && s.ctx != nil && s.ctx.Err() == nil
	return Counters{Captured: s.captured, OfflineDropped: s.offline, Dropped: s.offline}, active
}
func (s *captureSession) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// A nil channel is intentional before a real capture starts (including unit
// fixtures); callers may still select on task cancellation and configuration.
func (s *captureSession) finished() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}
func (s *captureSession) close() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
