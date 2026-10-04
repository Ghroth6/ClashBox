package compat

import (
	"errors"
	"fmt"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/tunnel"
)

// ConfigWithoutProxyListeners is an execution view, not an edited profile.
// The core applies listeners even while the app is stopped. Keep its resolver,
// controller and provider configuration, but do not open proxy ingress there.
// Those services still have a configuration/process lifetime; this is not a
// network shutdown or a promise to cancel their background work.
func ConfigWithoutProxyListeners(cfg *config.Config) *config.Config {
	runtime := *cfg
	general := *cfg.General
	runtime.General = &general
	general.Port = 0
	general.SocksPort = 0
	general.RedirPort = 0
	general.TProxyPort = 0
	general.MixedPort = 0
	general.ShadowSocksConfig = ""
	general.VmessConfig = ""
	general.TuicServer = LC.TuicServer{}
	// ReCreateTun sorts slice fields in place, including disabled configurations.
	// An empty value also keeps it away from the preserved system TUN options.
	general.Tun = LC.Tun{}
	runtime.Listeners = nil
	runtime.Tunnels = nil
	return &runtime
}

// FreshProxyListeners avoids reusing closed upstream named listener objects:
// their Listen methods append handles and their Close methods retain them.
// Decode everything before opening sockets so a parse error cannot partly start.
func FreshProxyListeners(cfg *config.Config, raw []map[string]any) (*config.Config, error) {
	runtime := *cfg
	runtime.Listeners = make(map[string]C.InboundListener, len(raw))
	for i, mapping := range raw {
		entry, err := listener.ParseListener(mapping)
		if err != nil {
			return nil, fmt.Errorf("listener %d: %w", i, err)
		}
		if _, exists := runtime.Listeners[entry.Name()]; exists {
			return nil, fmt.Errorf("duplicate listener %q", entry.Name())
		}
		runtime.Listeners[entry.Name()] = entry
	}
	return &runtime, nil
}

// StartProxyListeners restores the core-managed proxy ingress only. The caller
// serializes this with config application and StopProxyListeners. Any actual
// bind failure closes the complete proxy ingress set before returning an error.
func StartProxyListeners(cfg *config.Config) error {
	g := cfg.General
	listener.SetAllowLan(g.AllowLan)
	inbound.SetSkipAuthPrefixes(g.SkipAuthPrefixes)
	inbound.SetAllowedIPs(g.LanAllowedIPs)
	inbound.SetDisAllowedIPs(g.LanDisAllowedIPs)
	listener.SetBindAddress(g.BindAddress)
	steps := []struct {
		name  string
		start func() error
	}{
		{"named", func() error { return listener.PatchInboundListeners(cfg.Listeners, tunnel.Tunnel, true) }},
		{"http", func() error { return listener.ReCreateHTTP(g.Port, tunnel.Tunnel) }},
		{"socks", func() error { return listener.ReCreateSocks(g.SocksPort, tunnel.Tunnel) }},
		{"redir", func() error { return listener.ReCreateRedir(g.RedirPort, tunnel.Tunnel) }},
		{"tproxy", func() error { return listener.ReCreateTProxy(g.TProxyPort, tunnel.Tunnel) }},
		{"mixed", func() error { return listener.ReCreateMixed(g.MixedPort, tunnel.Tunnel) }},
		{"shadowsocks", func() error { return listener.ReCreateShadowSocks(g.ShadowSocksConfig, tunnel.Tunnel) }},
		{"vmess", func() error { return listener.ReCreateVmess(g.VmessConfig, tunnel.Tunnel) }},
		{"tuic", func() error { return listener.ReCreateTuic(g.TuicServer, tunnel.Tunnel) }},
		{"tunnels", func() error { return listener.PatchTunnel(cfg.Tunnels, tunnel.Tunnel) }},
	}
	for _, step := range steps {
		if err := step.start(); err != nil {
			return errors.Join(fmt.Errorf("start %s listeners: %w", step.name, err), StopProxyListeners())
		}
	}
	return nil
}

// StopProxyListeners closes registered named, legacy and tunnel listeners. It
// deliberately leaves the wrapper-owned system TUN, DNS and controller alone.
// Every close is attempted, including after an earlier close reports an error.
func StopProxyListeners() error {
	return errors.Join(
		listener.PatchInboundListeners(nil, tunnel.Tunnel, true),
		listener.PatchTunnel(nil, tunnel.Tunnel),
		listener.ReCreateHTTP(0, tunnel.Tunnel),
		listener.ReCreateSocks(0, tunnel.Tunnel),
		listener.ReCreateRedir(0, tunnel.Tunnel),
		listener.ReCreateTProxy(0, tunnel.Tunnel),
		listener.ReCreateMixed(0, tunnel.Tunnel),
		listener.ReCreateShadowSocks("", tunnel.Tunnel),
		listener.ReCreateVmess("", tunnel.Tunnel),
		listener.ReCreateTuic(LC.TuicServer{}, tunnel.Tunnel),
	)
}
