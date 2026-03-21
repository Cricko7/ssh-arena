package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTradeStoreQueryFiltersNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.json")
	store, err := LoadTradeStore(path, 10)
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	base := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	records := []TradeRecord{
		{ID: "t1", Symbol: "TECH", BuyerID: "alice", SellerID: "bob", ExecutedAt: base.Add(-2 * time.Minute)},
		{ID: "t2", Symbol: "CRYPTO", BuyerID: "carol", SellerID: "alice", ExecutedAt: base.Add(-1 * time.Minute)},
		{ID: "t3", Symbol: "TECH", BuyerID: "alice", SellerID: "dave", ExecutedAt: base},
	}
	if err := store.Append(records); err != nil {
		t.Fatalf("append trades: %v", err)
	}

	got := store.Query(TradeQuery{PlayerID: "alice", Symbol: "TECH", Limit: 5})
	if len(got) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(got))
	}
	if got[0].ID != "t3" || got[1].ID != "t1" {
		t.Fatalf("unexpected order: %+v", got)
	}
}
