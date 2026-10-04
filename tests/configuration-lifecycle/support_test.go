package main

// Only platform listeners and event presentation are substituted. The production
// configuration_lifecycle.go, parser, executor, providers and task owner run as-is.
import (
	"core/compat"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/config"
	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
)

type ConfigExtendedParams struct {
	IsPatch     bool
	SelectedMap map[string]string
}

var runLock sync.Mutex
var isRunning, isInit, tunActive bool
var currentConfig *config.Config
var currentRawListeners []map[string]any
var configurationTasks *compat.ConfigurationTasks
var configParams ConfigExtendedParams
var eventIDs atomic.Value
var externalProviders = map[string]cp.Provider{}
var stopError error

func systemTUNActiveLocked() bool { return tunActive }
func stopListeners() error        { return stopError }
func updateListeners() error      { return nil }
func startCoreEvents()            {}
func stopCoreEvents()             {}
func patchSelectGroup() error     { return nil }
func getExternalProvidersRaw() map[string]cp.Provider {
	providers := make(map[string]cp.Provider)
	for name, provider := range tunnel.Providers() {
		if provider.VehicleType() != cp.Compatible {
			providers[name] = provider
		}
	}
	for name, provider := range tunnel.RuleProviders() {
		if provider.VehicleType() != cp.Compatible {
			providers[name] = provider
		}
	}
	return providers
}
