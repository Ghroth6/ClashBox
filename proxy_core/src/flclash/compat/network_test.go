package compat

import (
	"testing"

	"github.com/metacubex/mihomo/component/platformnetwork"
)

func TestNetworkDecoderRejectsPartialAndTrailingUpdates(t *testing.T) {
	before, enabled := platformnetwork.Generation()
	for _, raw := range []string{
		`[{"iface":"wlan0","ipAddress":{"address":"192.0.2.1"}}]`,
		`{"generation":1,"online":false,"dnses":[]}`,
		`{"generation":0,"online":false}`,
		`{"generation":1,"online":true,"interfaces":[]}`,
		`{"generation":1,"online":false} {"generation":2,"online":false}`,
		`{"generation":1,"online":false} garbage`,
	} {
		if err := PublishNetworkSnapshot(raw); err == nil {
			t.Fatalf("accepted incomplete snapshot %s", raw)
		}
		if after, active := platformnetwork.Generation(); after != before || active != enabled {
			t.Fatal("invalid wrapper input changed the active network")
		}
	}
}
