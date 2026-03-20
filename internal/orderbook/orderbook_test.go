package orderbook

import (
	"testing"
	"time"
)

func TestBookPlaceMatchesPriceTimePriority(t *testing.T) {
	book := New("TECH", 100)

	first := Order{
		ID:         "bid-1",
		RequestID:  "req-1",
		PlayerID:   "player-a",
		PlayerRole: "Buyer",
		Symbol:     "TECH",
		Side:       SideBuy,
		Type:       OrderTypeLimit,
		Price:      101,
		Quantity:   10,
		CreatedAt:  time.Unix(100, 0),
	}
	if _, err := book.Place(first); err != nil {
		t.Fatalf("place first bid: %v", err)
	}

	second := first
	second.ID = "bid-2"
	second.RequestID = "req-2"
	second.Quantity = 5
	second.CreatedAt = time.Unix(200, 0)
	if _, err := book.Place(second); err != nil {
		t.Fatalf("place second bid: %v", err)
	}

	sell := Order{
		ID:         "ask-1",
		RequestID:  "req-3",
		PlayerID:   "player-seller",
		PlayerRole: "Holder",
		Symbol:     "TECH",
		Side:       SideSell,
		Type:       OrderTypeLimit,
		Price:      101,
		Quantity:   12,
		CreatedAt:  time.Unix(300, 0),
	}
	result, err := book.Place(sell)
	if err != nil {
		t.Fatalf("place sell: %v", err)
	}

	if got := len(result.Trades); got != 2 {
		t.Fatalf("expected 2 trades, got %d", got)
	}
	if result.Trades[0].BuyOrderID != "bid-1" {
		t.Fatalf("expected first trade against oldest bid, got %s", result.Trades[0].BuyOrderID)
	}
	if result.Trades[1].BuyOrderID != "bid-2" {
		t.Fatalf("expected second trade against next bid, got %s", result.Trades[1].BuyOrderID)
	}
}
