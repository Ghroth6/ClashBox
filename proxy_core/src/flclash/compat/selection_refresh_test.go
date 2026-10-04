package compat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/component/resource"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

// The wrapper only controls scheduling of the real provider Version read.
// Proxy parsing, file refresh, group membership, and Selector.Set remain production code.
type selectionRefreshGate struct {
	P.ProxyProvider
	reads   atomic.Int32
	entered chan struct{}
	updated chan struct{}
}

func TestProxyCatalogJSONKeepsActualIdentityThroughRefreshAndRemoval(t *testing.T) {
	aOld, aNew, bOld, bNew := catalogProxy("x"), catalogProxy("x"), catalogProxy("y"), catalogProxy("x")
	makeProvider := func(name string, versions map[string][]C.Proxy) (*provider.ProxySetProvider, func(string)) {
		path := filepath.Join(t.TempDir(), name+".txt")
		if err := os.WriteFile(path, []byte("initial"), 0600); err != nil {
			t.Fatal(err)
		}
		hc := provider.NewHealthCheck(nil, "", 1000, 0, true, nil)
		p, err := provider.NewProxySetProvider(name, 0, nil, func(data []byte) ([]C.Proxy, error) {
			return versions[string(data)], nil
		}, resource.NewFileVehicle(path), hc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		if err := p.Initial(); err != nil {
			t.Fatal(err)
		}
		return p, func(version string) {
			if err := os.WriteFile(path, []byte(version), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.Update(); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, refreshA := makeProvider("A", map[string][]C.Proxy{"initial": {aOld}, "new": {aNew}, "gone": {catalogProxy("z")}})
	b, refreshB := makeProvider("B", map[string][]C.Proxy{"initial": {bOld}, "collision": {bNew}})
	group := catalogSelector(t, "choice", "y", b, a)
	static, providers := map[string]C.Proxy{"choice": group}, map[string]P.ProxyProvider{"A": a, "B": b}
	catalog := requireCatalog(t, static, providers)
	want := catalog.ID(aOld)
	if err := catalog.Select("choice", want); err != nil {
		t.Fatal(err)
	}
	assertSelection := func(want string, ambiguous, unavailable bool) {
		t.Helper()
		current := requireCatalog(t, static, providers)
		data, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		var entries map[string]struct {
			Now         string `json:"now"`
			Ambiguous   bool   `json:"selectionAmbiguous"`
			Unavailable bool   `json:"selectionUnavailable"`
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		got := entries["choice"]
		if got.Now != want || got.Ambiguous != ambiguous || got.Unavailable != unavailable {
			t.Fatalf("selection JSON = %+v, want %q ambiguous=%v unavailable=%v", got, want, ambiguous, unavailable)
		}
	}
	assertSelection(want, false, false)
	refreshB("collision")
	assertSelection(want, true, false)
	refreshA("new")
	assertSelection(want, true, false)
	if group.Adapter().(outboundgroup.IdentitySelectAble).SelectedProxy() != aNew {
		t.Fatal("same identity did not resolve refreshed object")
	}
	refreshA("gone")
	assertSelection("", false, true)
	refreshA("new")
	assertSelection(want, true, false)
	if err := group.Adapter().(outboundgroup.SelectAble).Set("x"); err != nil {
		t.Fatal(err)
	}
	assertSelection(requireCatalog(t, static, providers).ID(bNew), true, false)
}

func (p *selectionRefreshGate) Version() uint32 {
	if p.reads.Add(1) == 2 { // Selector.Set, after ProxyCatalog.Select's member check
		close(p.entered)
		<-p.updated
	}
	return p.ProxyProvider.Version()
}

func TestProxyCatalogSelectionDuringProviderRefresh(t *testing.T) {
	aNode, bBefore, bAfter := catalogProxy("x"), catalogProxy("y"), catalogProxy("x")
	a := catalogProvider(t, "A", aNode)
	path := filepath.Join(t.TempDir(), "B.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	hc := provider.NewHealthCheck(nil, "", 1000, 0, true, nil)
	b, err := provider.NewProxySetProvider("B", 0, nil, func(data []byte) ([]C.Proxy, error) {
		if string(data) == "before" {
			return []C.Proxy{bBefore}, nil
		}
		return []C.Proxy{bAfter}, nil
	}, resource.NewFileVehicle(path), hc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if err := b.Initial(); err != nil {
		t.Fatal(err)
	}
	gate := &selectionRefreshGate{ProxyProvider: b, entered: make(chan struct{}), updated: make(chan struct{})}
	group := catalogSelector(t, "choice", "y", gate, a)
	catalog := requireCatalog(t, map[string]C.Proxy{"choice": group}, map[string]P.ProxyProvider{"A": a, "B": gate})
	refreshed := make(chan error, 1)
	go func() {
		<-gate.entered
		err := os.WriteFile(path, []byte("after"), 0600)
		if err == nil {
			err = b.Update()
		}
		refreshed <- err
		close(gate.updated)
	}()
	wantedID := catalog.ID(aNode)
	selectionErr := catalog.Select("choice", wantedID)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	selected := group.Adapter().(*outboundgroup.Selector).Unwrap(nil, false)
	actual := requireCatalog(t, map[string]C.Proxy{"choice": group}, map[string]P.ProxyProvider{"A": a, "B": gate})
	t.Logf("requested=%q; Select error=%v; actual=%q; selected is provider B object=%v", wantedID, selectionErr, actual.ID(selected), selected == bAfter)
	if selectionErr == nil && selected != aNode {
		t.Fatalf("provider-qualified selection succeeded but chose another provider: wanted %q, actual %q", wantedID, actual.ID(selected))
	}
}
