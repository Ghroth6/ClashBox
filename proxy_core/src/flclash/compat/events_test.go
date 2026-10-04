package compat

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHistoryBoundedOrderClearAndSnapshotIsolation(t *testing.T) {
	h := NewHistory[int](3)
	for i := 1; i <= 5; i++ {
		h.Add(i)
	}
	first := h.Snapshot()
	if !reflect.DeepEqual(first, []int{3, 4, 5}) {
		t.Fatalf("history kept wrong entries/order: %v", first)
	}
	first[0] = 100
	if !reflect.DeepEqual(h.Snapshot(), []int{3, 4, 5}) {
		t.Fatal("snapshot slice aliases history")
	}
	h.Clear()
	if len(h.Snapshot()) != 0 || !reflect.DeepEqual(first, []int{100, 4, 5}) {
		t.Fatal("clear changed a prior snapshot or retained history")
	}
	h.Add(6)
	if !reflect.DeepEqual(h.Snapshot(), []int{6}) {
		t.Fatal("history cannot be reused after clear")
	}
}

func TestHistoryConcurrentAddSnapshotAndClear(t *testing.T) {
	h := NewHistory[int](32)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for j := 0; j < 1000; j++ {
				h.Add(worker*1000 + j)
				snapshot := h.Snapshot()
				if len(snapshot) > 32 {
					t.Error("concurrent writers exceeded the history limit")
				}
				if len(snapshot) > 0 {
					snapshot[0] = -1
				}
				if j%17 == 0 {
					h.Clear()
				}
			}
		}(i)
	}
	workers.Wait()
	for _, value := range h.Snapshot() {
		if value < 0 {
			t.Fatal("a mutated snapshot contaminated shared history")
		}
	}
	h.Clear()
	h.Add(42)
	if !reflect.DeepEqual(h.Snapshot(), []int{42}) {
		t.Fatal("clear after all writers was not definitive")
	}
}

func TestHistoryRejectsNonpositiveLimits(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid limit was accepted")
				}
			}()
			NewHistory[int](limit)
		})
	}
}

func TestMessageBusBroadcastAndIdempotentCancellation(t *testing.T) {
	var bus MessageBus
	one, cancelOne := bus.Subscribe()
	two, cancelTwo := bus.Subscribe()
	defer cancelTwo()
	bus.Publish("one")
	bus.Publish("two")
	for _, ch := range []<-chan string{one, two} {
		for _, expected := range []string{"one", "two"} {
			select {
			case got, ok := <-ch:
				if !ok || got != expected {
					t.Fatalf("message order/value changed: %q, %v", got, ok)
				}
			default:
				t.Fatal("publish did not queue the message")
			}
		}
	}
	cancelOne()
	cancelOne()
	bus.Publish("survivor")
	if _, ok := <-one; ok {
		t.Fatal("cancelled subscriber remained connected")
	}
	if got := <-two; got != "survivor" {
		t.Fatal("cancelling one subscriber changed another")
	}
}

func TestMessageBusFullSubscriberDisconnectsWithoutBlockingOthers(t *testing.T) {
	var bus MessageBus
	slow, cancelSlow := bus.Subscribe()
	defer cancelSlow()
	fast, cancelFast := bus.Subscribe()
	defer cancelFast()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 129; i++ {
			message := fmt.Sprint(i)
			bus.Publish(message)
			if got, ok := <-fast; !ok || got != message {
				done <- fmt.Errorf("fast subscriber lost ordered message %d: %q, %v", i, got, ok)
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("full subscriber blocked publishing")
	}
	for i := 0; i < 128; i++ {
		if got, ok := <-slow; !ok || got != fmt.Sprint(i) {
			t.Fatal("overflow altered already queued messages")
		}
	}
	if _, ok := <-slow; ok {
		t.Fatal("overflow silently dropped messages instead of disconnecting")
	}
	bus.Publish("still-connected")
	if got := <-fast; got != "still-connected" {
		t.Fatal("slow subscriber overflow disconnected the fast subscriber")
	}
}

func TestMessageBusConcurrentPublishSubscribeAndCancel(t *testing.T) {
	var bus MessageBus
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for j := 0; j < 200; j++ {
				ch, cancel := bus.Subscribe()
				bus.Publish(fmt.Sprintf("%d/%d", worker, j))
				cancel()
				cancel()
				for range ch {
				}
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent publish/cancel stalled")
	}
	ch, cancel := bus.Subscribe()
	defer cancel()
	bus.Publish("usable")
	if got, ok := <-ch; !ok || got != "usable" {
		t.Fatal("bus was not reusable after concurrent cancellation")
	}
}
