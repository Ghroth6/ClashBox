package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTUNRequestCapturedBeforeStopCannotReserveAfterStop(t *testing.T) {
	var lifecycle tunLifecycle
	generation := lifecycle.Generation()
	lifecycle.CancelAll()
	late := lifecycle.NewSession(generation)
	if late.Valid() || lifecycle.Reserve(late, func(Fd) {}) {
		t.Fatal("request received before Stop revived TUN")
	}
}

func TestTUNQueuedStartIsCancelledBeforeConstructor(t *testing.T) {
	var lifecycle tunLifecycle
	owner := lifecycle.NewSession(lifecycle.Generation())
	lifecycle.CancelAll()
	if lifecycle.Reserve(owner, func(Fd) {}) {
		t.Fatal("queued start survived Stop")
	}
}

func TestTUNTokenIssuedBeforeStopIsRejectedEvenWhenAcceptedAfterStop(t *testing.T) {
	var lifecycle tunLifecycle
	token := lifecycle.StartToken()
	lifecycle.CancelAll()
	owner := lifecycle.SessionForToken(token, lifecycle.Generation())
	if owner.Valid() {
		t.Fatal("queued pre-Stop request accepted a new generation")
	}
	current := lifecycle.SessionForToken(lifecycle.StartToken(), lifecycle.Generation())
	if !current.Valid() {
		t.Fatal("fresh token rejected")
	}
	if lifecycle.SessionForToken("invalid", lifecycle.Generation()).Valid() {
		t.Fatal("malformed token accepted")
	}
}

func TestTUNBootstrapAllowedOnlyBeforeFirstReservation(t *testing.T) {
	var lifecycle tunLifecycle
	if lifecycle.ProtectionRequired() {
		t.Fatal("initial provider bootstrap blocked")
	}
	owner := lifecycle.NewSession(lifecycle.Generation())
	if !lifecycle.Reserve(owner, func(Fd) {}) {
		t.Fatal("reserve failed")
	}
	lifecycle.CancelAll()
	if !lifecycle.ProtectionRequired() {
		t.Fatal("native Stop incorrectly assumed system VPN routes were destroyed")
	}
}

func TestTUNStopDuringConstructionPreventsPublish(t *testing.T) {
	var lifecycle tunLifecycle
	owner := lifecycle.NewSession(lifecycle.Generation())
	if !lifecycle.Reserve(owner, func(Fd) {}) {
		t.Fatal("reserve failed")
	}
	if lifecycle.Ready() {
		t.Fatal("constructor was treated as ready")
	}
	lifecycle.CancelAll()
	if lifecycle.Commit(owner) || lifecycle.Ready() {
		t.Fatal("cancelled constructor published a TUN")
	}
}

func TestTUNOldOwnerDisconnectCannotCancelReplacement(t *testing.T) {
	var lifecycle tunLifecycle
	old := lifecycle.NewSession(lifecycle.Generation())
	if !lifecycle.Reserve(old, func(Fd) {}) || !lifecycle.Commit(old) {
		t.Fatal("old start failed")
	}
	lifecycle.CancelAll()
	next := lifecycle.NewSession(lifecycle.Generation())
	if !lifecycle.Reserve(next, func(Fd) {}) || !lifecycle.Commit(next) {
		t.Fatal("replacement start failed")
	}
	lifecycle.Cancel(old)
	lifecycle.Cancel(old)
	if !next.Valid() || !lifecycle.Ready() {
		t.Fatal("old disconnect cancelled replacement")
	}
}

func TestTUNOnlyOneConstructorOwnsProtection(t *testing.T) {
	var lifecycle tunLifecycle
	first := lifecycle.NewSession(lifecycle.Generation())
	second := lifecycle.NewSession(lifecycle.Generation())
	if !lifecycle.Reserve(first, func(Fd) {}) {
		t.Fatal("reserve failed")
	}
	if lifecycle.Reserve(second, func(Fd) {}) {
		t.Fatal("two owners acquired TUN")
	}
	lifecycle.Cancel(second)
	if !lifecycle.Commit(first) {
		t.Fatal("rejected second request cancelled first")
	}
}

func TestTUNConcurrentStopAndConstructorNeverLeaveReadySession(t *testing.T) {
	for i := 0; i < 100; i++ {
		var lifecycle tunLifecycle
		owner := lifecycle.NewSession(lifecycle.Generation())
		if !lifecycle.Reserve(owner, func(Fd) {}) {
			t.Fatal("reserve failed")
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); lifecycle.Commit(owner) }()
		go func() { defer wg.Done(); lifecycle.CancelAll() }()
		wg.Wait()
		if lifecycle.Ready() || owner.Valid() {
			t.Fatal("concurrent Stop left TUN ready")
		}
	}
}

type lifecycleRawConn struct{ entered bool }

func (r *lifecycleRawConn) Control(fn func(uintptr)) error { r.entered = true; fn(42); return nil }
func (*lifecycleRawConn) Read(func(uintptr) bool) error    { panic("unexpected Read") }
func (*lifecycleRawConn) Write(func(uintptr) bool) error   { panic("unexpected Write") }

func TestTUNCancellationReleasesProtectionWithoutWaitingForACK(t *testing.T) {
	var lifecycle tunLifecycle
	owner := lifecycle.NewSession(lifecycle.Generation())
	requested := make(chan int64, 1)
	result := make(chan error, 1)
	go func() {
		result <- protectOutboundSocketUntil(&lifecycleRawConn{}, func(fd Fd) { requested <- fd.Id }, time.Hour, owner.done)
	}()
	id := <-requested
	lifecycle.CancelAll()
	select {
	case err := <-result:
		if !errors.Is(err, errProtectionUnavailable) {
			t.Fatalf("unexpected cancellation error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop waited for ArkTS ACK")
	}
	if _, ok := protectionRequests.Load(id); ok {
		t.Fatal("cancelled protection request leaked")
	}
	acknowledgeProtectedSocket(id)
}

func TestTUNCancellationWinsOverQueuedProtectACK(t *testing.T) {
	var lifecycle tunLifecycle
	owner := lifecycle.NewSession(lifecycle.Generation())
	err := protectOutboundSocketUntil(&lifecycleRawConn{}, func(fd Fd) {
		acknowledgeProtectedSocket(fd.Id)
		lifecycle.Cancel(owner)
	}, time.Second, owner.done)
	if !errors.Is(err, errProtectionUnavailable) {
		t.Fatalf("cancelled owner ACK accepted: %v", err)
	}
}

func TestTUNNoProtectionOwnerRejectsBeforeTouchingDescriptor(t *testing.T) {
	raw := &lifecycleRawConn{}
	if err := protectOutboundSocketUntil(raw, nil, time.Second, nil); !errors.Is(err, errProtectionUnavailable) || raw.entered {
		t.Fatalf("missing protection callback touched descriptor: %v", err)
	}
	cancelled := make(chan struct{})
	close(cancelled)
	if err := protectOutboundSocketUntil(raw, func(Fd) { t.Fatal("stale protect request") }, time.Second, cancelled); !errors.Is(err, errProtectionUnavailable) || raw.entered {
		t.Fatalf("cancelled protection touched descriptor: %v", err)
	}
}
