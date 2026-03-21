package marketevents

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/jsonfile"
)

type Definition struct {
	Name             string  `json:"name"`
	Message          string  `json:"message"`
	Chance           float64 `json:"chance"`
	MarketMultiplier string  `json:"market_multiplier"`
	DurationSeconds  int     `json:"duration_seconds"`
	Global           bool    `json:"global"`
}

type Catalog struct {
	Events []Definition `json:"events"`
}

type Range struct {
	Min int
	Max int
}

type PlannedEvent struct {
	ID              string        `json:"id"`
	Kind            string        `json:"kind"`
	EventName       string        `json:"event_name"`
	Message         string        `json:"message"`
	Global          bool          `json:"global"`
	Symbol          string        `json:"symbol,omitempty"`
	MultiplierPct   int           `json:"multiplier_pct"`
	Duration        time.Duration `json:"-"`
	DurationSeconds int           `json:"duration_seconds"`
	ScheduledAt     time.Time     `json:"scheduled_at"`
}

type preparedDefinition struct {
	Definition
	Multiplier Range
}

type Config struct {
	Interval time.Duration
}

type PreviewScheduler interface {
	ScheduleNextRandomEvent(context.Context, PlannedEvent)
}

type Engine struct {
	cfg      Config
	market   Market
	schedule PreviewScheduler

	mu      sync.Mutex
	rng     *rand.Rand
	defs    []preparedDefinition
	planned *PlannedEvent
	nextSeq int64
}

type Market interface {
	ListTickers() []string
	TriggerMarketEvent(context.Context, exchange.EventShockInput) (exchange.EventShockOutput, error)
}

var rangePattern = regexp.MustCompile(`^(-?\d+)-(-?\d+)$`)

func LoadDefinitions(path string) ([]Definition, error) {
	var catalog Catalog
	if err := jsonfile.Read(path, &catalog); err != nil {
		return nil, fmt.Errorf("read random events config: %w", err)
	}
	return catalog.Events, nil
}

func ParseRange(value string) (Range, error) {
	value = strings.TrimSpace(value)
	matches := rangePattern.FindStringSubmatch(value)
	if len(matches) == 3 {
		var result Range
		if _, err := fmt.Sscanf(matches[1]+" "+matches[2], "%d %d", &result.Min, &result.Max); err != nil {
			return Range{}, fmt.Errorf("parse multiplier range %q: %w", value, err)
		}
		if result.Min > result.Max {
			result.Min, result.Max = result.Max, result.Min
		}
		return result, nil
	}

	var single int
	if _, err := fmt.Sscanf(value, "%d", &single); err == nil {
		return Range{Min: single, Max: single}, nil
	}
	return Range{}, fmt.Errorf("invalid multiplier range %q", value)
}

func NewEngine(cfg Config, defs []Definition, market Market, scheduler PreviewScheduler) (*Engine, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	prepared := make([]preparedDefinition, 0, len(defs))
	for _, def := range defs {
		if def.Name == "" {
			return nil, fmt.Errorf("random event name is required")
		}
		if def.DurationSeconds <= 0 {
			def.DurationSeconds = 30
		}
		multiplier, err := ParseRange(def.MarketMultiplier)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", def.Name, err)
		}
		prepared = append(prepared, preparedDefinition{Definition: def, Multiplier: multiplier})
	}

	return &Engine{
		cfg:      cfg,
		market:   market,
		schedule: scheduler,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		defs:     prepared,
	}, nil
}

func (e *Engine) Start(ctx context.Context) {
	if len(e.defs) == 0 {
		return
	}
	go e.run(ctx)
}

func (e *Engine) PeekNext() (PlannedEvent, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.planned == nil {
		return PlannedEvent{}, false
	}
	return *e.planned, true
}

func (e *Engine) run(ctx context.Context) {
	poll := e.cfg.Interval / 4
	if poll <= 0 || poll > time.Second {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	e.step(ctx, time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e.step(ctx, now.UTC())
		}
	}
}

func (e *Engine) step(ctx context.Context, now time.Time) {
	plans := make([]PlannedEvent, 0, 2)
	var fire *PlannedEvent

	e.mu.Lock()
	if e.planned == nil {
		if planned := e.planLocked(now); planned != nil {
			e.planned = planned
			plans = append(plans, *planned)
		}
	}
	if e.planned != nil && !now.Before(e.planned.ScheduledAt) {
		fireCopy := *e.planned
		fire = &fireCopy
		e.planned = nil
		if planned := e.planLocked(now); planned != nil {
			e.planned = planned
			plans = append(plans, *planned)
		}
	}
	e.mu.Unlock()

	for _, planned := range plans {
		if e.schedule != nil {
			e.schedule.ScheduleNextRandomEvent(ctx, planned)
		}
	}
	if fire != nil {
		e.fire(ctx, *fire)
	}
}

func (e *Engine) fire(ctx context.Context, planned PlannedEvent) {
	_, _ = e.market.TriggerMarketEvent(ctx, exchange.EventShockInput{
		Kind:          planned.Kind,
		EventName:     planned.EventName,
		Message:       planned.Message,
		Global:        planned.Global,
		Symbol:        planned.Symbol,
		MultiplierPct: planned.MultiplierPct,
		Duration:      planned.Duration,
		OccurredAt:    planned.ScheduledAt,
	})
}

func (e *Engine) planLocked(now time.Time) *PlannedEvent {
	def, ok := e.chooseDefinitionLocked()
	if !ok {
		return nil
	}
	tickers := e.market.ListTickers()
	if len(tickers) == 0 {
		return nil
	}

	symbol := ""
	if !def.Global {
		symbol = tickers[e.randomIntLocked(len(tickers))]
	}
	multiplierPct := e.randomBetweenLocked(def.Multiplier.Min, def.Multiplier.Max)
	message := renderTemplate(def.Message, symbol, multiplierPct)
	name := renderTemplate(def.Name, symbol, multiplierPct)
	if strings.TrimSpace(message) == "" {
		message = name
	}
	e.nextSeq++
	planned := PlannedEvent{
		ID:              fmt.Sprintf("random-event-%d", e.nextSeq),
		Kind:            "random_event",
		EventName:       name,
		Message:         message,
		Global:          def.Global,
		Symbol:          symbol,
		MultiplierPct:   multiplierPct,
		Duration:        time.Duration(def.DurationSeconds) * time.Second,
		DurationSeconds: def.DurationSeconds,
		ScheduledAt:     now.Add(e.cfg.Interval),
	}
	return &planned
}

func (e *Engine) chooseDefinitionLocked() (preparedDefinition, bool) {
	total := 0.0
	for _, def := range e.defs {
		if def.Chance > 0 {
			total += def.Chance
		}
	}
	if total <= 0 {
		return preparedDefinition{}, false
	}
	roll := e.rng.Float64() * total
	cumulative := 0.0
	for _, def := range e.defs {
		if def.Chance <= 0 {
			continue
		}
		cumulative += def.Chance
		if roll <= cumulative {
			return def, true
		}
	}
	return e.defs[len(e.defs)-1], true
}

func (e *Engine) randomIntLocked(max int) int {
	return e.rng.Intn(max)
}

func (e *Engine) randomBetweenLocked(minValue, maxValue int) int {
	if minValue >= maxValue {
		return minValue
	}
	return minValue + e.rng.Intn(maxValue-minValue+1)
}

func renderTemplate(template string, symbol string, multiplier int) string {
	replacer := strings.NewReplacer(
		"<ticker>", symbol,
		"<TICKER>", symbol,
		"<multiplier>", fmt.Sprintf("%d", multiplier),
	)
	return replacer.Replace(template)
}
