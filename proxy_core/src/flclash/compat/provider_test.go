package compat

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/component/resource"
)

type providerJSON string

func (p providerJSON) MarshalJSON() ([]byte, error) { return []byte(p), nil }

type failedProvider struct{ err error }

func (p failedProvider) MarshalJSON() ([]byte, error) { return nil, p.err }

func TestSubscriptionUnknownAndZeroAreDifferent(t *testing.T) {
	for _, raw := range []string{`{}`, `{"subscriptionInfo":null}`, `null`} {
		info, err := SubscriptionInfo(providerJSON(raw))
		if err != nil || info != nil {
			t.Fatalf("%s: info=%+v err=%v", raw, info, err)
		}
	}
	info, err := SubscriptionInfo(providerJSON(`{"subscriptionInfo":{}}`))
	if err != nil || info == nil || *info != (provider.SubscriptionInfo{}) {
		t.Fatalf("zero information was lost: %+v, %v", info, err)
	}
	var p *provider.ProxySetProvider
	if info, err := SubscriptionInfo(p); err != nil || info != nil {
		t.Fatalf("typed nil: %+v, %v", info, err)
	}
}

func TestSubscriptionPreservesInt64Fields(t *testing.T) {
	info, err := SubscriptionInfo(providerJSON(`{"subscriptionInfo":{"Upload":9007199254740993,"Download":23,"Total":9007199254741000,"Expire":1893456000},"futureField":true}`))
	if err != nil || info == nil {
		t.Fatal(info, err)
	}
	if *info != (provider.SubscriptionInfo{Upload: 9007199254740993, Download: 23, Total: 9007199254741000, Expire: 1893456000}) {
		t.Fatalf("subscription changed: %+v", info)
	}
}

func TestSubscriptionSerializationErrorsPropagate(t *testing.T) {
	want := errors.New("provider unavailable")
	if _, err := SubscriptionInfo(failedProvider{want}); !errors.Is(err, want) {
		t.Fatalf("lost serialization error: %v", err)
	}
	for _, raw := range []string{`{bad`, `{"subscriptionInfo":"invalid"}`, `{"subscriptionInfo":{"Total":"wrong type"}}`} {
		if _, err := SubscriptionInfo(providerJSON(raw)); err == nil {
			t.Fatalf("accepted malformed subscription: %s", raw)
		}
	}
}

func TestSubscriptionUsesRealUpstreamProvider(t *testing.T) {
	hc := provider.NewHealthCheck(nil, "", 1000, 0, true, nil)
	p, err := provider.NewProxySetProvider("offline", 0, nil, nil,
		resource.NewFileVehicle(filepath.Join(t.TempDir(), "provider.yaml")), hc)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// No Initial call: no provider download or background health check.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := SubscriptionInfo(p); err != nil || info != nil {
		t.Fatalf("real provider %s: %+v, %v", data, info, err)
	}
}
