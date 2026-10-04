package compat

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	coreForwarding "github.com/metacubex/mihomo/component/forwarding"
)

func prepareManagementNetwork(t *testing.T) {
	t.Helper()
	coreForwarding.EnableManagementNetwork()
	if err := coreForwarding.ResumeManagementNetwork(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		coreForwarding.CancelManagementNetwork()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coreForwarding.WaitManagementNetwork(ctx); err != nil {
			t.Error(err)
		}
		if err := coreForwarding.ResumeManagementNetwork(); err != nil {
			t.Error(err)
		}
	})
}

func TestDownloadManagementNetworkTransitionCancelsBodyAndPreservesFile(t *testing.T) {
	prepareManagementNetwork(t)
	entered := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			close(entered)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("complete"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "subscription.yaml")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := DownloadConfig(context.Background(), server.URL, "", path); result <- err }()
	<-entered
	coreForwarding.CancelManagementNetwork()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coreForwarding.WaitManagementNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("partial download succeeded after transition")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "previous" {
		t.Fatalf("changed file: %q %v", data, err)
	}
	if _, err := DownloadConfig(context.Background(), server.URL, "", path); !errors.Is(err, coreForwarding.ErrManagementNetworkPaused) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("transition admitted a new HTTP attempt")
	}
	if err := coreForwarding.ResumeManagementNetwork(); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadConfig(context.Background(), server.URL, "", path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "complete" {
		t.Fatalf("resume result %q", data)
	}
}

func TestDownloadUsesSocketProtectionAndDoesNotBypassARejectedSocket(t *testing.T) {
	prepareManagementNetwork(t)
	oldHook := dialer.DefaultSocketHook
	defer func() { dialer.DefaultSocketHook = oldHook }()
	failure := errors.New("platform protection refused")
	var protected, received atomic.Int32
	dialer.DefaultSocketHook = func(string, string, syscall.RawConn) error { protected.Add(1); return failure }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1); _, _ = w.Write([]byte("config")) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "profile")
	if _, err := DownloadConfig(context.Background(), server.URL, "", path); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if protected.Load() == 0 || received.Load() != 0 {
		t.Fatalf("protection %d, HTTP %d", protected.Load(), received.Load())
	}
	dialer.DefaultSocketHook = func(string, string, syscall.RawConn) error { protected.Add(1); return nil }
	if _, err := DownloadConfig(context.Background(), server.URL, "", path); err != nil {
		t.Fatal(err)
	}
	if received.Load() != 1 {
		t.Fatal("protected request did not arrive")
	}
}

func TestDownloadDNSUsesTheSameProtectionHook(t *testing.T) {
	prepareManagementNetwork(t)
	oldHook := dialer.DefaultSocketHook
	defer func() { dialer.DefaultSocketHook = oldHook }()
	var dns atomic.Int32
	dialer.DefaultSocketHook = func(network, _ string, _ syscall.RawConn) error {
		if strings.HasPrefix(network, "udp") || strings.HasPrefix(network, "tcp") {
			dns.Add(1)
		}
		return errors.New("DNS protection refused before send")
	}
	path := filepath.Join(t.TempDir(), "profile")
	if _, err := DownloadConfig(context.Background(), "http://clashbox-management.invalid/config", "", path); err == nil {
		t.Fatal("DNS failure bypassed")
	}
	if dns.Load() == 0 {
		t.Fatal("native resolver did not use socket protection")
	}
}

func TestDetachedDownloadDialCannotEscapeItsOriginalNetworkEpoch(t *testing.T) {
	prepareManagementNetwork(t)
	parent, finish, err := coreForwarding.AcquireManagementNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	left, right := net.Pipe()
	defer right.Close()
	dial := managementDownloadDial(parent, func(context.Context, string, string) (net.Conn, error) { close(entered); <-release; return left, nil })
	result := make(chan error, 1)
	go func() { _, err := dial(context.Background(), "tcp", "late"); result <- err }()
	<-entered
	coreForwarding.CancelManagementNetwork()
	finish()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := coreForwarding.WaitManagementNetwork(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forgot detached dial: %v", err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("late connection was published")
	}
	if err := coreForwarding.WaitManagementNetwork(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coreForwarding.ResumeManagementNetwork(); err != nil {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), "tcp", "late-again"); err == nil {
		t.Fatal("old parent acquired replacement epoch")
	}
}
