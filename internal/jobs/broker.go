package jobs

import "sync"

// Event is pushed to SSE clients and the Telegram notifier.
type Event struct {
	Type     string `json:"type"` // job | log | server
	JobID    uint   `json:"job_id,omitempty"`
	ServerID *uint  `json:"server_id,omitempty"`
	Data     any    `json:"data"`
}

// Broker fans events out to subscribers. A slow subscriber drops events
// rather than stalling the jobs that publish them.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

func (b *Broker) Subscribe() chan Event {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
