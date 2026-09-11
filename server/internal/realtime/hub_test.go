package realtime

import (
	"sync"
	"testing"
)

func TestHubPublishDuringSubscriptionClose(t *testing.T) {
	hub := NewHub()
	anchor := hub.Subscribe(1)
	defer anchor.Close()

	for i := 0; i < 200; i++ {
		sub := hub.Subscribe(1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			hub.Publish(1, ClientNotification{Type: NotificationNewMessage, SessionID: 1})
		}()
		go func() {
			defer wg.Done()
			sub.Close()
		}()
		wg.Wait()
	}
}

func TestHubAssignsIncreasingIDs(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe(1)
	defer sub.Close()

	hub.Publish(1, ClientNotification{Type: NotificationNewMessage})
	hub.Publish(1, ClientNotification{Type: NotificationNewMessage})
	first := <-sub.Notifications()
	second := <-sub.Notifications()
	if second.ID <= first.ID {
		t.Fatalf("event IDs must increase: first=%d second=%d", first.ID, second.ID)
	}
}
