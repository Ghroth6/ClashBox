package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/component/resource"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

func catalogProxy(name string) C.Proxy {
	return adapter.NewProxy(outbound.NewDirectWithOption(outbound.DirectOption{Name: name}))
}

func catalogProvider(t *testing.T, name string, proxies ...C.Proxy) P.ProxyProvider {
	t.Helper()
	hc := provider.NewHealthCheck(proxies, "", 1000, 0, true, nil)
	p, err := provider.NewCompatibleProvider(name, proxies, hc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func catalogSelector(t *testing.T, name, selected string, providers ...P.ProxyProvider) C.Proxy {
	t.Helper()
	s, err := outboundgroup.NewSelector(outboundgroup.GroupCommonOption{Name: name},
		outboundgroup.SelectorOption{DefaultSelected: selected}, catalogProxy("empty-fallback"), providers)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.NewProxy(s)
}

func requireCatalog(t *testing.T, static map[string]C.Proxy, providers map[string]P.ProxyProvider) *ProxyCatalog {
	t.Helper()
	c, err := NewProxyCatalog(static, providers)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func catalogJSON(t *testing.T, c *ProxyCatalog) map[string]struct {
	Name               string   `json:"name"`
	ID                 string   `json:"id"`
	All                []string `json:"all"`
	Now                string   `json:"now"`
	SelectionAmbiguous bool     `json:"selectionAmbiguous"`
} {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]struct {
		Name               string   `json:"name"`
		ID                 string   `json:"id"`
		All                []string `json:"all"`
		Now                string   `json:"now"`
		SelectionAmbiguous bool     `json:"selectionAmbiguous"`
	}
	if err = json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProxyCatalogPreservesStaticGroupsAndProviderObjects(t *testing.T) {
	staticProxy := catalogProxy("shared")
	aShared, aGroup, unique := catalogProxy("shared"), catalogProxy("choice"), catalogProxy("unique")
	aDup, bDup := catalogProxy("duplicate"), catalogProxy("duplicate")
	a := catalogProvider(t, "a", aShared, aGroup, unique, aDup)
	b := catalogProvider(t, "b", bDup)
	group := catalogSelector(t, "choice", "duplicate", a, b)
	static := map[string]C.Proxy{"shared": staticProxy, "choice": group}
	providers := map[string]P.ProxyProvider{"b": b, "a": a}
	beforeA := append([]C.Proxy(nil), a.Proxies()...)
	beforeGroup := append([]C.Proxy(nil), group.Adapter().(outboundgroup.ProxyGroup).Proxies()...)
	c := requireCatalog(t, static, providers)
	for name, want := range static {
		got, err := c.Lookup(name)
		if err != nil || got != want {
			t.Fatalf("static %q was replaced: %v", name, err)
		}
	}
	if c.ID(aShared) == "shared" || c.ID(aGroup) == "choice" || c.ID(aDup) == c.ID(bDup) {
		t.Fatal("provider identity collided with a static/group name or another provider")
	}
	if got, err := c.Lookup("unique"); err != nil || got != unique {
		t.Fatalf("unique bare-name compatibility failed: %v", err)
	}
	if _, err := c.Lookup("duplicate"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous bare name did not fail explicitly: %v", err)
	}
	entries := catalogJSON(t, c)
	wantAll := []string{c.ID(aShared), c.ID(aGroup), c.ID(unique), c.ID(aDup), c.ID(bDup)}
	if !reflect.DeepEqual(entries["choice"].All, wantAll) || entries["choice"].Now != "" || !entries["choice"].SelectionAmbiguous {
		t.Fatalf("group JSON did not preserve qualified membership/ambiguity: %+v", entries["choice"])
	}
	for id, entry := range entries {
		if entry.Name != id || entry.ID != id {
			t.Fatalf("JSON identity mismatch: key=%q entry=%+v", id, entry)
		}
	}
	items := c.Items()
	delete(items, "choice")
	items["shared"] = aShared
	if got, _ := c.Lookup("shared"); got != staticProxy {
		t.Fatal("returned items map aliases catalog state")
	}
	if len(static) != 2 || static["shared"] != staticProxy || static["choice"] != group || len(providers) != 2 || providers["a"] != a || providers["b"] != b {
		t.Fatal("catalog modified the source maps")
	}
	if !reflect.DeepEqual(a.Proxies(), beforeA) || !reflect.DeepEqual(group.Adapter().(outboundgroup.ProxyGroup).Proxies(), beforeGroup) {
		t.Fatal("catalog modified provider/group membership")
	}
	if aShared.Name() != "shared" || aGroup.Name() != "choice" || unique.Name() != "unique" {
		t.Fatal("catalog renamed the core proxy objects")
	}
}

func TestProxyCatalogSelectionRejectsAmbiguityAndReplaysQualifiedIDs(t *testing.T) {
	aDup, bDup, unique := catalogProxy("same"), catalogProxy("same"), catalogProxy("unique")
	a, b := catalogProvider(t, "a", aDup, unique), catalogProvider(t, "b", bDup)
	group := catalogSelector(t, "choice", "unique", a, b)
	c := requireCatalog(t, map[string]C.Proxy{"choice": group}, map[string]P.ProxyProvider{"a": a, "b": b})
	for _, id := range []string{"same", c.ID(aDup), c.ID(bDup)} {
		if err := c.Select("choice", id); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("accepted ambiguous membership %q: %v", id, err)
		}
		if group.Adapter().(outboundgroup.ProxyGroup).Now() != "unique" {
			t.Fatal("rejected selection changed the group")
		}
	}
	if err := c.Select("choice", "unique"); err != nil {
		t.Fatal(err)
	}
	saved := catalogJSON(t, c)["choice"].Now
	if saved != c.ID(unique) {
		t.Fatal("selected identity was not serialized in qualified form")
	}
	// Recreate the provider's proxy objects as a refresh/reload would do.
	newUnique, other := catalogProxy("unique"), catalogProxy("other")
	newA := catalogProvider(t, "a", other, newUnique)
	newGroup := catalogSelector(t, "choice", "other", newA)
	refreshed := requireCatalog(t, map[string]C.Proxy{"choice": newGroup}, map[string]P.ProxyProvider{"a": newA})
	if err := refreshed.Select("choice", saved); err != nil {
		t.Fatalf("saved qualified selection did not replay: %v", err)
	}
	if catalogJSON(t, refreshed)["choice"].Now != saved || newGroup.Adapter().(outboundgroup.ProxyGroup).Now() != "unique" {
		t.Fatal("replayed identity selected the wrong node")
	}
	if err := refreshed.Select("choice", ""); err != nil {
		t.Fatal(err)
	}
	if newGroup.Adapter().(outboundgroup.ProxyGroup).Now() != "other" {
		t.Fatal("empty selection did not return to the group's default behavior")
	}
}

func TestProxyCatalogStableIdentityAcrossCollisionReordering(t *testing.T) {
	one, two := catalogProxy("same"), catalogProxy("@same")
	unobstructed := requireCatalog(t, nil, map[string]P.ProxyProvider{"p": catalogProvider(t, "p", one)})
	// A legal static name can equal any public provider ID, regardless of the
	// encoding chosen by the catalog. Preserve that static object as well.
	collision := unobstructed.ID(one)
	static := map[string]C.Proxy{collision: catalogProxy(collision)}
	initial := requireCatalog(t, static, map[string]P.ProxyProvider{"p": catalogProvider(t, "p", one)})
	first := requireCatalog(t, static, map[string]P.ProxyProvider{"p": catalogProvider(t, "p", one, two)})
	if initial.ID(one) != first.ID(one) {
		t.Fatalf("adding another node changed an existing identity: %q -> %q", initial.ID(one), first.ID(one))
	}
	newOne, newTwo := catalogProxy("same"), catalogProxy("@same")
	second := requireCatalog(t, static, map[string]P.ProxyProvider{"p": catalogProvider(t, "p", newTwo, newOne)})
	if first.ID(one) != second.ID(newOne) || first.ID(two) != second.ID(newTwo) {
		t.Fatalf("provider reorder changed IDs: before=%q/%q after=%q/%q", first.ID(one), first.ID(two), second.ID(newOne), second.ID(newTwo))
	}
	for _, c := range []*ProxyCatalog{first, second} {
		if got, err := c.Lookup(collision); err != nil || got != static[collision] {
			t.Fatal("resolving a qualified-name collision replaced the static proxy")
		}
	}
}

func TestProxyCatalogRejectsDuplicateNamesWithinProvider(t *testing.T) {
	p := catalogProvider(t, "duplicate", catalogProxy("x"), catalogProxy("x"))
	if _, err := NewProxyCatalog(nil, map[string]P.ProxyProvider{"duplicate": p}); err == nil {
		t.Fatal("different nodes in one provider share an unrepresentable identity")
	}
}

func TestProxyCatalogRefreshRejectsStaleSelectionAndSnapshot(t *testing.T) {
	oldProxy, newProxy := catalogProxy("old"), catalogProxy("new")
	path := filepath.Join(t.TempDir(), "provider.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	hc := provider.NewHealthCheck(nil, "", 1000, 0, true, nil)
	p, err := provider.NewProxySetProvider("refresh", 0, nil, func(data []byte) ([]C.Proxy, error) {
		switch string(data) {
		case "old":
			return []C.Proxy{oldProxy}, nil
		case "new":
			return []C.Proxy{newProxy}, nil
		default:
			return nil, fmt.Errorf("unexpected test payload %q", data)
		}
	}, resource.NewFileVehicle(path), hc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err = p.Initial(); err != nil {
		t.Fatal(err)
	}
	group := catalogSelector(t, "choice", "old", p)
	static, providers := map[string]C.Proxy{"choice": group}, map[string]P.ProxyProvider{"refresh": p}
	stale := requireCatalog(t, static, providers)
	saved := stale.ID(oldProxy)
	events := NewEventIDs(static, providers)
	if events.ID(oldProxy, oldProxy.Name(), "refresh") != saved {
		t.Fatal("current provider event did not match catalog identity")
	}
	if err = os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = p.Update(); err != nil {
		t.Fatal(err)
	}
	if err = stale.Select("choice", saved); err == nil || !strings.Contains(err.Error(), "current member") {
		t.Fatalf("stale selection silently chose a replacement: %v", err)
	}
	if _, err = json.Marshal(stale); err == nil {
		t.Fatal("stale catalog serialized an unknown refreshed group member")
	}
	current := requireCatalog(t, static, providers)
	if events.ID(oldProxy, oldProxy.Name(), "refresh") != "" {
		t.Fatal("replaced provider object can still publish a late event")
	}
	if got := events.ID(newProxy, newProxy.Name(), "refresh"); got != current.ID(newProxy) {
		t.Fatalf("new current provider object lost its event identity: %q", got)
	}
	if _, err = current.Lookup(saved); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("removed qualified node did not fail lookup: %v", err)
	}
	if err = current.Select("choice", saved); err == nil {
		t.Fatal("removed saved selection did not fail replay")
	}
	if err = current.Select("choice", current.ID(newProxy)); err != nil {
		t.Fatal(err)
	}
}

func TestProxyCatalogSelectionRejectsWrongGroupOrNonmember(t *testing.T) {
	member, outsider := catalogProxy("member"), catalogProxy("outsider")
	p := catalogProvider(t, "p", member)
	group := catalogSelector(t, "choice", "member", p)
	c := requireCatalog(t, map[string]C.Proxy{"choice": group, "outsider": outsider}, map[string]P.ProxyProvider{"p": p})
	for _, selection := range [][2]string{{"missing", "member"}, {"outsider", "member"}, {"choice", "outsider"}, {"choice", "missing"}} {
		if err := c.Select(selection[0], selection[1]); err == nil {
			t.Fatalf("invalid selection succeeded: %v", selection)
		}
	}
	if group.Adapter().(outboundgroup.ProxyGroup).Now() != "member" {
		t.Fatal("invalid selection changed the live group")
	}
}

func TestQualifiedProxyIdentityEscapesAmbiguousNamesAndStaticPrefixes(t *testing.T) {
	seen := map[string][2]string{}
	for _, pair := range [][2]string{
		{"x [a]", "b"}, {"x", "a] [b"},
		{"@x", "p"}, {`\@x`, "p"}, {`\x`, "p"},
		{"x", "@p"}, {"x", `\@p`}, {"[x]", "[p]"},
		{"节点 / []", "订阅 \\ @"}, {"", "p"}, {"x", ""},
	} {
		id := QualifiedProxyID(pair[0], pair[1], nil)
		if prior, exists := seen[id]; exists {
			t.Fatalf("different provider/name pairs shared %q: %v and %v", id, prior, pair)
		}
		seen[id] = pair
		static := map[string]C.Proxy{id: catalogProxy(id), "@" + id: catalogProxy("@" + id)}
		qualified := QualifiedProxyID(pair[0], pair[1], static)
		if static[qualified] != nil || qualified != QualifiedProxyID(pair[0], pair[1], static) {
			t.Fatal("qualified identity is not deterministic or overlaps a static name")
		}
	}
}

func TestEventIDsMatchCatalogForStaticSharedAndProviderNodes(t *testing.T) {
	shared, remote := catalogProxy("shared"), catalogProxy(`node [x] @ \`)
	collision := QualifiedProxyID(remote.Name(), "p", nil)
	static := map[string]C.Proxy{"shared": shared, collision: catalogProxy(collision)}
	p := catalogProvider(t, "p", shared, remote)
	catalog := requireCatalog(t, static, map[string]P.ProxyProvider{"p": p})
	providers := map[string]P.ProxyProvider{"p": p}
	events := NewEventIDs(static, providers)
	if got := events.ID(shared, shared.Name(), "p"); got != catalog.ID(shared) || got != "shared" {
		t.Fatalf("compatible provider changed static event identity: %q", got)
	}
	if got := events.ID(remote, remote.Name(), "p"); got != catalog.ID(remote) {
		t.Fatalf("provider event and catalog disagree: event=%q catalog=%q", got, catalog.ID(remote))
	}
	// Matching labels alone do not prove that an object belongs to this active
	// configuration. A cancelled task may complete after its proxy was replaced.
	refreshed := catalogProxy(remote.Name())
	if got := events.ID(refreshed, refreshed.Name(), "p"); got != "" {
		t.Fatal("an object outside the current provider published an event")
	}
	if got := events.ID(nil, "unknown-static", ""); got != "" {
		t.Fatalf("unknown static event was not suppressed: %q", got)
	}
	newShared := catalogProxy(shared.Name())
	newP := catalogProvider(t, "p", refreshed, newShared)
	newEvents := NewEventIDs(map[string]C.Proxy{"shared": newShared, collision: static[collision]}, map[string]P.ProxyProvider{"p": newP})
	if newEvents.ID(shared, shared.Name(), "p") != "" || newEvents.ID(remote, remote.Name(), "p") != "" {
		t.Fatal("old configuration objects can publish into a replacement configuration")
	}
	if newEvents.ID(newShared, newShared.Name(), "p") != "shared" || newEvents.ID(refreshed, refreshed.Name(), "p") != catalog.ID(remote) {
		t.Fatal("replacement configuration lost valid static/provider identities")
	}
	if got := events.ID(remote, remote.Name(), "other-provider"); got != "" {
		t.Fatal("provider metadata can relabel an object from another provider")
	}
	want := catalog.ID(remote)
	// The published event mapping owns both maps: later caller map changes
	// must not race with, or reclassify, synchronous callback lookups.
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 1000; j++ {
				if events.ID(remote, remote.Name(), "p") != want || events.ID(shared, shared.Name(), "p") != "shared" {
					t.Error("immutable event identity changed")
				}
			}
		}()
	}
	delete(static, "shared")
	delete(static, collision)
	static["replacement"] = catalogProxy("replacement")
	delete(providers, "p")
	workers.Wait()
}
