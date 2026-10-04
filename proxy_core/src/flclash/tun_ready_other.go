//go:build !ohos

package main

// The native owner contract is specific to the OHOS service. Keep the existing
// listener startup semantics on other platforms, including legacy Android.
func systemTUNReadyLocked() bool { return true }
