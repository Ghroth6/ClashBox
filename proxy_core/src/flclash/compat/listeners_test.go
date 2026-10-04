package compat

import (
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/listener"
	LC "github.com/metacubex/mihomo/listener/config"
	LI "github.com/metacubex/mihomo/listener/inbound"
)

func TestConfigWithoutProxyListenersPreservesProfileAndServices(t *testing.T) {
	cfg := &config.Config{
		General: &config.General{Inbound: config.Inbound{
			Port: 12001, SocksPort: 12002, RedirPort: 12003, TProxyPort: 12004, MixedPort: 12005,
			ShadowSocksConfig: "ss", VmessConfig: "vmess", TuicServer: LC.TuicServer{Enable: true},
			Tun:            LC.Tun{Enable: true, DNSHijack: []string{"z:53", "a:53"}},
			Authentication: []string{"test:password"}, AllowLan: true, BindAddress: "127.0.0.1", InboundTfo: true,
		}},
		DNS:        &config.DNS{Enable: true, Listen: "127.0.0.1:15353"},
		Controller: &config.Controller{ExternalController: "127.0.0.1:19090"},
		Listeners:  map[string]C.InboundListener{"unstarted": nil},
		Tunnels:    []LC.Tunnel{{Address: "127.0.0.1:19999"}},
	}
	original := *cfg.General
	runtime := ConfigWithoutProxyListeners(cfg)
	g := runtime.General
	if runtime == cfg || g == cfg.General {
		t.Fatal("execution config aliases the original mutable structs")
	}
	if g.Port != 0 || g.SocksPort != 0 || g.RedirPort != 0 || g.TProxyPort != 0 || g.MixedPort != 0 ||
		g.ShadowSocksConfig != "" || g.VmessConfig != "" || g.TuicServer.Enable || g.Tun.Enable ||
		len(runtime.Listeners) != 0 || len(runtime.Tunnels) != 0 {
		t.Fatal("execution config still requests proxy ingress")
	}
	// The actual upstream entry sorts TUN slices even when disabled.
	listener.ReCreateTun(g.Tun, nil)
	if !reflect.DeepEqual(original, *cfg.General) || len(cfg.Listeners) != 1 || len(cfg.Tunnels) != 1 ||
		cfg.General.Tun.DNSHijack[0] != "z:53" {
		t.Fatal("preserved configuration was changed")
	}
	if !g.AllowLan || !g.InboundTfo || g.BindAddress != "127.0.0.1" || g.Authentication[0] != "test:password" {
		t.Fatal("non-listener inbound policy was lost")
	}
	if runtime.DNS != cfg.DNS || runtime.Controller != cfg.Controller {
		t.Fatal("DNS/controller configuration lifetime changed")
	}
}

type testPort struct {
	addr string
	port int
}

func reserveProxyPort(t *testing.T) testPort {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := tcp.Addr().String()
		udp, err := net.ListenPacket("udp", addr)
		if err != nil {
			tcp.Close()
			continue
		}
		_, port, _ := net.SplitHostPort(addr)
		n, _ := strconv.Atoi(port)
		tcp.Close()
		udp.Close()
		return testPort{addr, n}
	}
	t.Fatal("no free TCP/UDP pair")
	return testPort{}
}

func assertPortFree(t *testing.T, network, addr string) {
	t.Helper()
	if network == "tcp" {
		l, err := net.Listen(network, addr)
		if err != nil {
			t.Fatalf("%s %s not released: %v", network, addr, err)
		}
		l.Close()
	} else {
		l, err := net.ListenPacket(network, addr)
		if err != nil {
			t.Fatalf("%s %s not released: %v", network, addr, err)
		}
		l.Close()
	}
}

func assertPortOpen(t *testing.T, network, addr string) {
	t.Helper()
	if network == "tcp" {
		conn, err := net.DialTimeout(network, addr, time.Second)
		if err != nil {
			t.Fatalf("%s %s not accepting: %v", network, addr, err)
		}
		conn.Close()
	} else {
		l, err := net.ListenPacket(network, addr)
		if err == nil {
			l.Close()
			t.Fatalf("%s %s not bound", network, addr)
		}
	}
}

