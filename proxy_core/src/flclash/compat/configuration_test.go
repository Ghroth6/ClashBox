package compat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/resource"
)

func TestConfigurationRetirementJoinsCallbackAndRejectsLateWrite(t *testing.T) {
	owner := NewConfigurationTasks()
	ctx, finish, err := owner.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "resource")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	owner.Cancel()
	if err := resource.Commit(ctx, func() error { return os.WriteFile(path, []byte("late"), 0600) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("late write admitted: %v", err)
	}
	if _, _, err := owner.Acquire(); err == nil {
		t.Fatal("retired owner admitted a new request")
	}
	wait, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := owner.Wait(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("callback not joined: %v", err)
	}
	finish()
	finish() // Completing a request twice must not corrupt the shared counter.
	if err := owner.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("retired write changed resource: %q %v", data, err)
	}
	if owner.Active() {
		t.Fatal("retired owner revived")
	}
	var unloaded *ConfigurationTasks
	if _, _, err := unloaded.Acquire(); err == nil {
		t.Fatal("unloaded owner admitted work")
	}
}

func TestDownloadCancellationAtCommitPreservesFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new")) }))
	defer server.Close()
	for _, retireOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "configuration"}[retireOwner], func(t *testing.T) {
			owner := NewConfigurationTasks()
			ownerCtx, finish, err := owner.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			ctx, cancel := context.WithCancel(ownerCtx)
			defer cancel()
			if retireOwner {
				// Retire before acquiring the owner fence, not inside its lock.
				ctx = resource.WithCommitGuard(context.Background(), func(action func() error) error {
					owner.Cancel()
					return resource.Commit(ownerCtx, action)
				})
			} else {
				ctx = resource.WithCommitGuard(ctx, func(action func() error) error {
					cancel()
					return action()
				})
			}
			path := filepath.Join(t.TempDir(), "resource")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := DownloadConfig(ctx, server.URL, "test", path); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel at commit: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old" {
				t.Fatalf("cancelled write changed file: %q %v", data, err)
			}
		})
	}
}

func TestDownloadAndGeoUpdateStopBeforeRetirementCompletes(t *testing.T) {
	for _, geo := range []bool{false, true} {
		t.Run(map[bool]string{false: "MRS", true: "GeoSite"}[geo], func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			home := geoHome(t)
			path := filepath.Join(home, "GeoSite.dat")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			oldURL := geodata.GeoSiteUrl()
			geodata.SetGeoSiteUrl(server.URL)
			defer geodata.SetGeoSiteUrl(oldURL)
			owner := NewConfigurationTasks()
			ctx, finish, err := owner.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				defer finish()
				if geo {
					result <- UpdateGeoDataContext(ctx, "GeoSite", "GeoSite.dat")
				} else {
					_, err := DownloadConfig(ctx, server.URL, "", path)
					result <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP request did not start")
			}
			owner.Cancel()
			wait, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := owner.Wait(wait); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("unexpected result: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old" {
				t.Fatalf("cancelled download changed file: %q %v", data, err)
			}
		})
	}
}

func TestGeoUpdateWaitingForOtherRequestCanBeCanceled(t *testing.T) {
	geoUpdateGate <- struct{}{}
	defer func() { <-geoUpdateGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := UpdateGeoDataContext(ctx, "GeoSite", "GeoSite.dat"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued update not cancelled: %v", err)
	}
}

func TestOrdinaryDownloadKeepsMetadataAndRejectsHTTPError(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("valid subscription"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "profile")
	metadata, err := DownloadConfig(context.Background(), server.URL, "", path)
	if err != nil || metadata == "" {
		t.Fatalf("ordinary download: %q %v", metadata, err)
	}
	status = http.StatusForbidden
	if _, err := DownloadConfig(context.Background(), server.URL, "", path); err == nil {
		t.Fatal("HTTP error accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "valid subscription" {
		t.Fatalf("HTTP error changed file: %q %v", data, err)
	}
}
