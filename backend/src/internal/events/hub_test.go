package events

import "testing"

func TestPublishReachesSubscribers(t *testing.T) {
	hub := NewHub()
	a, cancelA := hub.Subscribe()
	b, cancelB := hub.Subscribe()
	defer cancelB()

	hub.Publish(Event{Type: TorrentCompleted, Data: "x"})
	if got := <-a; got.Type != TorrentCompleted {
		t.Fatalf("a got %+v", got)
	}
	if got := <-b; got.Type != TorrentCompleted {
		t.Fatalf("b got %+v", got)
	}

	cancelA()
	cancelA()
	if _, open := <-a; open {
		t.Fatal("channel should be closed after cancel")
	}
	hub.Publish(Event{Type: TorrentCompleted})
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	hub := NewHub()
	_, cancel := hub.Subscribe()
	defer cancel()
	for i := 0; i < subscriberBuffer*3; i++ {
		hub.Publish(Event{Type: TorrentCompleted})
	}
}
