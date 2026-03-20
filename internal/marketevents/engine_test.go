package marketevents

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
)

type mockMarket struct {
	tickers []string
	calls   []exchange.EventShockInput
}

func (m *mockMarket) ListTickers() []string {
	return append([]string(nil), m.tickers...)
}

func (m *mockMarket) TriggerMarketEvent(_ context.Context, input exchange.EventShockInput) (exchange.EventShockOutput, error) {
	m.calls = append(m.calls, input)
	return exchange.EventShockOutput{}, nil
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		value string
		min   int
		max   int
	}{
		{value: "10-20", min: 10, max: 20},
		{value: "-10-10", min: -10, max: 10},
		{value: "-12--4", min: -12, max: -4},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			got, err := ParseRange(tc.value)
			if err != nil {
				t.Fatalf("ParseRange(%q): %v", tc.value, err)
			}
			if got.Min != tc.min || got.Max != tc.max {
				t.Fatalf("ParseRange(%q) = %+v, want min=%d max=%d", tc.value, got, tc.min, tc.max)
			}
		})
	}
}

func TestEngineTickTriggersLocalEvent(t *testing.T) {
	market := &mockMarket{tickers: []string{"TECH", "FOOD"}}
	engine, err := NewEngine(Config{Interval: time.Second}, []Definition{{
		Name:             "Elon buys <ticker>",
		Message:          "Elon buys <ticker>",
		Chance:           100,
		MarketMultiplier: "10-10",
		DurationSeconds:  60,
		Global:           false,
	}}, market)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.mu.Lock()
	engine.rng = rand.New(rand.NewSource(1))
	engine.mu.Unlock()

	engine.tick(context.Background())
	if len(market.calls) != 1 {
		t.Fatalf("expected 1 event call, got %d", len(market.calls))
	}
	call := market.calls[0]
	if call.Global {
		t.Fatalf("expected local event")
	}
	if call.Symbol == "" {
		t.Fatalf("expected random ticker to be selected")
	}
	if !strings.Contains(call.Message, call.Symbol) {
		t.Fatalf("expected message %q to include symbol %q", call.Message, call.Symbol)
	}
	if call.MultiplierPct != 10 {
		t.Fatalf("expected exact multiplier 10, got %d", call.MultiplierPct)
	}
}
