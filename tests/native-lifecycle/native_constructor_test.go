package main

import (
	"core/compat"
	t "core/tun"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Only native dependencies are substituted. The preparation script extracts
// StartTUN, StopTun, StopTunOwner and stopTunLocked verbatim from lib_linux.go.
var runLock sync.Mutex
var tunListener *t.Listener
var tunOwner *tunSession
var tunCleanupErr error
var listenerCleanupErr error
var protectionOwner atomic.Pointer[tunSession]
var runTime *time.Time
var isRunning bool
var currentConfig = &nativeConfig{}

type nativeConfig struct {
	General struct {
		Tun struct {
			Device, Stack string
			DNSHijack     []string
		}
	}
}

var stopListenersFn func() error
var stoppedCoreEvents, closedAllConnections int

func startKeepalive()               {}
func stopKeepalive()                {}
func stopCoreEvents()               { stoppedCoreEvents++ }
func handleCloseConnectionsUnLock() { closedAllConnections++ }

func stopListeners() error {
	if stopListenersFn != nil {
		return stopListenersFn()
	}
	return nil
}

func resetNativeTestState() {
	tunSessions.CancelAll()
	runLock.Lock()
	defer runLock.Unlock()
	tunListener = nil
	tunOwner = nil
	tunCleanupErr = nil
	listenerCleanupErr = nil
	protectionOwner.Store(nil)
	runTime = nil
	isRunning = false
	currentConfig = &nativeConfig{}
	stopListenersFn = nil
	stoppedCoreEvents, closedAllConnections = 0, 0
	compat.CancelForwardingFn = nil
	t.StartFn = nil
}

func prepareNativeTest(tst *testing.T) {
	tst.Helper()
	resetNativeTestState()
	tst.Cleanup(func() {
		_ = StopTun()
		// Reset test doubles only. Production deliberately has no API that can
		// discard an unconfirmed native cleanup result and permit another Start.
		resetNativeTestState()
	})
}

type observedWriteConn struct {
	net.Conn
	writeEntered chan struct{}
	once         sync.Once
}

func TestNativeStopPreservesManagementAndCancelsForwardingBeforeCleanup(tst *testing.T) {
	prepareNativeTest(tst)
	owner := tunSessions.NewSession(tunSessions.Generation())
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) { return &t.Listener{}, nil }
	if err := StartTUN(42, owner, func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	cancelled := false
	compat.CancelForwardingFn = func() {
		cancelled = true
		if owner.ctx.Err() == nil {
			tst.Error("native owner context is still active")
		}
	}
	stopListenersFn = func() error {
		if !cancelled {
			tst.Error("forwarding cleanup began before cancellation")
		}
		return nil
	}
	if err := StopTun(); err != nil {
		tst.Fatal(err)
	}
	if stoppedCoreEvents != 0 || closedAllConnections != 0 {
		tst.Fatal("forwarding Stop cancelled management events or globally closed connections")
	}
}

func (c *observedWriteConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.writeEntered) })
	return c.Conn.Write(data)
}

func TestNativeStopCancelsConstructorProtectionBeforeWaitingForRunLock(tst *testing.T) {
	prepareNativeTest(tst)
	owner := tunSessions.NewSession(tunSessions.Generation())
	requested := make(chan int64, 1)
	var closes atomic.Int32
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		// This executes inside the real StartTUN while runLock is held. No ArkTS
		// ACK will arrive: synchronous Stop must cancel first, then take the lock.
		err := protectOutboundSocketUntil(&lifecycleRawConn{}, owner.request, time.Hour, owner.done)
		if err == nil {
			tst.Error("cancelled constructor protection succeeded")
		}
		return &t.Listener{CloseFn: func() error { closes.Add(1); return nil }}, nil
	}
	started := make(chan error, 1)
	go func() { started <- StartTUN(42, owner, func(fd Fd) { requested <- fd.Id }) }()
	id := <-requested
	stopped := make(chan struct{})
	go func() { StopTun(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		tst.Fatal("Stop deadlocked on ArkTS ACK")
	}
	if err := <-started; err == nil {
		tst.Fatal("cancelled constructor returned ready")
	}
	if closes.Load() != 1 {
		tst.Fatalf("late successful listener closed %d times", closes.Load())
	}
	if tunListener != nil || tunSessions.Ready() {
		tst.Fatal("late constructor revived TUN")
	}
	acknowledgeProtectedSocket(id)
}

