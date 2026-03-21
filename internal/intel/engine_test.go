package intel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/marketevents"
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

func TestBuyInsiderGetsNextRandomEventPreview(t *testing.T) {
	market := &mockMarket{tickers: []string{"TECH"}}
	notifier := &mockNotifier{}
	engine, err := NewEngine(Config{Interval: time.Second}, []Definition{{
		ID:              "insider.next_event",
		Kind:            KindInsider,
		Name:            "Next event preview",
		Description:     "See the next random event 30 seconds early.",
		PrivateMessage:  "Preview: <ticker> will move soon.",
		Price:           1000,
		LeadTimeSeconds: 1,
	}}, market, notifier)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	result, err := engine.Buy(context.Background(), "player-a", "insider.next_event")
	if err != nil {
		t.Fatalf("Buy: %v", err)
	}
	if !strings.Contains(result.PayloadJSON, "intel.purchase.armed") {
		t.Fatalf("expected armed payload, got %s", result.PayloadJSON)
	}

	event := marketevents.PlannedEvent{
		ID:              "evt-1",
		Kind:            "random_event",
		EventName:       "Big TECH move",
		Message:         "TECH is about to squeeze higher.",
		Symbol:          "TECH",
		MultiplierPct:   15,
		DurationSeconds: 60,
		ScheduledAt:     time.Now().UTC().Add(1100 * time.Millisecond),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.ScheduleNextRandomEvent(ctx, event)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(notifier.payloads["player-a"]) > 0 {
			if !strings.Contains(notifier.payloads["player-a"][0], "intel.insider.preview") {
				t.Fatalf("unexpected preview payload: %s", notifier.payloads["player-a"][0])
			}
			if !strings.Contains(notifier.payloads["player-a"][0], "TECH") {
				t.Fatalf("expected ticker in preview payload: %s", notifier.payloads["player-a"][0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected insider preview notification")
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
