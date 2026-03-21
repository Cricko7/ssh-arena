package state

import (
	"testing"
	"time"
)

func TestPerformanceStoreLeaderboardUsesPnLAndTurnover(t *testing.T) {
	now := time.Date(2026, 3, 21, 15, 0, 0, 0, time.UTC)
	window := time.Hour

	store := &PerformanceStore{}
	store.snapshots = []EquitySnapshot{
		{PlayerID: "alice", Username: "alice", Role: "Buyer", TotalEquity: 1000, CapturedAt: now.Add(-2 * time.Hour)},
		{PlayerID: "alice", Username: "alice", Role: "Buyer", TotalEquity: 1250, CapturedAt: now.Add(-5 * time.Minute)},
		{PlayerID: "bob", Username: "bob", Role: "Holder", TotalEquity: 1000, CapturedAt: now.Add(-2 * time.Hour)},
		{PlayerID: "bob", Username: "bob", Role: "Holder", TotalEquity: 1180, CapturedAt: now.Add(-10 * time.Minute)},
	}
	trades := []TradeRecord{
		{ID: "t1", BuyerID: "alice", SellerID: "bob", Quantity: 2, Notional: 200, ExecutedAt: now.Add(-20 * time.Minute)},
		{ID: "t2", BuyerID: "alice", SellerID: "bob", Quantity: 3, Notional: 330, ExecutedAt: now.Add(-15 * time.Minute)},
	}

	entries := store.Leaderboard(window, now, trades, 10)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].PlayerID != "alice" {
		t.Fatalf("expected alice first, got %+v", entries[0])
	}
	if entries[0].PnL != 250 {
		t.Fatalf("expected alice pnl 250, got %d", entries[0].PnL)
	}
	if entries[0].TradeCount != 2 || entries[0].BuyVolume != 5 || entries[0].Turnover != 530 {
		t.Fatalf("unexpected alice stats: %+v", entries[0])
	}
	if entries[1].PlayerID != "bob" || entries[1].SellVolume != 5 {
		t.Fatalf("unexpected bob stats: %+v", entries[1])
	}
}
