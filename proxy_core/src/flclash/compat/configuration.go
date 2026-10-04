package compat

import (
	"context"
	"fmt"
	"sync"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/resource"
)

// ConfigurationTasks owns application requests that read or update the active
// configuration. A failed retirement retains this object for a later Wait;
// proxy Stop never cancels it. The application serializes replacement separately.
type ConfigurationTasks struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	pending int
	retired bool
	done    chan struct{}
}

func NewConfigurationTasks() *ConfigurationTasks {
	ctx, cancel := context.WithCancel(context.Background())
	t := &ConfigurationTasks{cancel: cancel, done: make(chan struct{})}
	t.ctx = resource.WithCommitGuard(ctx, t.commit)
	t.ctx = adapter.WithURLTestCommitGuard(t.ctx, func(action func()) bool {
		return t.commit(func() error { action(); return nil }) == nil
	})
	return t
}

func (t *ConfigurationTasks) Acquire() (context.Context, func(), error) {
	if t == nil {
		return nil, nil, fmt.Errorf("configuration is not loaded")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		return nil, nil, fmt.Errorf("previous configuration is retiring")
	}
	t.pending++
	var once sync.Once
	return t.ctx, func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.pending--
			if t.retired && t.pending == 0 {
				close(t.done)
			}
		})
	}, nil
}

func (t *ConfigurationTasks) Active() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.retired
}

func (t *ConfigurationTasks) commit(action func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		return context.Canceled
	}
	return action()
}

func (t *ConfigurationTasks) Cancel() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		return
	}
	t.retired = true
	t.cancel()
	if t.pending == 0 {
		close(t.done)
	}
}

func (t *ConfigurationTasks) Wait(ctx context.Context) error {
	if t == nil {
		return nil
	}
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("configuration requests have not finished: %w", ctx.Err())
	}
}