func TestProxyListenersStoppedApplyAndRepeatedRestore(t *testing.T) {
	// No remote providers, NTP, geodata rules, DNS listener or controller; every
	// socket in this test is loopback. The real executor still initializes DNS.
	cfg, err := config.Parse([]byte("dns:\n  enable: true\n  nameserver: [127.0.0.1:9]\nrules:\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	http, socks, mixed, named, tun := reserveProxyPort(t), reserveProxyPort(t), reserveProxyPort(t), reserveProxyPort(t), reserveProxyPort(t)
	namedListener, err := LI.NewHTTP(&LI.HTTPOption{BaseOption: LI.BaseOption{NameStr: "test-http", Listen: "127.0.0.1", Port: strconv.Itoa(named.port)}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.Port, cfg.General.SocksPort, cfg.General.MixedPort = http.port, socks.port, mixed.port
	cfg.General.AllowLan, cfg.General.BindAddress = false, "127.0.0.1"
	cfg.General.Tun.DNSHijack = []string{"z:53", "a:53"}
	cfg.Listeners = map[string]C.InboundListener{"test-http": namedListener}
	rawListeners := []map[string]any{{"type": "http", "name": "test-http", "listen": "127.0.0.1", "port": named.port}}
	cfg.Tunnels = []LC.Tunnel{{Network: []string{"tcp", "udp"}, Address: tun.addr, Target: "127.0.0.1:9", Proxy: "DIRECT"}}
	t.Cleanup(StopProxyListeners)
	ConfigureEmbeddedController()
	for cycle := 0; cycle < 3; cycle++ {
		// All entry points must be absent directly after real config application.
		hub.ApplyConfig(ConfigWithoutProxyListeners(cfg))
		for _, p := range []testPort{http, socks, mixed, named, tun} {
			assertPortFree(t, "tcp", p.addr)
		}
		for _, p := range []testPort{socks, mixed, tun} {
			assertPortFree(t, "udp", p.addr)
		}
		if cfg.General.MixedPort != mixed.port || cfg.General.Tun.DNSHijack[0] != "z:53" || len(cfg.Tunnels) != 1 {
			t.Fatal("core apply changed the preserved listener configuration")
		}
		run, err := FreshProxyListeners(cfg, rawListeners)
		if err != nil {
			t.Fatal(err)
		}
		if run.Listeners["test-http"] == namedListener {
			t.Fatal("reused profile listener object")
		}
		StartProxyListeners(run)
		StartProxyListeners(run) // an idempotent update must not drop ports
		if run.Listeners["test-http"].Address() != named.addr {
			t.Fatal("named listener retained old handles")
		}
		for _, p := range []testPort{http, socks, mixed, named, tun} {
			assertPortOpen(t, "tcp", p.addr)
		}
		for _, p := range []testPort{socks, mixed, tun} {
			assertPortOpen(t, "udp", p.addr)
		}
		StopProxyListeners()
		StopProxyListeners()
		for _, p := range []testPort{http, socks, mixed, named, tun} {
			assertPortFree(t, "tcp", p.addr)
		}
		for _, p := range []testPort{socks, mixed, tun} {
			assertPortFree(t, "udp", p.addr)
		}
	}
}

func TestFreshProxyListenersRejectsInvalidOrDuplicateDefinitions(t *testing.T) {
	cfg := &config.Config{Listeners: map[string]C.InboundListener{"original": nil}}
	for _, raw := range [][]map[string]any{
		{{"type": "unknown"}},
		{{"type": "http", "name": "same"}, {"type": "http", "name": "same"}},
	} {
		if _, err := FreshProxyListeners(cfg, raw); err == nil {
			t.Fatal("invalid listeners accepted")
		}
		if len(cfg.Listeners) != 1 {
			t.Fatal("failed preparation changed preserved config")
		}
	}
}
