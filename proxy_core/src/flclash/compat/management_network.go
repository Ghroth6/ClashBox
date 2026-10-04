package compat

import (
	"context"

	coreForwarding "github.com/metacubex/mihomo/component/forwarding"
)

func EnableManagementNetwork() { coreForwarding.EnableManagementNetwork() }
func CancelManagementNetwork() { coreForwarding.CancelManagementNetwork() }
func WaitManagementNetwork(ctx context.Context) error {
	return coreForwarding.WaitManagementNetwork(ctx)
}
func ResumeManagementNetwork() error { return coreForwarding.ResumeManagementNetwork() }