func TestNativeStopClosesBlockedProtectIPCWriteDuringConstruction(tst *testing.T) {
	prepareNativeTest(tst)
	var closes atomic.Int32
	requestHandler = func(r RpcRequest, fn func(RpcResult)) {
		_ = StartTUN(42, r.tunOwner, func(fd Fd) {
			data, _ := json.Marshal(fd)
			fn(RpcResult{Method: StartClash, Result: string(data)})
		})
	}
	constructing := make(chan struct{})
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		owner := protectionOwner.Load()
		close(constructing)
		_ = protectOutboundSocketUntil(&lifecycleRawConn{}, owner.request, time.Hour, owner.done)
		return &t.Listener{CloseFn: func() error { closes.Add(1); return nil }}, nil
	}
	server, client := net.Pipe()
	writeEntered := make(chan struct{})
	observed := &observedWriteConn{Conn: server, writeEntered: writeEntered}
	defer client.Close()
	handlerDone := make(chan struct{})
	go func() { handleConnection(observed); close(handlerDone) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	request, _ := json.Marshal(RpcRequest{Method: StartClash, Params: []any{42, tunSessions.StartToken()}})
	if _, err := client.Write(request); err != nil {
		tst.Fatal(err)
	}
	<-constructing
	<-writeEntered
	// Deliberately do not read the protect response: the actual transport Write
	// blocks until cancellation closes its owner connection.
	stopped := make(chan struct{})
	go func() { StopTun(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		tst.Fatal("Stop waited for blocked IPC write")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		tst.Fatal("cancelled IPC handler leaked")
	}
	if closes.Load() != 1 {
		tst.Fatalf("cancelled constructor cleanup count %d", closes.Load())
	}
}

func TestNativeOldOwnerDisconnectDoesNotCloseNewListener(tst *testing.T) {
	prepareNativeTest(tst)
	var closes atomic.Int32
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		return &t.Listener{CloseFn: func() error { closes.Add(1); return nil }}, nil
	}
	old := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(42, old, func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	StopTun()
	next := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(43, next, func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	StopTunOwner(old)
	if closes.Load() != 1 || !tunSessions.Ready() || tunOwner != next {
		tst.Fatal("old disconnect closed replacement")
	}
	StopTun()
	if closes.Load() != 2 {
		tst.Fatal("replacement was not closed exactly once")
	}
}

func TestNativeStopRetriesListenerCleanupWithoutClosingTUNAgain(tst *testing.T) {
	prepareNativeTest(tst)
	listenerErr := errors.New("proxy listener close failed")
	var closes, constructors, stops int
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		constructors++
		return &t.Listener{CloseFn: func() error { closes++; return nil }}, nil
	}
	stopListenersFn = func() error {
		stops++
		if stops == 1 {
			return listenerErr
		}
		return nil
	}
	owner := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(42, owner, func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	if err := StopTun(); !errors.Is(err, listenerErr) {
		tst.Fatalf("Stop did not report listener error: %v", err)
	}
	if closes != 1 || tunListener != nil || tunSessions.Ready() || protectionOwner.Load() != nil {
		tst.Fatal("listener failure prevented TUN shutdown or left a ready owner")
	}
	if err := StartTUN(43, tunSessions.NewSession(tunSessions.Generation()), func(Fd) {}); !errors.Is(err, listenerErr) {
		tst.Fatalf("Start ignored pending listener cleanup: %v", err)
	}
	if constructors != 1 {
		tst.Fatal("Start entered constructor while listener cleanup was pending")
	}
	if err := StopTun(); err != nil {
		tst.Fatalf("listener retry failed: %v", err)
	}
	if closes != 1 || tunOwner != nil {
		tst.Fatal("retry reclosed TUN or retained resolved owner")
	}
	if err := StartTUN(43, tunSessions.NewSession(tunSessions.Generation()), func(Fd) {}); err != nil {
		tst.Fatalf("Start remained blocked after confirmed cleanup: %v", err)
	}
}

func TestNativeStopKeepsUnconfirmedTUNCloseFailure(tst *testing.T) {
	prepareNativeTest(tst)
	tunErr := errors.New("TUN resource close failed")
	listenerErr := errors.New("proxy listener close failed")
	var closes, constructors, stops int
	listener := &t.Listener{CloseFn: func() error {
		closes++
		if closes == 1 {
			return tunErr
		}
		return nil // A repeated nil would not prove the first partial close succeeded.
	}}
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		constructors++
		return listener, nil
	}
	stopListenersFn = func() error {
		stops++
		if stops == 1 {
			return listenerErr
		}
		return nil
	}
	owner := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(42, owner, func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	if err := StopTun(); !errors.Is(err, tunErr) || !errors.Is(err, listenerErr) {
		tst.Fatalf("Stop lost a cleanup failure: %v", err)
	}
	if tunListener != listener || tunOwner != owner || tunSessions.Ready() || protectionOwner.Load() != nil {
		tst.Fatal("unconfirmed TUN resource lost ownership or remained ready")
	}
	if err := StopTun(); !errors.Is(err, tunErr) || errors.Is(err, listenerErr) {
		tst.Fatalf("Stop did not retain TUN uncertainty and retry proxy listeners: %v", err)
	}
	if err := StopTunOwner(owner); !errors.Is(err, tunErr) {
		tst.Fatalf("owner cleanup hid the sticky error: %v", err)
	}
	if err := StartTUN(43, tunSessions.NewSession(tunSessions.Generation()), func(Fd) {}); !errors.Is(err, tunErr) {
		tst.Fatalf("Start ignored unconfirmed TUN cleanup: %v", err)
	}
	if closes != 1 || constructors != 1 || stops != 3 {
		tst.Fatalf("unsafe retry or short-circuited listener cleanup: closes=%d constructors=%d stops=%d", closes, constructors, stops)
	}
}

func TestNativeCancelledSuccessfulConstructorReportsRollbackFailure(tst *testing.T) {
	prepareNativeTest(tst)
	closeErr := errors.New("cancelled TUN rollback failed")
	var closes int
	owner := tunSessions.NewSession(tunSessions.Generation())
	listener := &t.Listener{CloseFn: func() error { closes++; return closeErr }}
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		tunSessions.Cancel(owner)
		return listener, nil
	}
	err := StartTUN(42, owner, func(Fd) {})
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "cancelled") {
		tst.Fatalf("cancelled Start lost rollback failure: %v", err)
	}
	if tunListener != listener || tunOwner != owner || tunSessions.Ready() {
		tst.Fatal("cancelled constructor lost unresolved ownership")
	}
	if err := StopTun(); !errors.Is(err, closeErr) || closes != 1 {
		tst.Fatalf("Stop retried or hid uncertain rollback: err=%v closes=%d", err, closes)
	}
}

