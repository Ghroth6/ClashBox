//go:build ohos

package main

func systemTUNReadyLocked() bool { return tunSessions.Ready() }

func systemTUNActiveLocked() bool { return tunOwner != nil || tunListener != nil }
