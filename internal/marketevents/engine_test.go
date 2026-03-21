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

type mockScheduler struct {
	planned []PlannedEvent
}

func (m *mockScheduler) ScheduleNextRandomEvent(_ context.Context, event PlannedEvent) {
	m.planned = append(m.planned, event)
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

func TestEnginePlansAndTriggersNextEvent(t *testing.T) {
	market := &mockMarket{tickers: []string{"TECH", "FOOD"}}
	scheduler := &mockScheduler{}
	engine, err := NewEngine(Config{Interval: time.Second}, []Definition{{
		Name:             "Elon buys <ticker>",
		Message:          "Elon buys <ticker>",
		Chance:           100,
		MarketMultiplier: "10-10",
		DurationSeconds:  60,
		Global:           false,
	}}, market, scheduler)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.mu.Lock()
	engine.rng = rand.New(rand.NewSource(1))
	engine.mu.Unlock()

	now := time.Now().UTC()
	engine.step(context.Background(), now)
	if len(scheduler.planned) != 1 {
		t.Fatalf("expected one planned event, got %d", len(scheduler.planned))
	}
	planned := scheduler.planned[0]
	if planned.Symbol == "" {
		t.Fatal("expected a ticker to be selected for the next event")
	}
	if !strings.Contains(planned.Message, planned.Symbol) {
		t.Fatalf("expected planned message %q to include symbol %q", planned.Message, planned.Symbol)
	}
	if planned.MultiplierPct != 10 {
		t.Fatalf("expected multiplier 10, got %d", planned.MultiplierPct)
	}

	engine.step(context.Background(), planned.ScheduledAt)
	if len(market.calls) != 1 {
		t.Fatalf("expected 1 event call, got %d", len(market.calls))
	}
	call := market.calls[0]
	if call.Symbol != planned.Symbol || call.MultiplierPct != planned.MultiplierPct {
		t.Fatalf("unexpected triggered event: %+v vs planned %+v", call, planned)
	}
	if len(scheduler.planned) < 2 {
		t.Fatalf("expected next event to be planned immediately after fire, got %d plans", len(scheduler.planned))
	}
}
