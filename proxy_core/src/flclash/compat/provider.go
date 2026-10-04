// Package compat adapts the wrapper to the pinned public Mihomo interfaces.
package compat

import (
	"encoding/json"
	"fmt"

	"github.com/metacubex/mihomo/adapter/provider"
)

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
