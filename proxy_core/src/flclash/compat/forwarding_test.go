package compat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/listener/inner"
	"github.com/metacubex/mihomo/tunnel"
)

func prepareForwardingConfig(t *testing.T) (*config.Config, testPort, testPort) {
	t.Helper()
	if err := ConfigureForwarding(); err != nil {
		t.Fatal(err)
	}
	proxy, dns := reserveProxyPort(t), reserveProxyPort(t)
	cfg, err := config.Parse([]byte(fmt.Sprintf("mixed-port: %d\nbind-address: 127.0.0.1\ndns:\n  enable: true\n  listen: %s\n  nameserver: [127.0.0.1:9]\nrules:\n  - MATCH,DIRECT\n", proxy.port, dns.addr)))
	if err != nil {
		t.Fatal(err)
	}
	hub.ApplyConfig(ConfigWithoutProxyListeners(cfg))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := StopForwarding(ctx); err != nil {
			t.Error(err)
		}
	})
	assertPortFree(t, "tcp", proxy.addr)
	assertPortFree(t, "udp", dns.addr)
	return cfg, proxy, dns
}

func assertEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "alive" {
		t.Fatalf("echo lost: %q, %v", data, err)
	}
}

func TestForwardingStopRetainsInternalManagementConnection(t *testing.T) {
	cfg, proxy, dns := prepareForwardingConfig(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	if err := StartForwarding(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	first := ForwardingGeneration()
	if first == 0 {
		t.Fatal("no running generation")
	}
	management, err := inner.HandleTcpContext(context.Background(), tunnel.Tunnel, echo.Addr().String(), "DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	defer management.Close()
	assertEcho(t, management)
	client, err := net.DialTimeout("tcp", proxy.addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr())
	reader := bufio.NewReader(client)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("proxy CONNECT: %q %v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	assertEcho(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StopForwarding(ctx); err != nil {
		t.Fatal(err)
	}
	if ForwardingGeneration() != 0 {
		t.Fatal("stopped generation remained published")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("forwarding client survived Stop")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("Stop did not close the forwarding client")
	}
	assertEcho(t, management)
	assertPortFree(t, "tcp", proxy.addr)
	assertPortFree(t, "udp", dns.addr)
	if err := StartForwarding(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if ForwardingGeneration() <= first {
		t.Fatal("restart reused the retired generation")
	}
}

func TestForwardingCancelledParentNeverReportsReady(t *testing.T) {
	cfg, proxy, dns := prepareForwardingConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := StartForwarding(ctx, cfg); err == nil {
		t.Fatal("cancelled parent opened forwarding")
	}
	if ForwardingGeneration() != 0 {
		t.Fatal("cancelled start published readiness")
	}
	assertPortFree(t, "tcp", proxy.addr)
	assertPortFree(t, "udp", dns.addr)
}

func TestForwardingDNSBindFailureRequiresCleanupBeforeRestart(t *testing.T) {
	cfg, proxy, dns := prepareForwardingConfig(t)
	occupied, err := net.ListenPacket("udp", dns.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := StartForwarding(context.Background(), cfg); err == nil {
		t.Fatal("occupied DNS port was reported ready")
	}
	if ForwardingGeneration() != 0 {
		t.Fatal("failed start published readiness")
	}
	if err := StartForwarding(context.Background(), cfg); err == nil {
		t.Fatal("unresolved startup was forgotten")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StopForwarding(ctx); err != nil {
		t.Fatal(err)
	}
	assertPortFree(t, "tcp", proxy.addr)
	_ = occupied.Close()
	if err := StartForwarding(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}
