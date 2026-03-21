package intel

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/jsonfile"
)

type Kind string

const (
	KindRumor        Kind = "rumor"
	KindFakeNews     Kind = "fake_news"
	KindInsider      Kind = "insider"
	KindPaidAnalysis Kind = "paid_analytics"
)

type Definition struct {
	ID               string  `json:"id"`
	Kind             Kind    `json:"kind"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Message          string  `json:"message"`
	PrivateMessage   string  `json:"private_message,omitempty"`
	Chance           float64 `json:"chance,omitempty"`
	Price            int64   `json:"price,omitempty"`
	MarketMultiplier string  `json:"market_multiplier,omitempty"`
	DurationSeconds  int     `json:"duration_seconds,omitempty"`
	LeadTimeSeconds  int     `json:"lead_time_seconds,omitempty"`
	Global           bool    `json:"global"`
	Ticker           string  `json:"ticker,omitempty"`
}

type Catalog struct {
	Feeds []Definition `json:"feeds"`
}

type CatalogItem struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
}

type BuyResult struct {
	Cost        int64  `json:"cost"`
	PayloadJSON string `json:"payload_json"`
}

type Config struct {
	Interval time.Duration
}

type Range struct {
	Min int
	Max int
}

type Market interface {
	ListTickers() []string
	ChartSnapshot(symbol string, depth int) (exchange.ChartSnapshot, error)
	TriggerMarketEvent(context.Context, exchange.EventShockInput) (exchange.EventShockOutput, error)
}

type Notifier interface {
	NotifyPlayer(playerID string, payload string)
}

type preparedDefinition struct {
	Definition
	Multiplier Range
}

type Engine struct {
	cfg          Config
	market       Market
	notifier     Notifier
	mu           sync.Mutex
	rng          *rand.Rand
	defs         map[string]preparedDefinition
	orderedDefs  []preparedDefinition
	entitlements map[string]map[string]int
}

func LoadDefinitions(path string) ([]Definition, error) {
	var catalog Catalog
	if err := jsonfile.Read(path, &catalog); err != nil {
		return nil, fmt.Errorf("read intel config: %w", err)
	}
	return catalog.Feeds, nil
}

func NewEngine(cfg Config, defs []Definition, market Market, notifier Notifier) (*Engine, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 12 * time.Second
	}
	preparedMap := make(map[string]preparedDefinition, len(defs))
	preparedList := make([]preparedDefinition, 0, len(defs))
	for _, def := range defs {
		if def.ID == "" || def.Name == "" {
			return nil, fmt.Errorf("intel id and name are required")
		}
		if def.DurationSeconds <= 0 {
			def.DurationSeconds = 45
		}
		if def.Kind == KindRumor || def.Kind == KindFakeNews || def.Kind == KindInsider {
			multiplier, err := parseRange(def.MarketMultiplier)
			if err != nil {
				return nil, fmt.Errorf("intel %q: %w", def.ID, err)
			}
			prepared := preparedDefinition{Definition: def, Multiplier: multiplier}
			preparedMap[def.ID] = prepared
			preparedList = append(preparedList, prepared)
			continue
		}
		prepared := preparedDefinition{Definition: def}
		preparedMap[def.ID] = prepared
		preparedList = append(preparedList, prepared)
	}
	sort.Slice(preparedList, func(i, j int) bool { return preparedList[i].ID < preparedList[j].ID })
	return &Engine{
		cfg:          cfg,
		market:       market,
		notifier:     notifier,
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
		defs:         preparedMap,
		orderedDefs:  preparedList,
		entitlements: make(map[string]map[string]int),
	}, nil
}

func (e *Engine) Catalog() []CatalogItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	items := make([]CatalogItem, 0)
	for _, def := range e.orderedDefs {
		if def.Kind != KindInsider && def.Kind != KindPaidAnalysis {
			continue
		}
		items = append(items, CatalogItem{ID: def.ID, Kind: def.Kind, Name: def.Name, Description: def.Description, Price: def.Price})
	}
	return items
}

func (e *Engine) Quote(id string) (CatalogItem, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	def, ok := e.defs[id]
	if !ok {
		return CatalogItem{}, fmt.Errorf("intel %q not found", id)
	}
	if def.Kind != KindInsider && def.Kind != KindPaidAnalysis {
		return CatalogItem{}, fmt.Errorf("intel %q is not purchasable", id)
	}
	return CatalogItem{ID: def.ID, Kind: def.Kind, Name: def.Name, Description: def.Description, Price: def.Price}, nil
}

func (e *Engine) Buy(ctx context.Context, playerID string, intelID string) (BuyResult, error) {
	e.mu.Lock()
	def, ok := e.defs[intelID]
	if !ok {
		e.mu.Unlock()
		return BuyResult{}, fmt.Errorf("intel %q not found", intelID)
	}
	if def.Kind == KindInsider {
		if _, ok := e.entitlements[intelID]; !ok {
			e.entitlements[intelID] = make(map[string]int)
		}
		e.entitlements[intelID][playerID]++
		e.mu.Unlock()
		payload := map[string]any{
			"type":         "intel.purchase.armed",
			"intel_id":     def.ID,
			"kind":         def.Kind,
			"name":         def.Name,
			"price_paid":   def.Price,
			"lead_time":    def.LeadTimeSeconds,
			"description":  def.Description,
			"message":      "Insider access armed. If this event triggers, you will get the preview before the rest of the market.",
			"purchased_at": time.Now().UTC(),
		}
		return BuyResult{Cost: def.Price, PayloadJSON: marshal(payload)}, nil
	}
	e.mu.Unlock()

	if def.Kind != KindPaidAnalysis {
		return BuyResult{}, fmt.Errorf("intel %q cannot be bought directly", intelID)
	}
	payloadJSON, err := e.buildAnalyticsPayload(ctx, def)
	if err != nil {
		return BuyResult{}, err
	}
	return BuyResult{Cost: def.Price, PayloadJSON: payloadJSON}, nil
}

func (e *Engine) Start(ctx context.Context) {
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
	for _, def := range e.orderedDefs {
		if def.Kind != KindRumor && def.Kind != KindFakeNews && def.Kind != KindInsider {
			continue
		}
		if !e.shouldTrigger(def.Chance) {
			continue
		}
		trigger := e.prepareTrigger(def)
		if !trigger.ok {
			continue
		}
		if def.Kind == KindInsider {
			e.fireInsider(ctx, def, trigger)
			continue
		}
		_, _ = e.market.TriggerMarketEvent(ctx, exchange.EventShockInput{
			Kind:          string(def.Kind),
			EventName:     trigger.name,
			Message:       trigger.publicMessage,
			Global:        def.Global,
			Symbol:        trigger.symbol,
			MultiplierPct: trigger.multiplier,
			Duration:      time.Duration(def.DurationSeconds) * time.Second,
			OccurredAt:    trigger.occurredAt,
		})
	}
}

type preparedTrigger struct {
	ok             bool
	symbol         string
	multiplier     int
	name           string
	publicMessage  string
	privateMessage string
	occurredAt     time.Time
}

func (e *Engine) prepareTrigger(def preparedDefinition) preparedTrigger {
	tickers := e.market.ListTickers()
	if len(tickers) == 0 {
		return preparedTrigger{}
	}
	symbol := strings.ToUpper(strings.TrimSpace(def.Ticker))
	if !def.Global && symbol == "" {
		symbol = tickers[e.randomInt(len(tickers))]
	}
	multiplier := e.randomBetween(def.Multiplier.Min, def.Multiplier.Max)
	publicMessage := renderTemplate(def.Message, symbol, multiplier)
	privateMessage := def.PrivateMessage
	if strings.TrimSpace(privateMessage) == "" {
		privateMessage = publicMessage
	}
	privateMessage = renderTemplate(privateMessage, symbol, multiplier)
	return preparedTrigger{
		ok:             true,
		symbol:         symbol,
		multiplier:     multiplier,
		name:           renderTemplate(def.Name, symbol, multiplier),
		publicMessage:  publicMessage,
		privateMessage: privateMessage,
		occurredAt:     time.Now().UTC(),
	}
}

func (e *Engine) fireInsider(ctx context.Context, def preparedDefinition, trigger preparedTrigger) {
	lead := time.Duration(def.LeadTimeSeconds) * time.Second
	playerIDs := e.consumeEntitlements(def.ID)
	previewAt := trigger.occurredAt
	publicAt := previewAt.Add(lead)
	if len(playerIDs) > 0 && e.notifier != nil {
		preview := marshal(map[string]any{
			"type":             "intel.insider.preview",
			"intel_id":         def.ID,
			"kind":             def.Kind,
			"name":             trigger.name,
			"message":          trigger.privateMessage,
			"symbol":           trigger.symbol,
			"global":           def.Global,
			"multiplier_pct":   trigger.multiplier,
			"duration_seconds": def.DurationSeconds,
			"scheduled_for":    publicAt,
			"previewed_at":     previewAt,
		})
		for _, playerID := range playerIDs {
			e.notifier.NotifyPlayer(playerID, preview)
		}
	}
	publish := func() {
		_, _ = e.market.TriggerMarketEvent(ctx, exchange.EventShockInput{
			Kind:          string(def.Kind),
			EventName:     trigger.name,
			Message:       trigger.publicMessage,
			Global:        def.Global,
			Symbol:        trigger.symbol,
			MultiplierPct: trigger.multiplier,
			Duration:      time.Duration(def.DurationSeconds) * time.Second,
			OccurredAt:    publicAt,
		})
	}
	if lead <= 0 {
		publish()
		return
	}
	go func() {
		timer := time.NewTimer(lead)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		publish()
	}()
}

func (e *Engine) consumeEntitlements(intelID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	buyers := e.entitlements[intelID]
	if len(buyers) == 0 {
		return nil
	}
	playerIDs := make([]string, 0, len(buyers))
	for playerID, count := range buyers {
		if count <= 0 {
			continue
		}
		playerIDs = append(playerIDs, playerID)
		buyers[playerID] = count - 1
		if buyers[playerID] <= 0 {
			delete(buyers, playerID)
		}
	}
	if len(buyers) == 0 {
		delete(e.entitlements, intelID)
	}
	sort.Strings(playerIDs)
	return playerIDs
}

func (e *Engine) buildAnalyticsPayload(ctx context.Context, def preparedDefinition) (string, error) {
	tickers := e.market.ListTickers()
	if len(tickers) == 0 {
		return "", fmt.Errorf("no tickers configured")
	}
	symbol := strings.ToUpper(strings.TrimSpace(def.Ticker))
	if !def.Global && symbol == "" {
		symbol = tickers[e.randomInt(len(tickers))]
	}
	if symbol == "" {
		symbol = tickers[0]
	}
	snapshot, err := e.market.ChartSnapshot(symbol, 5)
	if err != nil {
		return "", err
	}
	bias := "mixed"
	avgMultiplier := 0
	if def.Multiplier.Min != 0 || def.Multiplier.Max != 0 {
		avgMultiplier = (def.Multiplier.Min + def.Multiplier.Max) / 2
		switch {
		case avgMultiplier > 0:
			bias = "bullish"
		case avgMultiplier < 0:
			bias = "bearish"
		}
	}
	message := renderTemplate(def.Message, symbol, avgMultiplier)
	return marshal(map[string]any{
		"type":                "intel.analytics.report",
		"intel_id":            def.ID,
		"kind":                def.Kind,
		"name":                def.Name,
		"description":         def.Description,
		"message":             message,
		"symbol":              symbol,
		"global":              def.Global,
		"bias":                bias,
		"expected_move_range": def.MarketMultiplier,
		"current_price":       snapshot.Price.CurrentPrice,
		"last_move_bps":       snapshot.Price.MoveBps,
		"recent_trade_count":  len(snapshot.RecentTrades),
		"orderbook":           snapshot.OrderBook,
		"generated_at":        time.Now().UTC(),
		"context_source":      "paid_analytics",
	}), nil
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

func parseRange(value string) (Range, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Range{}, nil
	}
	var a, b int
	if _, err := fmt.Sscanf(value, "%d-%d", &a, &b); err == nil {
		if a > b {
			a, b = b, a
		}
		return Range{Min: a, Max: b}, nil
	}
	if strings.Count(value, "-") >= 2 {
		for i := 1; i < len(value); i++ {
			if value[i] != '-' {
				continue
			}
			left, right := value[:i], value[i+1:]
			if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
				continue
			}
			if _, err := fmt.Sscanf(left, "%d", &a); err != nil {
				continue
			}
			if _, err := fmt.Sscanf(right, "%d", &b); err != nil {
				continue
			}
			if a > b {
				a, b = b, a
			}
			return Range{Min: a, Max: b}, nil
		}
	}
	if _, err := fmt.Sscanf(value, "%d", &a); err == nil {
		return Range{Min: a, Max: a}, nil
	}
	return Range{}, fmt.Errorf("invalid multiplier range %q", value)
}

func renderTemplate(template string, symbol string, multiplier int) string {
	replacer := strings.NewReplacer(
		"<ticker>", symbol,
		"<TICKER>", symbol,
		"<multiplier>", fmt.Sprintf("%d", multiplier),
	)
	return replacer.Replace(template)
}

func marshal(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
