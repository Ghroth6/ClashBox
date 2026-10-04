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
	isRunning, tunActive, stopError = false, false, nil
	if err := compat.ConfigureForwarding(); err != nil {
		t.Fatal(err)
	}
	compat.ConfigureEmbeddedController()
	t.Cleanup(func() {
		configurationTasks.Cancel()
		_ = executor.RetireConfig(context.Background())
		C.SetHomeDir(oldHome)
	})
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
