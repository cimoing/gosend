package app

import (
	"testing"
	"time"
)

func TestEventHubNotifiesAndUnsubscribes(t *testing.T) {
	hub := newEventHub()
	events, unsubscribe := hub.Subscribe()
	hub.Notify()
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified")
	}

	unsubscribe()
	hub.Notify()
	select {
	case <-events:
		t.Fatal("unsubscribed channel received an event")
	default:
	}
}
