package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/batch"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/constant/features"
	cp "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/route"
	"github.com/metacubex/mihomo/listener"
	"github.com/metacubex/mihomo/log"
	rp "github.com/metacubex/mihomo/rules/provider"
	"github.com/metacubex/mihomo/tunnel"
)

var (
	isRunning = false
	runLock   sync.Mutex
	b, _      = batch.New[bool](context.Background(), batch.WithConcurrencyNum[bool](50))
)

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
		return &ExternalProvider{
			Name:             psp.Name(),
			Type:             psp.Type().String(),
			VehicleType:      psp.VehicleType().String(),
			Count:            psp.Count(),
			UpdateAt:         psp.UpdatedAt(),
			Path:             psp.Vehicle().Path(),
			SubscriptionInfo: psp.GetSubscriptionInfo(),
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
		return nil, errors.New("not external provider")
	}
}

func sideUpdateExternalProvider(p cp.Provider, bytes []byte) error {
	switch p.(type) {
	case *provider.ProxySetProvider:
		psp := p.(*provider.ProxySetProvider)
		_, _, err := psp.SideUpdate(bytes)
		if err == nil {
			return err
		}
		return nil
	case rp.RuleSetProvider:
		rsp := p.(*rp.RuleSetProvider)
		_, _, err := rsp.SideUpdate(bytes)
		if err == nil {
			return err
		}
		return nil
	default:
		return errors.New("not external provider")
	}
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
	// The system VPN Extension owns routes and the TUN file descriptor.
	targetConfig.Tun.Enable = false
	targetConfig.Tun.AutoRoute = false
	targetConfig.Tun.AutoDetectInterface = false
	return nil
}

func patchConfig() {
	log.Infoln("[Apply] patch")
	general := currentConfig.General
	controller := currentConfig.Controller
	tls := currentConfig.TLS
	tunnel.SetSniffing(general.Sniffing)
	tunnel.SetFindProcessMode(general.FindProcessMode)
	dialer.SetTcpConcurrent(general.TCPConcurrent)
	dialer.DefaultInterface.Store(general.Interface)
	adapter.UnifiedDelay.Store(general.UnifiedDelay)
	tunnel.SetMode(general.Mode)
	log.SetLevel(general.LogLevel)
	resolver.DisableIPv6 = !general.IPv6

	route.ReCreateServer(&route.Config{
		Addr:        controller.ExternalController,
		TLSAddr:     controller.ExternalControllerTLS,
		UnixAddr:    controller.ExternalControllerUnix,
		PipeAddr:    controller.ExternalControllerPipe,
		Secret:      controller.Secret,
		Certificate: tls.Certificate,
		PrivateKey:  tls.PrivateKey,
		DohServer:   controller.ExternalDohServer,
		IsDebug:     false,
		Cors: route.Cors{
			AllowOrigins:        controller.Cors.AllowOrigins,
			AllowPrivateNetwork: controller.Cors.AllowPrivateNetwork,
		},
	})
}

func updateListeners(force bool) {
	if !isRunning || currentConfig == nil {
		return
	}
	general := currentConfig.General
	listeners := currentConfig.Listeners
	if force == true {
		stopListeners()
	}
	listener.PatchInboundListeners(listeners, tunnel.Tunnel, true)
	listener.SetAllowLan(general.AllowLan)
	inbound.SetSkipAuthPrefixes(general.SkipAuthPrefixes)
	inbound.SetAllowedIPs(general.LanAllowedIPs)
	inbound.SetDisAllowedIPs(general.LanDisAllowedIPs)
	listener.SetBindAddress(general.BindAddress)
	listener.ReCreateHTTP(general.Port, tunnel.Tunnel)
	listener.ReCreateSocks(general.SocksPort, tunnel.Tunnel)
	listener.ReCreateRedir(general.RedirPort, tunnel.Tunnel)
	listener.ReCreateTProxy(general.TProxyPort, tunnel.Tunnel)
	listener.ReCreateMixed(general.MixedPort, tunnel.Tunnel)
	listener.ReCreateShadowSocks(general.ShadowSocksConfig, tunnel.Tunnel)
	listener.ReCreateVmess(general.VmessConfig, tunnel.Tunnel)
	listener.ReCreateTuic(general.TuicServer, tunnel.Tunnel)
	if !features.Android && !features.OHOS {
		listener.ReCreateTun(general.Tun, tunnel.Tunnel)
	}
}

func stopListeners() {
	listener.StopListener()
}

func patchSelectGroup() {
	mapping := configParams.SelectedMap
	if mapping == nil {
		return
	}
	for name, proxy := range tunnel.ProxiesWithProviders() {
		outbound, ok := proxy.(*adapter.Proxy)
		if !ok {
			continue
		}

		selector, ok := outbound.ProxyAdapter.(outboundgroup.SelectAble)
		if !ok {
			continue
		}

		selected, exist := mapping[name]
		if !exist {
			continue
		}

		selector.ForceSet(selected)
	}
}

func applyConfig(rawConfig *config.RawConfig) error {
	runLock.Lock()
	defer runLock.Unlock()
	nextConfig, err := config.ParseRawConfig(rawConfig)
	if err != nil {
		return err // Keep the previous config; never apply a broad default on failure.
	}
	currentConfig = nextConfig
	if configParams.IsPatch {
		patchConfig()
	} else {
		handleCloseConnectionsUnLock()
		runtime.GC()
		hub.ApplyConfig(currentConfig)
		patchSelectGroup()
	}
	updateListeners(false)
	return err
}