func TestNativeConstructorFailureWithoutHandleCannotReportCleanStop(tst *testing.T) {
	prepareNativeTest(tst)
	constructorErr := errors.New("stack construction failed after consuming fd")
	var constructors int
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		constructors++
		return nil, constructorErr
	}
	owner := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(42, owner, func(Fd) {}); !errors.Is(err, constructorErr) || !strings.Contains(err.Error(), "cannot be confirmed") {
		tst.Fatalf("constructor failure lost ownership uncertainty: %v", err)
	}
	if tunListener != nil || tunOwner != owner || tunSessions.Ready() || protectionOwner.Load() != nil {
		tst.Fatal("unexpected state after uncertain constructor failure")
	}
	if err := StopTun(); !errors.Is(err, constructorErr) {
		tst.Fatalf("empty listener was mistaken for successful cleanup: %v", err)
	}
	if err := StartTUN(43, tunSessions.NewSession(tunSessions.Generation()), func(Fd) {}); !errors.Is(err, constructorErr) || constructors != 1 {
		tst.Fatalf("Start retried after unconfirmed construction: err=%v calls=%d", err, constructors)
	}
}

func TestNativePreconditionRejectionDoesNotPoisonLaterStart(tst *testing.T) {
	prepareNativeTest(tst)
	var constructors, closes int
	t.StartFn = func(int, string, string, []string) (*t.Listener, error) {
		constructors++
		return &t.Listener{CloseFn: func() error { closes++; return nil }}, nil
	}
	owner := tunSessions.NewSession(tunSessions.Generation())
	if err := StartTUN(-1, owner, func(Fd) {}); err == nil {
		tst.Fatal("invalid descriptor accepted")
	}
	currentConfig = nil
	if err := StartTUN(42, owner, func(Fd) {}); err == nil {
		tst.Fatal("missing configuration accepted")
	}
	currentConfig = &nativeConfig{}
	tunSessions.Cancel(owner)
	if err := StartTUN(42, owner, func(Fd) {}); err == nil {
		tst.Fatal("cancelled owner accepted")
	}
	if err := StopTun(); err != nil || constructors != 0 {
		tst.Fatalf("precondition rejection became cleanup failure: err=%v calls=%d", err, constructors)
	}
	if err := StartTUN(42, tunSessions.NewSession(tunSessions.Generation()), func(Fd) {}); err != nil {
		tst.Fatal(err)
	}
	if err := StopTun(); err != nil {
		tst.Fatal(err)
	}
	if err := StopTun(); err != nil || closes != 1 {
		tst.Fatalf("successful Stop is not idempotent: err=%v closes=%d", err, closes)
	}
}
