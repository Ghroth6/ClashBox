package main

import (
	"core/compat"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

var requestHistory = compat.NewHistory[*statistic.TrackerInfo](1000)
var messages compat.MessageBus
var eventMu sync.Mutex
var eventGeneration atomic.Uint64
var eventUnsubscribe []func()
var eventIDs atomic.Pointer[compat.EventIDs]

// Called under runLock; callbacks deliberately never acquire runLock.
func startCoreEvents() {
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(eventUnsubscribe) != 0 {
		return
	}
	generation := eventGeneration.Add(1)
	active := func() bool { return eventGeneration.Load() == generation }
	eventUnsubscribe = []func(){
		adapter.SubscribeURLTests(func(event adapter.URLTestEvent) {
			eventMu.Lock()
			defer eventMu.Unlock()
			if !active() {
				return
			}
			ids := eventIDs.Load()
			if ids == nil {
				return
			}
			name := ids.ID(event.Proxy, event.Name, event.ProviderName)
			if name == "" {
				return
			}
			delay := int32(-1)
			if event.Succeeded {
				delay = int32(event.Delay)
			}
			sendMessage(Message{Type: DelayMessage, Data: &Delay{Name: name, Value: delay}})
		}),
		statistic.DefaultManager.SubscribeConnections(func(tracker statistic.Tracker) {
			eventMu.Lock()
			defer eventMu.Unlock()
			if !active() {
				return
			}
			requestHistory.Add(tracker.Info())
			sendMessage(Message{Type: RequestMessage, Data: tracker.Info()})
		}),
		executor.SubscribeProviderInitialization(func(event executor.ProviderInitializationEvent) {
			eventMu.Lock()
			defer eventMu.Unlock()
			if !active() {
				return
			}
			errText := ""
			if event.Err != nil {
				errText = event.Err.Error()
			}
			sendMessage(Message{Type: LoadedMessage, Data: map[string]any{
				"name": event.Name, "type": event.Type.String(), "generation": event.Generation,
				"success": event.Err == nil, "error": errText,
			}})
		}),
	}
}

func stopCoreEvents() {
	eventMu.Lock()
	eventGeneration.Add(1)
	callbacks := eventUnsubscribe
	eventUnsubscribe = nil
	eventMu.Unlock()
	for _, unsubscribe := range callbacks {
		unsubscribe()
	}
}
