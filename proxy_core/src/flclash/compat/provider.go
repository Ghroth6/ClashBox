// Package compat adapts the wrapper to the pinned public Mihomo interfaces.
package compat

import (
	"encoding/json"
	"fmt"

	"github.com/metacubex/mihomo/adapter/provider"
	cp "github.com/metacubex/mihomo/constant/provider"
	rp "github.com/metacubex/mihomo/rules/provider"
)

func SideUpdateProvider(p cp.Provider, data []byte) error {
	switch p := p.(type) {
	case *provider.ProxySetProvider:
		if p == nil {
			return fmt.Errorf("nil proxy provider")
		}
		_, _, err := p.SideUpdate(data)
		return err
	case *rp.RuleSetProvider:
		if p == nil {
			return fmt.Errorf("nil rule provider")
		}
		_, _, err := p.SideUpdate(data)
		return err
	default:
		return fmt.Errorf("not external provider")
	}
}

// SubscriptionInfo reads the provider's public representation. Missing and null
// information remain unknown (nil); serialization errors must reach the caller.
func SubscriptionInfo(p json.Marshaler) (*provider.SubscriptionInfo, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("serialize provider: %w", err)
	}
	var snapshot struct {
		SubscriptionInfo *provider.SubscriptionInfo `json:"subscriptionInfo"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("read provider subscription info: %w", err)
	}
	return snapshot.SubscriptionInfo, nil
}
