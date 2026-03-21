package exchange

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/chat"
)

func TestTriggerMarketEventPublishesEnvelopeAndMovesPrice(t *testing.T) {
	service := NewService([]Ticker{{
		Symbol:         "TECH",
		Name:           "Tech",
		Sector:         "technology",
		InitialPrice:   1000,
		TickSize:       5,
		LiquidityUnits: 10000,
		WhaleThreshold: 500,
	}}, chat.NewService(8, nil), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := service.Subscribe(ctx)

	before, err := service.ChartSnapshot("TECH", 5)
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}

	result, err := service.TriggerMarketEvent(ctx, EventShockInput{
		Kind:          "random_event",
		EventName:     "Whale rumor",
		Message:       "Whale rumor lifts TECH",
		Global:        false,
		Symbol:        "TECH",
		MultiplierPct: 12,
		Duration:      30 * time.Second,
		OccurredAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("TriggerMarketEvent: %v", err)
	}
	if result.Prices["TECH"].CurrentPrice <= before.Price.CurrentPrice {
		t.Fatalf("expected event to lift price, before=%d after=%d", before.Price.CurrentPrice, result.Prices["TECH"].CurrentPrice)
	}

	select {
	case payload := <-feed:
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Message string `json:"message"`
				Symbol  string `json:"symbol"`
				Kind    string `json:"kind"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		if env.Type != "market.event" {
			t.Fatalf("expected market.event envelope, got %q", env.Type)
		}
		if env.Payload.Message != "Whale rumor lifts TECH" || env.Payload.Symbol != "TECH" || env.Payload.Kind != "random_event" {
			t.Fatalf("unexpected event payload: %+v", env.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for market.event publication")
	}
}
