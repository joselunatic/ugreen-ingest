package ingest

import (
	"sync"

	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
)

type EventType string

const (
	EventIngestUpdated EventType = "ingest_updated"
	EventProgress      EventType = "progress"
)

// Event is what gets pushed to SSE subscribers. Exactly one of Ingest /
// Progress is set, depending on Type.
type Event struct {
	Type     EventType       `json:"type"`
	Ingest   *model.Ingest   `json:"ingest,omitempty"`
	Progress *model.Progress `json:"progress,omitempty"`
}

// broker is a minimal in-memory pub/sub so the HTTP layer can turn Manager
// activity into an SSE stream without the ingest package knowing anything
// about HTTP.
type broker struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

func newBroker() *broker {
	return &broker{subs: make(map[int]chan Event)}
}

func (b *broker) subscribe() (int, <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Event, 32)
	b.subs[id] = ch
	return id, ch
}

func (b *broker) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		close(ch)
		delete(b.subs, id)
	}
}

func (b *broker) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			// Slow subscriber: drop the event rather than block ingest
			// processing. The next full ingest_updated reload catches it up.
		}
	}
}
