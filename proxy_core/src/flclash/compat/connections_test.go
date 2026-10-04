package compat

import (
	"testing"

	"github.com/metacubex/mihomo/tunnel/statistic"
)

func TestConnectionCountUsesSuppliedSnapshot(t *testing.T) {
	if ConnectionCount(nil) != 0 || ConnectionCount(&statistic.Snapshot{}) != 0 {
		t.Fatal("missing snapshot must have zero connections")
	}
	old := &statistic.Snapshot{Connections: []*statistic.TrackerInfo{{}, {}}}
	next := &statistic.Snapshot{Connections: []*statistic.TrackerInfo{{}}}
	if ConnectionCount(old) != 2 || ConnectionCount(next) != 1 || ConnectionCount(old) != 2 {
		t.Fatal("count must belong to the supplied snapshot")
	}
}
