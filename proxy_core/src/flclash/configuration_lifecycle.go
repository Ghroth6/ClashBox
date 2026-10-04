package main

import (
	"context"
	"core/compat"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/metacubex/mihomo/config"
	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
)

// Guarded by runLock. An error can retain an unfinished candidate cleanup;
// subsequent calls wait for that same operation instead of closing it again.
var pendingCandidateCleanup error

func waitCandidateCleanup(ctx context.Context) error {
	if err := config.WaitCleanup(ctx, pendingCandidateCleanup); err != nil {
		return fmt.Errorf("previous candidate cleanup has not completed: %w", err)
	}
	pendingCandidateCleanup = nil
	return nil
}

func applyConfig(rawConfig *config.RawConfig, params ConfigExtendedParams) error {
	runLock.Lock()
	defer runLock.Unlock()
	if isRunning || systemTUNActiveLocked() {
		return errors.New("stop the system VPN before replacing its configuration")
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := waitCandidateCleanup(cleanupCtx)
	cleanupCancel()
	if err != nil {
		return err
	}
	if err := stopListeners(); err != nil {
		isRunning = false
		return fmt.Errorf("stop forwarding before config replacement: %w", err)
	}
	isRunning = false
	// Cancel all owners before waiting for any of them: a manual provider Update
	// may otherwise be waiting for a Fetcher whose cancellation is still queued.
	configurationTasks.Cancel()
	currentConfig = nil // Retired tasks cannot be restarted after a failed reload.
	if err := executor.CancelConfigTasks(nil); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := configurationTasks.Wait(ctx); err != nil {
		return err
	}
	if err := executor.RetireConfig(ctx); err != nil {
		return err
	}
	// ParseRawConfig temporarily mutates core globals and can load geodata. It
	// must run after old work has ended, even for the legacy IsPatch request.
	// A deep validation failure leaves the imported file intact and requires a
	// successful reload; silently reviving retired objects would be unsafe.
	nextConfig, err := config.ParseRawConfig(rawConfig)
	if err != nil {
		pendingCandidateCleanup = err
		return err
	}
	eventIDs.Store(compat.NewEventIDs(nextConfig.Proxies, nextConfig.Providers))
	startCoreEvents()
	if err := hub.ApplyConfigContext(context.Background(), compat.ConfigWithoutProxyListeners(nextConfig)); err != nil {
		// ApplyConfigContext returns errors before publication. This candidate
		// has no other owner; preserve any incomplete cleanup in the error.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		pendingCandidateCleanup = errors.Join(err, nextConfig.Discard(cleanupCtx))
		eventIDs.Store(compat.NewEventIDs(nil, nil))
		return pendingCandidateCleanup
	}
	configParams = params
	currentConfig = nextConfig
	currentRawListeners = rawConfig.Listeners
	externalProviders = getExternalProvidersRaw()
	configurationTasks = compat.NewConfigurationTasks()
	if err := patchSelectGroup(); err != nil {
		return err
	}
	return updateListeners()
}

func handleShutdown() bool {
	compat.CancelForwarding()
	runLock.Lock()
	defer runLock.Unlock()
	isRunning = false
	stopCoreEvents()
	err := stopListeners()
	configurationTasks.Cancel()
	currentConfig = nil
	err = errors.Join(err, executor.CancelConfigTasks(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = errors.Join(err, waitCandidateCleanup(ctx), configurationTasks.Wait(ctx))
	if err != nil {
		return false
	}
	if err := executor.RetireConfig(ctx); err != nil {
		return false
	}
	executor.Shutdown()
	runtime.GC()
	isInit = false
	currentConfig = nil
	externalProviders = map[string]cp.Provider{}
	return true
}
