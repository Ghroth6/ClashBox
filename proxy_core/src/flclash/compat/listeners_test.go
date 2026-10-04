package compat

import (
	"errors"
	"math/rand"
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
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		// TCP and UDP can have different Windows exclusion ranges. On a
		// collision, sample another range instead of retrying adjacent ports.
		addr := "127.0.0.1:0"
		if attempt > 0 {
			addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(10000+rand.Intn(39000)))
		}
		udp, err := net.ListenPacket("udp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		addr = udp.LocalAddr().String()
		tcp, err := net.Listen("tcp", addr)
		if err != nil {
			lastErr = err
			udp.Close()
			continue
		}
		_, port, _ := net.SplitHostPort(addr)
		n, _ := strconv.Atoi(port)
		tcp.Close()
		udp.Close()
		return testPort{addr, n}
	}
	t.Fatalf("no free TCP/UDP pair: %v", lastErr)
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
	t.Cleanup(func() { _ = StopProxyListeners() })
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
		if err := StartProxyListeners(run); err != nil {
			t.Fatal(err)
		}
		if err := StartProxyListeners(run); err != nil { // an idempotent update must not drop ports
			t.Fatal(err)
		}
		if run.Listeners["test-http"].Address() != named.addr {
			t.Fatal("named listener retained old handles")
		}
		for _, p := range []testPort{http, socks, mixed, named, tun} {
			assertPortOpen(t, "tcp", p.addr)
		}
		for _, p := range []testPort{socks, mixed, tun} {
			assertPortOpen(t, "udp", p.addr)
		}
		if err := StopProxyListeners(); err != nil {
			t.Fatal(err)
		}
		if err := StopProxyListeners(); err != nil {
			t.Fatal(err)
		}
		for _, p := range []testPort{http, socks, mixed, named, tun} {
			assertPortFree(t, "tcp", p.addr)
		}
		for _, p := range []testPort{socks, mixed, tun} {
			assertPortFree(t, "udp", p.addr)
		}
	}
}

func TestProxyListenerBindFailureRollsBackAllIngressAndCanRetry(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			http, named, mixed := reserveProxyPort(t), reserveProxyPort(t), reserveProxyPort(t)
			var closeBlocker func() error
			if network == "tcp" {
				blocker, err := net.Listen(network, mixed.addr)
				if err != nil {
					t.Fatal(err)
				}
				closeBlocker = blocker.Close
			} else {
				blocker, err := net.ListenPacket(network, mixed.addr)
				if err != nil {
					t.Fatal(err)
				}
				closeBlocker = blocker.Close
			}
			t.Cleanup(func() { _ = closeBlocker(); _ = StopProxyListeners() })
			cfg := &config.Config{General: &config.General{Inbound: config.Inbound{
				Port: http.port, MixedPort: mixed.port, BindAddress: "127.0.0.1",
			}}}
			raw := []map[string]any{{"type": "http", "name": "early", "listen": "127.0.0.1", "port": named.port}}
			run, err := FreshProxyListeners(cfg, raw)
			if err != nil {
				t.Fatal(err)
			}
			err = StartProxyListeners(run)
			var bindError *net.OpError
			if !errors.As(err, &bindError) {
				t.Fatalf("want actual bind error, got %v", err)
			}
			assertPortFree(t, "tcp", http.addr)
			assertPortFree(t, "tcp", named.addr)
			// A failed UDP bind must also release its already-open TCP peer.
			if network == "udp" {
				assertPortFree(t, "tcp", mixed.addr)
			}
			if err := closeBlocker(); err != nil {
				t.Fatal(err)
			}
			assertPortFree(t, "tcp", mixed.addr)
			assertPortFree(t, "udp", mixed.addr)
			run, err = FreshProxyListeners(cfg, raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := StartProxyListeners(run); err != nil {
				t.Fatal(err)
			}
			assertPortOpen(t, "tcp", named.addr)
			assertPortOpen(t, "tcp", http.addr)
			assertPortOpen(t, "tcp", mixed.addr)
			assertPortOpen(t, "udp", mixed.addr)
			if err := StopProxyListeners(); err != nil {
				t.Fatal(err)
			}
		})
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
