package main

import (
	"context"
	"core/compat"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/batch"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/listener"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/log"
	rp "github.com/metacubex/mihomo/rules/provider"
	"github.com/metacubex/mihomo/tunnel"
)

var (
	errNotExternalProvider = errors.New("not external provider")
	isRunning              = false
	runLock                sync.Mutex
	b, _                   = batch.New[bool](context.Background(), batch.WithConcurrencyNum[bool](50))
)

func init() {
	compat.ConfigureEmbeddedController()
	if err := compat.ConfigureForwarding(); err != nil {
		panic(err)
	}
}

type ExternalProviders []ExternalProvider

func (a ExternalProviders) Len() int           { return len(a) }
func (a ExternalProviders) Less(i, j int) bool { return a[i].Name < a[j].Name }
func (a ExternalProviders) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func (message *Message) Json() (string, error) {
	data, err := json.Marshal(message)
	return string(data), err
}

func readFile(path string) ([]byte, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return data, err
}

func getProfilePath(id string) string {
	return filepath.Join(constant.Path.HomeDir(), "profiles", id+".yaml")
}

func getProfileProvidersPath(id string) string {
	return filepath.Join(constant.Path.HomeDir(), "providers", id)
}

func getRawConfigWithId(id string) (*config.RawConfig, error) {
	path := getProfilePath(id)
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile cannot be read: %w", err)
	}
	prof, err := config.UnmarshalRawConfig(data)
	if err != nil {
		return nil, fmt.Errorf("profile YAML is invalid: %w", err)
	}
	base := filepath.Dir(path)
	if err = resolveProviderResources(base, prof.ProxyProvider); err != nil {
		return nil, err
	}
	if err = resolveProviderResources(base, prof.RuleProvider); err != nil {
		return nil, err
	}
	return prof, nil
}

func getExternalProvidersRaw() map[string]cp.Provider {
	eps := make(map[string]cp.Provider)
	for n, p := range tunnel.Providers() {
		if p.VehicleType() != cp.Compatible {
			eps[n] = p
		}
	}
	for n, p := range tunnel.RuleProviders() {
		if p.VehicleType() != cp.Compatible {
			eps[n] = p
		}
	}
	return eps
}

func toExternalProvider(p cp.Provider) (*ExternalProvider, error) {
	switch p.(type) {
	case *provider.ProxySetProvider:
		psp := p.(*provider.ProxySetProvider)
		if psp == nil {
			return nil, errors.New("nil proxy provider")
		}
		info, err := compat.SubscriptionInfo(psp)
		if err != nil {
			return nil, err
		}
		return &ExternalProvider{
			Name:             psp.Name(),
			Type:             psp.Type().String(),
			VehicleType:      psp.VehicleType().String(),
			Count:            psp.Count(),
			UpdateAt:         psp.UpdatedAt(),
			Path:             psp.Vehicle().Path(),
			SubscriptionInfo: info,
		}, nil
	case *rp.RuleSetProvider:
		rsp := p.(*rp.RuleSetProvider)
		return &ExternalProvider{
			Name:        rsp.Name(),
			Type:        rsp.Type().String(),
			VehicleType: rsp.VehicleType().String(),
			Count:       rsp.Count(),
			UpdateAt:    rsp.UpdatedAt(),
			Path:        rsp.Vehicle().Path(),
		}, nil
	default:
		return nil, errNotExternalProvider
	}
}

func sideUpdateExternalProvider(p cp.Provider, bytes []byte) error {
	return compat.SideUpdateProvider(p, bytes)
}

func decorationConfig(profileId string, cfg config.RawConfig) (*config.RawConfig, error) {
	prof, err := getRawConfigWithId(profileId)
	if err != nil {
		return nil, err
	}
	if err := overwriteConfig(prof, cfg); err != nil {
		return nil, err
	}
	return prof, nil
}

// Full-profile mode. App preferences must not rewrite DNS, rules, URLs, groups,
// providers, hosts, protocol options, mode, sniffer or tunnels.
func overwriteConfig(targetConfig *config.RawConfig, _ config.RawConfig) error {
	if !targetConfig.DNS.Enable {
		return errors.New("VPN profile requires dns.enable: true; imported DNS was not changed")
	}
	if targetConfig.IPTables.Enable {
		return errors.New("VPN profile cannot enable iptables; the system VPN owns routing")
	}
	for _, inbound := range targetConfig.Listeners {
		if inbound["type"] == "tun" {
			return errors.New("VPN profile cannot create a named TUN listener; the system VPN owns TUN")
		}
	}
	// The system VPN Extension owns routes and the TUN file descriptor.
	targetConfig.Tun.Enable = false
	targetConfig.Tun.AutoRoute = false
	targetConfig.Tun.AutoDetectInterface = false
	return nil
}

func updateListeners() (err error) {
	if !isRunning || currentConfig == nil {
		return nil
	}
	defer func() {
		if err != nil {
			isRunning = false
			err = errors.Join(err, stopListeners())
		}
	}()
	if !systemTUNReadyLocked() {
		return errors.New("system TUN is not ready for proxy listeners")
	}
	runtime, err := compat.FreshProxyListeners(currentConfig, currentRawListeners)
	if err != nil {
		return err
	}
	if systemOwnsTUN {
		err = compat.StartPreparedForwarding(runtime)
	} else {
		err = compat.StartForwarding(context.Background(), runtime)
	}
	if err != nil {
		return err
	}
	if !systemOwnsTUN {
		listener.ReCreateTun(currentConfig.General.Tun, tunnel.Tunnel)
	}
	return nil
}

func stopListeners() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := compat.StopForwarding(ctx)
	if err != nil {
		log.Errorln("Stop forwarding: %s", err)
	}
	if !systemOwnsTUN {
		listener.ReCreateTun(LC.Tun{}, tunnel.Tunnel)
	}
	return err
}

func patchSelectGroup() error {
	mapping := configParams.SelectedMap
	if mapping == nil {
		return nil
	}
	catalog, err := compat.NewProxyCatalog(tunnel.Proxies(), tunnel.Providers())
	if err != nil {
		return err
	}
	for name, selected := range mapping {
		if err := catalog.Select(name, selected); err != nil {
			return err
		}
	}
	return nil
}
