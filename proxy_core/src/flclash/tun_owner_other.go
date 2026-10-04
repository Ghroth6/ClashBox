//go:build !ohos && !android

package main

const systemOwnsTUN = false

func systemTUNReadyLocked() bool { return true }
