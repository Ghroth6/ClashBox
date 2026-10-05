package compat

import "sync"

// History retains short-lived connections even after they leave the core map.
// It owns the slice, not the entries; callers must use concurrency-safe entries.
type History[T any] struct {
	mu sync.Mutex
	items []T
	limit int
}

func NewHistory[T any](limit int) *History[T] {
	if limit < 1 { panic("history limit must be positive") }
	return &History[T]{limit: limit}
}
func (h *History[T]) Add(value T) {
	h.mu.Lock(); defer h.mu.Unlock()
	if len(h.items) == h.limit { copy(h.items, h.items[1:]); h.items[len(h.items)-1] = value
	} else { h.items = append(h.items, value) }
}
func (h *History[T]) Snapshot() []T {
	h.mu.Lock(); defer h.mu.Unlock()
	return append([]T{}, h.items...)
}
func (h *History[T]) Clear() { h.mu.Lock(); h.items = nil; h.mu.Unlock() }

// MessageBus never waits for an IPC consumer while inside a core callback.
// A full subscriber is disconnected rather than silently skipping messages.
type MessageBus struct {
	mu sync.Mutex
	subs map[chan string]struct{}
}
func (b *MessageBus) Subscribe() (<-chan string, func()) {
	b.mu.Lock(); defer b.mu.Unlock()
	if b.subs == nil { b.subs = map[chan string]struct{}{} }
	ch := make(chan string, 128)
	b.subs[ch] = struct{}{}
	return ch, func() {
		b.mu.Lock(); defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok { delete(b.subs, ch); close(ch) }
	}
}
func (b *MessageBus) Publish(message string) {
	b.mu.Lock(); defer b.mu.Unlock()
	for ch := range b.subs {
		select { case ch <- message: default: delete(b.subs, ch); close(ch) }
	}
}
