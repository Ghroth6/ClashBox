package compat

import "github.com/metacubex/mihomo/tunnel/statistic"

// ConnectionCount belongs to this snapshot, not a second live observation.
func ConnectionCount(snapshot *statistic.Snapshot) int {
	if snapshot == nil {
		return 0
	}
	return len(snapshot.Connections)
}
