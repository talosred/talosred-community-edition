package store

import "sync"

// Broadcaster fans out RequestLog events to all subscribed SSE clients.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[chan *RequestLog]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[chan *RequestLog]struct{})}
}

// Subscribe returns a channel that receives each new RequestLog and a cancel
// func that must be called when the subscriber disconnects.
func (b *Broadcaster) Subscribe() (<-chan *RequestLog, func()) {
	ch := make(chan *RequestLog, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	// cancel removes the subscription. Publish is non-blocking (it uses a
	// select/default and skips full buffers), so there's nothing to drain —
	// once unsubscribed, Publish never sends to ch again.
	cancel := func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish sends r to all current subscribers. Non-blocking: slow subscribers
// that have a full buffer are skipped.
func (b *Broadcaster) Publish(r *RequestLog) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- r:
		default:
		}
	}
}

func (b *Broadcaster) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
