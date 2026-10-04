package compat

import "github.com/metacubex/mihomo/component/resolver"

// FlushDNS schedules cache clearing and connection reset on both the configured
// and system resolvers. The upstream operations are asynchronous: this function
// is not a completion barrier and does not change system DNS configuration.
func FlushDNS() {
	resolver.ClearCache()
	resolver.ResetConnection()
}
