package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/compat"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/configresources"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
)

func fixture(t *testing.T) *config.RawConfig {
	t.Helper()
	raw, err := config.UnmarshalRawConfig([]byte("mode: direct\nprofile:\n  store-selected: false\n  store-fake-ip: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func reset(t *testing.T) {
	t.Helper()
	oldHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	currentConfig = nil
	configurationTasks = nil
	pendingCandidateCleanup = nil
	isRunning, tunActive, stopError = false, false, nil
	if err := compat.ConfigureForwarding(); err != nil {
		t.Fatal(err)
	}
	compat.ConfigureEmbeddedController()
	t.Cleanup(func() {
		configurationTasks.Cancel()
		_ = executor.RetireConfig(context.Background())
		C.SetHomeDir(oldHome)
		pendingCandidateCleanup = nil
	})
}

type candidateCloseProbe struct {
	*outbound.Base
	started chan struct{}
	release chan struct{}
	err     error
	calls   atomic.Int32
}

func (p *candidateCloseProbe) Close() error {
	p.calls.Add(1)
	close(p.started)
	if p.release != nil {
		<-p.release
	}
	return p.err
}

func TestUnfinishedCandidateCleanupBlocksReloadAndCanContinueWaiting(t *testing.T) {
	reset(t)
	probe := &candidateCloseProbe{Base: outbound.NewBase(outbound.BaseOption{Name: "candidate", Type: C.Direct}), started: make(chan struct{}), release: make(chan struct{})}
	owned := &configresources.Set{}
	owned.AddAdapter(probe)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pendingCandidateCleanup = errors.Join(errors.New("invalid candidate"), owned.Close(ctx))
	<-probe.started
	result := make(chan error, 1)
	raw := fixture(t)
	go func() { result <- applyConfig(raw, ConfigExtendedParams{}) }()
	select {
	case err := <-result:
		t.Fatalf("reload skipped pending cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(probe.release)
	if err := <-result; err != nil {
		t.Fatalf("cleanup completion did not permit reload: %v", err)
	}
	if probe.calls.Load() != 1 || pendingCandidateCleanup != nil || currentConfig == nil {
		t.Fatal("cleanup retried Close or candidate not published")
	}
}

func TestCandidateCloseFailureRemainsBlockingForReloadAndShutdown(t *testing.T) {
	reset(t)
	closeErr := errors.New("candidate socket close failed")
	probe := &candidateCloseProbe{Base: outbound.NewBase(outbound.BaseOption{Name: "candidate", Type: C.Direct}), started: make(chan struct{}), err: closeErr}
	owned := &configresources.Set{}
	owned.AddAdapter(probe)
	pendingCandidateCleanup = errors.Join(errors.New("invalid candidate"), owned.Close(context.Background()))
	for attempt := 0; attempt < 2; attempt++ {
		if err := applyConfig(fixture(t), ConfigExtendedParams{}); !errors.Is(err, closeErr) {
			t.Fatalf("lost cleanup failure: %v", err)
		}
	}
	if handleShutdown() {
		t.Fatal("shutdown ignored candidate cleanup failure")
	}
	if probe.calls.Load() != 1 || currentConfig != nil {
		t.Fatal("failed close retried or configuration activated")
	}
}

func TestReplacementWaitsForOldRequestBeforeParsing(t *testing.T) {
	reset(t)
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatal(err)
	}
	old := configurationTasks
	ctx, finish, err := old.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	result := make(chan error, 1)
	go func() { result <- applyConfig(fixture(t), ConfigExtendedParams{IsPatch: true}) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("old request not cancelled")
	}
	select {
	case err := <-result:
		t.Fatalf("replacement passed an unfinished request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if configurationTasks == old || !configurationTasks.Active() {
		t.Fatal("replacement did not create a new owner")
	}
	if _, _, err := old.Acquire(); err == nil {
		t.Fatal("old owner was revived")
	}
}

func TestDeepParseFailureLeavesStoppedAndCanReload(t *testing.T) {
	reset(t)
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatal(err)
	}
	old := configurationTasks
	invalid := fixture(t)
	invalid.Rule = []string{"MATCH,missing-proxy"}
	if err := applyConfig(invalid, ConfigExtendedParams{}); err == nil {
		t.Fatal("invalid rule accepted")
	}
	if currentConfig != nil || old.Active() || len(tunnel.Proxies()) != 0 {
		t.Fatal("failed reload revived retired configuration")
	}
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatalf("valid retry failed: %v", err)
	}
}

func TestActiveVPNRejectsReplacementWithoutRetiringManagement(t *testing.T) {
	reset(t)
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatal(err)
	}
	old := configurationTasks
	tunActive = true
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err == nil {
		t.Fatal("active VPN replaced")
	}
	if configurationTasks != old || !old.Active() || currentConfig == nil {
		t.Fatal("rejected request retired the active config")
	}
	tunActive = false
}

type closeProbe struct {
	*outbound.Base
	closes atomic.Int32
}

func (p *closeProbe) Close() error { p.closes.Add(1); return nil }

func TestShutdownTimeoutKeepsPoolsUntilRequestFinishes(t *testing.T) {
	reset(t)
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatal(err)
	}
	probe := &closeProbe{Base: outbound.NewBase(outbound.BaseOption{Name: "probe", Type: C.Direct})}
	proxies := tunnel.Proxies()
	proxies["probe"] = adapter.NewProxy(probe)
	_, finish, err := configurationTasks.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if handleShutdown() {
		t.Fatal("shutdown reported success with pending callback")
	}
	if probe.closes.Load() != 0 || currentConfig != nil || configurationTasks.Active() {
		t.Fatal("timeout closed pool or revived retired state")
	}
	finish()
	if !handleShutdown() {
		t.Fatal("shutdown retry failed")
	}
	if probe.closes.Load() != 1 {
		t.Fatalf("pool closed %d times", probe.closes.Load())
	}
}

func TestListenerFailurePreventsReplacement(t *testing.T) {
	reset(t)
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err != nil {
		t.Fatal(err)
	}
	old := configurationTasks
	stopError = errors.New("listener close failed")
	if err := applyConfig(fixture(t), ConfigExtendedParams{}); err == nil {
		t.Fatal("ignored listener failure")
	}
	if configurationTasks != old || !old.Active() {
		t.Fatal("retired config before listener cleanup")
	}
}
