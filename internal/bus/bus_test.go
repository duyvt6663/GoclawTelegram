package bus

import (
	"testing"
	"time"
)

func TestBroadcastAllowsSubscriberMutation(t *testing.T) {
	mb := New()
	done := make(chan struct{})

	mb.Subscribe("first", func(Event) {
		mb.Subscribe("second", func(Event) {})
		mb.Unsubscribe("second")
		close(done)
	})

	mb.Broadcast(Event{Name: "test"})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast deadlocked while subscriber mutated subscriptions")
	}
}
