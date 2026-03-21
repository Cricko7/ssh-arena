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

type preparedDefinition struct {
	Definition
	Multiplier Range
}

type Config struct {
	Interval time.Duration
}

type Engine struct {
	cfg    Config
	market Market
	mu     sync.Mutex
	rng    *rand.Rand
	defs   []preparedDefinition
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

func NewEngine(cfg Config, defs []Definition, market Market) (*Engine, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
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
		cfg:    cfg,
		market: market,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
		defs:   prepared,
	}, nil
}

func (e *Engine) Start(ctx context.Context) {
	if len(e.defs) == 0 {
		return
	}
	go e.run(ctx)
}

func (e *Engine) run(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, def := range e.defs {
		if !e.shouldTrigger(def.Chance) {
			continue
		}
		tickers := e.market.ListTickers()
		if len(tickers) == 0 {
			return
		}
		symbol := ""
		if !def.Global {
			symbol = tickers[e.randomInt(len(tickers))]
		}
		multiplierPct := e.randomBetween(def.Multiplier.Min, def.Multiplier.Max)
		message := def.Message
		if symbol != "" {
			message = strings.ReplaceAll(message, "<ticker>", symbol)
			message = strings.ReplaceAll(message, "<TICKER>", symbol)
		}
		_, _ = e.market.TriggerMarketEvent(ctx, exchange.EventShockInput{
			Kind:          "random_event",
			EventName:     def.Name,
			Message:       message,
			Global:        def.Global,
			Symbol:        symbol,
			MultiplierPct: multiplierPct,
			Duration:      time.Duration(def.DurationSeconds) * time.Second,
			OccurredAt:    time.Now().UTC(),
		})
	}
}

func (e *Engine) shouldTrigger(chance float64) bool {
	if chance <= 0 {
		return false
	}
	if chance >= 100 {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rng.Float64()*100.0 < chance
}

func (e *Engine) randomInt(max int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rng.Intn(max)
}

func (e *Engine) randomBetween(minValue, maxValue int) int {
	if minValue >= maxValue {
		return minValue
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return minValue + e.rng.Intn(maxValue-minValue+1)
}
