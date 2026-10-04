package main

// Run with the actual IPC transport and the portable bridge test doubles from
// clashbox-meta/scripts/prepare-bridge-tests.py.
import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestIPCStartReceivedBeforeStopDoesNotAcquireNewGeneration(t *testing.T) {
	tunSessions.CancelAll()
	defer tunSessions.CancelAll()
	handled := make(chan bool, 1)
	requestHandler = func(r RpcRequest, fn func(RpcResult)) {
		handled <- r.tunOwner.Valid()
	}
	server, client := net.Pipe()
	defer client.Close()
	finished := make(chan struct{})
	go func() { handleConnection(server); close(finished) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	token := tunSessions.StartToken()
	if _, err := client.Write([]byte(`{"method":13,"params":[`)); err != nil {
		t.Fatal(err)
	}
	// The decoder is already inside this request; Stop invalidates it before
	// the descriptor finishes arriving and before dispatch calls StartTUN.
	tunSessions.CancelAll()
	if _, err := client.Write([]byte(`42,"` + token + `"]}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case valid := <-handled:
		if valid {
			t.Fatal("delayed request acquired post-Stop owner")
		}
	case <-time.After(time.Second):
		t.Fatal("request was not dispatched")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled handler leaked")
	}
}

func TestIPCDisconnectCancelsOwnerWhileConstructorIsPending(t *testing.T) {
	tunSessions.CancelAll()
	defer tunSessions.CancelAll()
	constructing := make(chan *tunSession, 1)
	requestHandler = func(r RpcRequest, fn func(RpcResult)) {
		if !tunSessions.Reserve(r.tunOwner, func(Fd) {}) {
			t.Error("reserve failed")
			return
		}
		constructing <- r.tunOwner
		<-r.tunOwner.done
		if tunSessions.Commit(r.tunOwner) {
			t.Error("disconnected constructor published TUN")
		}
	}
	server, client := net.Pipe()
	defer client.Close()
	finished := make(chan struct{})
	go func() { handleConnection(server); close(finished) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	request, _ := json.Marshal(RpcRequest{Method: StartClash, Params: []any{42, tunSessions.StartToken()}})
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	owner := <-constructing
	_ = client.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("disconnect waited for constructor ACK")
	}
	if owner.Valid() || tunSessions.Ready() {
		t.Fatal("disconnected session remained active")
	}
}

func TestIPCStopClosesProtectionStreamAndCancelsOwner(t *testing.T) {
	tunSessions.CancelAll()
	defer tunSessions.CancelAll()
	ready := make(chan *tunSession, 1)
	requestHandler = func(r RpcRequest, fn func(RpcResult)) {
		if !tunSessions.Reserve(r.tunOwner, func(Fd) {}) || !tunSessions.Commit(r.tunOwner) {
			t.Error("start failed")
			return
		}
		fn(RpcResult{Result: "tun-ready"})
		ready <- r.tunOwner
	}
	server, client := net.Pipe()
	defer client.Close()
	finished := make(chan struct{})
	go func() { handleConnection(server); close(finished) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	request, _ := json.Marshal(RpcRequest{Method: StartClash, Params: []any{42, tunSessions.StartToken()}})
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	if _, err := client.Read(buffer); err != nil {
		t.Fatal(err)
	}
	owner := <-ready
	tunSessions.CancelAll()
	if _, err := client.Read(buffer); err == nil {
		t.Fatal("Stop left protect stream open")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Stop leaked handler")
	}
	if owner.Valid() {
		t.Fatal("Stop left owner valid")
	}
}
