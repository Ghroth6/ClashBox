package compat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

// ProxyCatalog owns its maps, never the core's proxy objects. Provider nodes
// have stable qualified keys; a bare provider name is accepted only if unique.
type ProxyCatalog struct {
	static  map[string]C.Proxy
	items   map[string]C.Proxy
	ids     map[C.Proxy]string
	aliases map[string][]string
}

func NewProxyCatalog(static map[string]C.Proxy, providers map[string]P.ProxyProvider) (*ProxyCatalog, error) {
	c := &ProxyCatalog{static: map[string]C.Proxy{}, items: map[string]C.Proxy{}, ids: map[C.Proxy]string{}, aliases: map[string][]string{}}
	for name, proxy := range static {
		if proxy == nil {
			continue
		}
		c.static[name], c.items[name], c.ids[proxy] = proxy, proxy, name
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, providerName := range names {
		provider := providers[providerName]
		if provider == nil {
			continue
		}
		seen := map[string]C.Proxy{}
		// Copy before sorting: provider refresh order must not reassign IDs and
		// callers must not mutate a provider-owned slice.
		providerProxies := append([]C.Proxy(nil), provider.Proxies()...)
		sort.SliceStable(providerProxies, func(i, j int) bool {
			if providerProxies[i] == nil {
				return providerProxies[j] != nil
			}
			if providerProxies[j] == nil {
				return false
			}
			return providerProxies[i].Name() < providerProxies[j].Name()
		})
		for _, proxy := range providerProxies {
			if proxy == nil {
				continue
			}
			if previous, ok := seen[proxy.Name()]; ok && previous != proxy {
				return nil, fmt.Errorf("provider %q has duplicate proxy name %q", providerName, proxy.Name())
			}
			seen[proxy.Name()] = proxy
			if _, exists := c.ids[proxy]; exists {
				continue
			}
			id := QualifiedProxyID(proxy.Name(), providerName, static)
			c.items[id], c.ids[proxy] = proxy, id
			c.aliases[proxy.Name()] = append(c.aliases[proxy.Name()], id)
		}
	}
	return c, nil
}

// Escape delimiters and every @ so generated IDs are injective and never start
// with the static-collision prefix. Adding/reordering other nodes cannot change
// an existing ID; only changing the static namespace can do so.
func QualifiedProxyID(name, provider string, static map[string]C.Proxy) string {
	escape := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "@", "\\@")
	id := escape.Replace(name) + " [" + escape.Replace(provider) + "]"
	for static[id] != nil {
		id = "@" + id
	}
	return id
}

// EventIDs is immutable and safe to read from synchronous core callbacks.
// The static objects also identify nodes shared by compatible providers.
type EventIDs struct {
	static    map[string]C.Proxy
	ids       map[C.Proxy]string
	providers map[string]P.ProxyProvider
}

func NewEventIDs(static map[string]C.Proxy, providers map[string]P.ProxyProvider) *EventIDs {
	e := &EventIDs{static: map[string]C.Proxy{}, ids: map[C.Proxy]string{}, providers: map[string]P.ProxyProvider{}}
	for name, proxy := range static {
		e.static[name], e.ids[proxy] = proxy, name
	}
	for name, provider := range providers {
		e.providers[name] = provider
	}
	return e
}
func (e *EventIDs) ID(proxy C.Proxy, name, provider string) string {
	if id := e.ids[proxy]; id != "" {
		return id
	}
	if p := e.providers[provider]; p != nil {
		for _, current := range p.Proxies() {
			if current == proxy {
				return QualifiedProxyID(name, provider, e.static)
			}
		}
	}
	return "" // cancelled configuration or replaced provider object
}

func (c *ProxyCatalog) Lookup(id string) (C.Proxy, error) {
	if proxy := c.items[id]; proxy != nil {
		return proxy, nil
	}
	if ids := c.aliases[id]; len(ids) == 1 {
		return c.items[ids[0]], nil
	} else if len(ids) > 1 {
		return nil, fmt.Errorf("ambiguous proxy %q; use a provider-qualified name", id)
	}
	return nil, fmt.Errorf("proxy %q not found", id)
}

func (c *ProxyCatalog) ID(proxy C.Proxy) string { return c.ids[proxy] }

func (c *ProxyCatalog) Items() map[string]C.Proxy {
	items := make(map[string]C.Proxy, len(c.items))
	for id, proxy := range c.items {
		items[id] = proxy
	}
	return items
}

// Select never chooses a provider node as a group. Official group setters use
// bare names, so ambiguous membership is explicitly rejected even with an ID.
func (c *ProxyCatalog) Select(groupName, id string) error {
	group := c.static[groupName]
	if group == nil {
		return fmt.Errorf("group %q not found", groupName)
	}
	selector, ok := group.Adapter().(outboundgroup.SelectAble)
	if !ok {
		return fmt.Errorf("group %q is not selectable", groupName)
	}
	if id == "" {
		selector.ForceSet("")
		return nil
	}
	proxy, err := c.Lookup(id)
	if err != nil {
		return err
	}
	members, ok := group.Adapter().(interface{ Proxies() []C.Proxy })
	if !ok {
		return fmt.Errorf("group %q does not expose membership", groupName)
	}
	count, present := 0, false
	for _, candidate := range members.Proxies() {
		if candidate.Name() == proxy.Name() {
			count++
		}
		if candidate == proxy {
			present = true
		}
	}
	if !present {
		return fmt.Errorf("proxy %q is not a current member of %q", id, groupName)
	}
	if count != 1 {
		return fmt.Errorf("group %q has ambiguous members named %q; selection was not changed", groupName, proxy.Name())
	}
	return selector.Set(proxy.Name())
}

func (c *ProxyCatalog) MarshalJSON() ([]byte, error) {
	result := make(map[string]map[string]json.RawMessage, len(c.items))
	for id, proxy := range c.items {
		data, err := json.Marshal(proxy)
		if err != nil {
			return nil, err
		}
		entry := map[string]json.RawMessage{}
		if err = json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		entry["name"], _ = json.Marshal(id)
		entry["id"], _ = json.Marshal(id)
		entry["displayName"], _ = json.Marshal(proxy.Name())
		if group, ok := proxy.Adapter().(outboundgroup.ProxyGroup); ok {
			all := []string{}
			selected := ""
			matches := 0
			now := group.Now()
			for _, child := range group.Proxies() {
				childID := c.ids[child]
				if childID == "" {
					return nil, fmt.Errorf("group %q changed while taking snapshot", id)
				}
				all = append(all, childID)
				if child.Name() == now {
					selected = childID
					matches++
				}
			}
			if matches != 1 {
				selected = ""
			}
			entry["all"], _ = json.Marshal(all)
			entry["now"], _ = json.Marshal(selected)
			entry["selectionAmbiguous"], _ = json.Marshal(matches > 1)
		}
		result[id] = entry
	}
	return json.Marshal(result)
}
