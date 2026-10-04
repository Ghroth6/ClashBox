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
	return protectOutboundSocketUntil(conn, request, timeout, nil)
}

var errProtectionUnavailable = errors.New("socket protection owner is unavailable; outbound connection rejected")

func protectOutboundSocketUntil(conn syscall.RawConn, request func(Fd), timeout time.Duration, cancelled <-chan struct{}) error {
	if request == nil {
		return errProtectionUnavailable
	}
	select {
	case <-cancelled:
		return errProtectionUnavailable
	default:
	}
	var protectErr error
	err := conn.Control(func(fd uintptr) {
		select {
		case <-cancelled:
			protectErr = errProtectionUnavailable
			return
		default:
		}
		id := atomic.AddInt64(&protectionSequence, 1)
		done := make(chan struct{}, 1)
		protectionRequests.Store(id, done)
		defer protectionRequests.Delete(id)
		request(Fd{Id: id, Value: int64(fd)})
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-cancelled:
			protectErr = errProtectionUnavailable
		case <-timer.C:
			protectErr = errors.New("socket protection was not acknowledged; outbound connection rejected")
		}
		// Cancellation wins over an ACK that was already queued by the old owner.
		select {
		case <-cancelled:
			protectErr = errProtectionUnavailable
		default:
		}
	})
	if err != nil {
		return err
	}
	return protectErr
}
