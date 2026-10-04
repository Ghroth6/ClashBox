package compat

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/component/resource"
	P "github.com/metacubex/mihomo/constant/provider"
	rp "github.com/metacubex/mihomo/rules/provider"
)

func TestSideUpdateReturnsActualProviderErrorsAndRejectsRetiredOwner(t *testing.T) {
	hc := provider.NewHealthCheck(nil, "", 1000, 0, true, nil)
	proxy, err := provider.NewProxySetProvider("proxy", 0, nil, nil, resource.NewFileVehicle(filepath.Join(t.TempDir(), "proxies.yaml")), hc)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	rule := rp.NewRuleSetProvider("rule", P.Domain, P.YamlRule, 0, resource.NewFileVehicle(filepath.Join(t.TempDir(), "rules.yaml")), nil, nil, nil).(*rp.RuleSetProvider)
	defer rule.Close()
	for _, item := range []struct {
		name   string
		p      P.Provider
		valid  []byte
		cancel func()
	}{
		{"proxy", proxy, []byte("proxies:\n  - {name: local, type: direct}\n"), proxy.Cancel},
		{"rule", rule, []byte("payload:\n  - example.com\n"), rule.Cancel},
	} {
		t.Run(item.name, func(t *testing.T) {
			if err := SideUpdateProvider(item.p, item.valid); err != nil {
				t.Fatalf("valid update: %v", err)
			}
			if err := SideUpdateProvider(item.p, []byte("[bad yaml")); err == nil {
				t.Fatal("parser error discarded")
			}
			item.cancel()
			if err := SideUpdateProvider(item.p, item.valid); !errors.Is(err, context.Canceled) {
				t.Fatalf("retired update: %v", err)
			}
		})
	}
}
