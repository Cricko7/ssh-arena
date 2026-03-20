package chat

import (
	"context"
	"testing"
	"time"
)

func TestBroadcastDeliversJSONToSubscribers(t *testing.T) {
	service := NewService(10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := service.Subscribe(ctx)
	_, err := service.Broadcast(context.Background(), Message{
		PlayerID: "player-1",
		Username: "alice",
		Role:     "Buyer",
		Body:     "hello arena",
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	select {
	case msg := <-ch:
		if msg == "" {
			t.Fatal("expected non-empty json message")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chat message")
	}
}
