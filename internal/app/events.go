package app

import "sync"

type eventHub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subscribers: make(map[chan struct{}]struct{})}
}

func (hub *eventHub) Subscribe() (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	hub.mu.Lock()
	hub.subscribers[channel] = struct{}{}
	hub.mu.Unlock()
	return channel, func() {
		hub.mu.Lock()
		delete(hub.subscribers, channel)
		hub.mu.Unlock()
	}
}

func (hub *eventHub) Notify() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for subscriber := range hub.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}
