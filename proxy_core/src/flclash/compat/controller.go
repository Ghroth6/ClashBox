package compat

import "github.com/metacubex/mihomo/hub/route"

// ConfigureEmbeddedController must run before any controller server is created.
// Config changes must pass through the wrapper's VPN/TUN validation. Official
// embed mode keeps queries and proxy selection, while disabling direct config
// and rule mutation and process restart through the controller.
func ConfigureEmbeddedController() {
	route.SetEmbedMode(true)
}
