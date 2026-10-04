//go:build ohos

package main

func systemTUNReadyLocked() bool { return tunSessions.Ready() }
