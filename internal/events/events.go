// Package events provides an in-process activity-log event bus with a
// Server-Sent-Events HTTP handler.
package events

import (
	"fmt"
	"sync"
	"time"
)

const (
	ringSize      = 200
	subscriberCap = 64
)

// Event is a single activity-log entry.
type Event struct {
	// Time is the UTC time the event was published.
	Time time.Time `json:"time"`
	// Level is one of "info", "success" or "error".
	Level string `json:"level"`
	// Message is the human-readable log line.
	Message string `json:"message"`
}

type bus struct {
	mu   sync.Mutex
	ring []Event
	subs map[int]chan Event
	next int
}

var defaultBus = &bus{subs: make(map[int]chan Event)}

func (b *bus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.ring) >= ringSize {
		copy(b.ring, b.ring[1:])
		b.ring = b.ring[:ringSize-1]
	}
	b.ring = append(b.ring, e)
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber is full; drop for it, never block.
		}
	}
}

// Publish records an event with the given level and message in the ring
// buffer (last 200 kept) and fans it out to all subscribers without blocking.
// Subscribers whose channel is full miss the event.
func Publish(level, message string) {
	defaultBus.publish(Event{Time: time.Now().UTC(), Level: level, Message: message})
}

// Info publishes an "info" event.
func Info(msg string) { Publish("info", msg) }

// Success publishes a "success" event.
func Success(msg string) { Publish("success", msg) }

// Errorf publishes an "error" event with a formatted message.
func Errorf(format string, a ...any) { Publish("error", fmt.Sprintf(format, a...)) }

// Recent returns a snapshot of the ring buffer, oldest first.
func Recent() []Event {
	defaultBus.mu.Lock()
	defer defaultBus.mu.Unlock()
	out := make([]Event, len(defaultBus.ring))
	copy(out, defaultBus.ring)
	return out
}

// Subscribe returns a buffered channel (capacity 64) receiving future events
// and a function that unsubscribes and closes the channel. The unsubscribe
// function is safe to call more than once.
func Subscribe() (<-chan Event, func()) {
	b := defaultBus
	ch := make(chan Event, subscriberCap)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
}
