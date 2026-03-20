package exchange

import (
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/orderbook"
)

func TestPriceEngineWhaleBuyMovesPriceUp(t *testing.T) {
	engine := NewPriceEngine([]Ticker{{Symbol: "TECH", InitialPrice: 1000, TickSize: 5, LiquidityUnits: 1000, WhaleThreshold: 100}})
	price, err := engine.Apply("TECH", []orderbook.Trade{{Quantity: 200, Price: 1010, AggressorSide: orderbook.SideBuy, BuyerRole: "Whale", ExecutedAt: time.Now().UTC()}}, orderbook.Snapshot{
		Symbol: "TECH",
		Bids:   []orderbook.Level{{Price: 1000, Quantity: 500}},
		Asks:   []orderbook.Level{{Price: 1010, Quantity: 200}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if price.CurrentPrice <= 1000 {
		t.Fatalf("expected whale buying pressure to lift price, got %d", price.CurrentPrice)
	}
}

func TestPriceEngineMeanRevertsWithoutTrades(t *testing.T) {
	engine := NewPriceEngine([]Ticker{{Symbol: "TECH", InitialPrice: 1000, TickSize: 5, LiquidityUnits: 1000, WhaleThreshold: 100}})
	_, _ = engine.Apply("TECH", []orderbook.Trade{{Quantity: 100, Price: 1100, AggressorSide: orderbook.SideBuy, ExecutedAt: time.Now().UTC()}}, orderbook.Snapshot{
		Symbol: "TECH",
		Bids:   []orderbook.Level{{Price: 1095, Quantity: 100}},
		Asks:   []orderbook.Level{{Price: 1100, Quantity: 80}},
	})
	price, err := engine.Apply("TECH", nil, orderbook.Snapshot{
		Symbol: "TECH",
		Bids:   []orderbook.Level{{Price: 990, Quantity: 400}},
		Asks:   []orderbook.Level{{Price: 995, Quantity: 420}},
	})
	if err != nil {
		t.Fatalf("apply idle step: %v", err)
	}
	if price.CurrentPrice >= 1100 {
		t.Fatalf("expected idle market to mean-revert from spike, got %d", price.CurrentPrice)
	}
}
