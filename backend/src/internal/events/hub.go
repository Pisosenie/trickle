package events

import "sync"

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

const TorrentCompleted = "torrent_completed"

const subscriberBuffer = 32

type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[chan Event]struct{}{}}
}

func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
