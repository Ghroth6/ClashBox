package compat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/tunnel"
)

// The embedded application serializes Start/Stop with config application. Cancel
// is independently safe while a constructor holds the application's run lock.
// A failed Stop retains the run: no later Start may forget unresolved resources.
var forwarding struct {
	sync.Mutex
	run        *forwardingRun
	sequence   uint64
	generation atomic.Uint64
}

type forwardingRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	id     uint64
	target C.Tunnel
}

func ConfigureForwarding() error {
	if err := dns.SetExternalIngressManaged(true); err != nil {
		return err
	}
	tunnel.EnableForwardingLifecycle()
	return nil
}

// PrepareForwarding freezes the target for the native TUN and proxy listeners.
// No root requests are admitted until their constructors have all succeeded.
func PrepareForwarding(parent context.Context) (C.Tunnel, error) {
	forwarding.Lock()
	if forwarding.run != nil {
		forwarding.Unlock()
		return nil, errors.New("previous forwarding run has not been stopped")
	}
	ctx, cancel := context.WithCancel(parent)
	run := &forwardingRun{ctx: ctx, cancel: cancel}
	forwarding.run = run
	forwarding.sequence++
	generation := forwarding.sequence
	run.id = generation
	forwarding.Unlock()
	// The caller owns rollback so it can choose a bounded wait and aggregate its
	// TUN/platform errors with these ingress errors. Keep the record on failure.
	if err := tunnel.PrepareForwarding(ctx, generation); err != nil {
		return nil, err
	}
	target, err := tunnel.BoundForwardingTunnel(generation)
	if err != nil {
		return nil, err
	}
	run.target = target
	return target, nil
}

func StartForwarding(parent context.Context, cfg *config.Config) error {
	if _, err := PrepareForwarding(parent); err != nil {
		return err
	}
	return StartPreparedForwarding(cfg)
}

// StartPreparedForwarding completes a run already prepared for its system TUN.
func StartPreparedForwarding(cfg *config.Config) (err error) {
	forwarding.Lock()
	run := forwarding.run
	forwarding.Unlock()
	if run == nil || run.target == nil {
		return errors.New("forwarding run is not prepared")
	}
	if err = run.ctx.Err(); err != nil {
		return err
	}
	if err = StartProxyListenersWithTunnel(cfg, run.target); err != nil {
		return err
	}
	if err = dns.PrepareExternalIngress(run.ctx); err != nil {
		return err
	}
	if err = tunnel.ActivateForwarding(run.id); err != nil {
		return err
	}
	if err = dns.ActivateExternalIngress(); err != nil {
		return err
	}
	forwarding.Lock()
	defer forwarding.Unlock()
	if err = run.ctx.Err(); err != nil {
		return err
	}
	forwarding.generation.Store(run.id)
	return nil
}

func CancelForwarding() {
	forwarding.Lock()
	forwarding.generation.Store(0)
	if forwarding.run != nil {
		forwarding.run.cancel()
	}
	forwarding.Unlock()
	tunnel.CancelForwarding()
}

func StopForwarding(ctx context.Context) error {
	CancelForwarding()
	// Close ingress before waiting for admitted requests; do every cleanup even
	// when another fails, and retain the record until all have succeeded.
	listenerErr := StopProxyListeners()
	dnsErr := dns.StopExternalIngress(ctx)
	requestErr := tunnel.StopForwarding(ctx)
	err := errors.Join(listenerErr, dnsErr, requestErr)
	if err == nil {
		forwarding.Lock()
		forwarding.run = nil
		forwarding.Unlock()
	}
	return err
}

// Zero denotes stopped/transitioning. Management trackers have no forwarding
// generation; stale forwarding callbacks must not enter a newer run's history.
func ForwardingGeneration() uint64 { return forwarding.generation.Load() }
