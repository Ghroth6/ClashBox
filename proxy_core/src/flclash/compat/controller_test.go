package compat

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/hub/route"
)

func TestEmbeddedControllerKeepsQueriesAndRejectsConfigMutation(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	reservation.Close()
	ConfigureEmbeddedController()
	route.ReCreateServer(&route.Config{Addr: address, Secret: "local-test"})
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 200 * time.Millisecond}
	request := func(method, path string) (int, error) {
		// Malformed bodies are intentional: if a mutation route is accidentally
		// registered, it must fail parsing rather than apply host network settings.
		req, err := http.NewRequest(method, "http://"+address+path, strings.NewReader("{"))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer local-test")
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return response.StatusCode, nil
	}
	t.Cleanup(func() {
		route.ReCreateServer(&route.Config{})
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			listener, err := net.Listen("tcp", address)
			if err == nil {
				listener.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("test controller did not release its port")
	})
	ready := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if status, err := request("GET", "/version"); err == nil && status == http.StatusOK {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("test controller did not start")
	}
	for _, path := range []string{"/configs", "/proxies"} {
		if status, err := request("GET", path); err != nil || status != http.StatusOK {
			t.Fatalf("GET %s: status=%d err=%v", path, status, err)
		}
	}
	for _, method := range []string{"PUT", "PATCH"} {
		if status, err := request(method, "/configs"); err != nil || status != http.StatusMethodNotAllowed {
			t.Fatalf("%s /configs: status=%d err=%v", method, status, err)
		}
	}
}
