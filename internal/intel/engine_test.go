package intel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/orderbook"
)

type mockMarket struct {
	tickers  []string
	events   []exchange.EventShockInput
	snapshot exchange.ChartSnapshot
}

func (m *mockMarket) ListTickers() []string { return append([]string(nil), m.tickers...) }
func (m *mockMarket) ChartSnapshot(symbol string, depth int) (exchange.ChartSnapshot, error) {
	if m.snapshot.Symbol == "" {
		m.snapshot = exchange.ChartSnapshot{
			Symbol: symbol,
			Price:  exchange.PricePoint{Symbol: symbol, CurrentPrice: 1010, MoveBps: 120},
			OrderBook: orderbook.Snapshot{
				Symbol: symbol,
				Bids:   []orderbook.Level{{Price: 1000, Quantity: 40}},
				Asks:   []orderbook.Level{{Price: 1010, Quantity: 35}},
			},
		}
	}
	return m.snapshot, nil
}
func (m *mockMarket) TriggerMarketEvent(_ context.Context, input exchange.EventShockInput) (exchange.EventShockOutput, error) {
	m.events = append(m.events, input)
	return exchange.EventShockOutput{OccurredAt: time.Now().UTC()}, nil
}

type mockNotifier struct{ payloads map[string][]string }

func (n *mockNotifier) NotifyPlayer(playerID string, payload string) {
	if n.payloads == nil {
		n.payloads = map[string][]string{}
	}
	n.payloads[playerID] = append(n.payloads[playerID], payload)
}

func TestParseRangeSupportsNegativeSpan(t *testing.T) {
	got, err := parseRange("-12--4")
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if got.Min != -12 || got.Max != -4 {
		t.Fatalf("unexpected range: %+v", got)
	}
}

func TestBuyInsiderConsumesOnTrigger(t *testing.T) {
	market := &mockMarket{tickers: []string{"TECH"}}
	notifier := &mockNotifier{}
	engine, err := NewEngine(Config{Interval: time.Second}, []Definition{{
		ID:               "insider.tech",
		Kind:             KindInsider,
		Name:             "Insider <ticker>",
		Message:          "Public <ticker>",
		PrivateMessage:   "Private <ticker>",
		Chance:           100,
		Price:            1000,
		MarketMultiplier: "10-10",
		DurationSeconds:  30,
		LeadTimeSeconds:  0,
		Global:           false,
	}}, market, notifier)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	_, err = engine.Buy(context.Background(), "player-a", "insider.tech")
	if err != nil {
		t.Fatalf("Buy: %v", err)
	}
	engine.tick(context.Background())
	if len(notifier.payloads["player-a"]) != 1 {
		t.Fatalf("expected insider preview to be delivered once, got %d", len(notifier.payloads["player-a"]))
	}
	if len(market.events) != 1 {
		t.Fatalf("expected public event to be triggered, got %d", len(market.events))
	}
	engine.tick(context.Background())
	if len(notifier.payloads["player-a"]) != 1 {
		t.Fatalf("expected entitlement to be consumed after one trigger")
	}
}

func TestPaidAnalyticsReturnsReport(t *testing.T) {
	market := &mockMarket{tickers: []string{"TECH"}}
	engine, err := NewEngine(Config{Interval: time.Second}, []Definition{{
		ID:               "analytics.tech",
		Kind:             KindPaidAnalysis,
		Name:             "Tech report",
		Message:          "Watch <ticker>",
		Price:            500,
		MarketMultiplier: "5-12",
		Global:           false,
	}}, market, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	result, err := engine.Buy(context.Background(), "player-a", "analytics.tech")
	if err != nil {
		t.Fatalf("Buy: %v", err)
	}
	if result.Cost != 500 {
		t.Fatalf("unexpected cost: %d", result.Cost)
	}
	if !strings.Contains(result.PayloadJSON, "intel.analytics.report") {
		t.Fatalf("expected analytics payload, got %s", result.PayloadJSON)
	}
}
