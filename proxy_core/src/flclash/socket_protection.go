package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Fd struct {
	Id    int64 `json:"id"`
	Value int64 `json:"value"`
}

var protectionRequests sync.Map
var protectionSequence int64

// An acknowledgement is sent only after the OS protect operation succeeds.
func acknowledgeProtectedSocket(id int64) {
	if value, ok := protectionRequests.Load(id); ok {
		select {
		case value.(chan struct{}) <- struct{}{}:
		default:
		}
	}
}

func protectOutboundSocket(conn syscall.RawConn, request func(Fd), timeout time.Duration) error {
	var protectErr error
	err := conn.Control(func(fd uintptr) {
		id := atomic.AddInt64(&protectionSequence, 1)
		done := make(chan struct{}, 1)
		protectionRequests.Store(id, done)
		defer protectionRequests.Delete(id)
		request(Fd{Id: id, Value: int64(fd)})
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			protectErr = errors.New("socket protection was not acknowledged; outbound connection rejected")
		}
	})
	if err != nil {
		return err
	}
	return protectErr
}
