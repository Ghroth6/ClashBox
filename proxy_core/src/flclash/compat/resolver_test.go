package compat

import (
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
)

type observedResolver struct {
	resolver.Resolver
	name    string
	events  chan<- string
	release <-chan struct{}
	done    *sync.WaitGroup
}

func (r *observedResolver) operation(name string) {
	defer r.done.Done()
	r.events <- r.name + ":" + name
	<-r.release
}
func (r *observedResolver) ClearCache()      { r.operation("clear") }
func (r *observedResolver) ResetConnection() { r.operation("reset") }

func TestFlushDNSIncludesSystemAndIsAsynchronous(t *testing.T) {
	for _, withDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "system-only", true: "configured-and-system"}[withDefault], func(t *testing.T) {
			oldDefault, oldSystem := resolver.DefaultResolver, resolver.SystemResolver
			events := make(chan string, 4)
			release := make(chan struct{})
			var done sync.WaitGroup
			count := 2
			if withDefault {
				count = 4
			}
			done.Add(count)
			t.Cleanup(func() {
				close(release)
				finished := make(chan struct{})
				go func() { done.Wait(); close(finished) }()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("resolver operations did not finish")
				}
				resolver.DefaultResolver, resolver.SystemResolver = oldDefault, oldSystem
			})
			resolver.SystemResolver = &observedResolver{name: "system", events: events, release: release, done: &done}
			resolver.DefaultResolver = nil
			if withDefault {
				resolver.DefaultResolver = &observedResolver{name: "configured", events: events, release: release, done: &done}
			}
			returned := make(chan struct{})
			go func() { FlushDNS(); close(returned) }()
			select {
			case <-returned:
			case <-time.After(2 * time.Second):
				t.Fatal("flush waited for resolver completion")
			}
			seen := make(map[string]bool)
			for i := 0; i < count; i++ {
				select {
				case event := <-events:
					seen[event] = true
				case <-time.After(2 * time.Second):
					t.Fatal("missing resolver operation")
				}
			}
			if !seen["system:clear"] || !seen["system:reset"] ||
				(withDefault && (!seen["configured:clear"] || !seen["configured:reset"])) {
				t.Fatalf("wrong resolver operations: %v", seen)
			}
		})
	}
}
