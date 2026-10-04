package main

import (
	t "core/tun"
	"encoding/json"
	"net"
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

func startKeepalive()               {}
func stopKeepalive()                {}
func stopCoreEvents()               {}
func stopListeners()                {}
func handleCloseConnectionsUnLock() {}

type observedWriteConn struct {
	net.Conn
	writeEntered chan struct{}
	once         sync.Once
}

func (c *observedWriteConn) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.writeEntered) })
	return c.Conn.Write(data)
}

func TestNativeStopCancelsConstructorProtectionBeforeWaitingForRunLock(tst *testing.T) {
	StopTun()
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
	StopTun()
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
	StopTun()
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
