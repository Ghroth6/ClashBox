package main

import (
	"context"
	"strconv"
	"sync"
)

// A session is issued for the native generation captured by ArkTS before VPN
// preparation, and checked again before the IPC request is read and dispatched.
// Cancelling does not wait for its constructor or for an ArkTS protect ACK.
type tunSession struct {
	lifecycle  *tunLifecycle
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	done       <-chan struct{}
	request    func(Fd)
}

type tunLifecycle struct {
	mu                 sync.Mutex
	generation         uint64
	sessions           map[*tunSession]struct{}
	current            *tunSession
	ready              bool
	protectionRequired bool
}

var tunSessions tunLifecycle

func (l *tunLifecycle) StartToken() string { return strconv.FormatUint(l.Generation(), 10) }

func (l *tunLifecycle) SessionForToken(token string, receivedGeneration uint64) *tunSession {
	generation, err := strconv.ParseUint(token, 10, 64)
	if err != nil || generation != receivedGeneration {
		return nil
	}
	return l.NewSession(generation)
}

func (l *tunLifecycle) Generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generation
}

func (l *tunLifecycle) NewSession(generation uint64) *tunSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	s := &tunSession{lifecycle: l, generation: generation, ctx: ctx, cancel: cancel, done: ctx.Done()}
	if generation != l.generation {
		cancel()
		return s
	}
	if l.sessions == nil {
		l.sessions = make(map[*tunSession]struct{})
	}
	l.sessions[s] = struct{}{}
	return s
}

func (s *tunSession) Valid() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (l *tunLifecycle) Reserve(s *tunSession, request func(Fd)) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s == nil || s.lifecycle != l || !s.Valid() || s.generation != l.generation || l.current != nil || request == nil {
		return false
	}
	s.request = request
	l.current = s
	l.ready = false
	l.protectionRequired = true
	return true
}

func (l *tunLifecycle) Commit(s *tunSession) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != s || !s.Valid() {
		return false
	}
	l.ready = true
	return true
}

func (l *tunLifecycle) Ready() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ready && l.current.Valid()
}

// Before the first TUN, providers/geodata may bootstrap over the ordinary
// network. After reservation, only an explicitly protected session is safe:
// native Stop does not confirm that ArkTS destroyed the system VPN routes.
func (l *tunLifecycle) ProtectionRequired() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.protectionRequired
}

func (l *tunLifecycle) cancelLocked(s *tunSession) {
	if s == nil || s.lifecycle != l {
		return
	}
	if _, ok := l.sessions[s]; ok {
		delete(l.sessions, s)
		s.cancel()
	}
	if l.current == s {
		l.current = nil
		l.ready = false
	}
}

func (l *tunLifecycle) Cancel(s *tunSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelLocked(s)
}

func (l *tunLifecycle) CancelAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	for s := range l.sessions {
		l.cancelLocked(s)
	}
}
