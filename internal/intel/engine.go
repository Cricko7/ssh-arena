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
	"github.com/aeza/ssh-arena/internal/marketevents"
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

type scheduledRandomEvent struct {
	Event       marketevents.PlannedEvent
	Buyers      map[string]struct{}
	PreviewSent bool
}

type Engine struct {
	cfg      Config
	market   Market
	notifier Notifier

	mu                   sync.Mutex
	rng                  *rand.Rand
	defs                 map[string]preparedDefinition
	orderedDefs          []preparedDefinition
	pendingInsiderBuyers map[string]struct{}
	nextRandomEvent      *scheduledRandomEvent
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
		if def.LeadTimeSeconds <= 0 && def.Kind == KindInsider {
			def.LeadTimeSeconds = 30
		}
		if def.DurationSeconds <= 0 {
			def.DurationSeconds = 45
		}

		prepared := preparedDefinition{Definition: def}
		if def.Kind == KindRumor || def.Kind == KindFakeNews || def.Kind == KindPaidAnalysis {
			multiplier, err := parseRange(def.MarketMultiplier)
			if err != nil {
				return nil, fmt.Errorf("intel %q: %w", def.ID, err)
			}
			prepared.Multiplier = multiplier
		}
		preparedMap[def.ID] = prepared
		preparedList = append(preparedList, prepared)
	}
	sort.Slice(preparedList, func(i, j int) bool { return preparedList[i].ID < preparedList[j].ID })
	return &Engine{
		cfg:                  cfg,
		market:               market,
		notifier:             notifier,
		rng:                  rand.New(rand.NewSource(time.Now().UnixNano())),
		defs:                 preparedMap,
		orderedDefs:          preparedList,
		pendingInsiderBuyers: make(map[string]struct{}),
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
		result, err := e.buyInsiderLocked(playerID, def)
		e.mu.Unlock()
		if err != nil {
			return BuyResult{}, err
		}
		if result.dispatchPayload != "" && e.notifier != nil {
			e.notifier.NotifyPlayer(playerID, result.dispatchPayload)
		}
		return BuyResult{Cost: def.Price, PayloadJSON: result.responsePayload}, nil
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

type insiderBuyOutcome struct {
	responsePayload string
	dispatchPayload string
}

func (e *Engine) buyInsiderLocked(playerID string, def preparedDefinition) (insiderBuyOutcome, error) {
	now := time.Now().UTC()
	if e.nextRandomEvent != nil && !now.Before(e.nextRandomEvent.Event.ScheduledAt) {
		e.nextRandomEvent = nil
	}
	if e.nextRandomEvent == nil {
		if _, exists := e.pendingInsiderBuyers[playerID]; exists {
			return insiderBuyOutcome{}, fmt.Errorf("insider preview is already armed for your next market event")
		}
		e.pendingInsiderBuyers[playerID] = struct{}{}
		payload := marshal(map[string]any{
			"type":              "intel.purchase.armed",
			"intel_id":          def.ID,
			"kind":              def.Kind,
			"name":              def.Name,
			"price_paid":        def.Price,
			"lead_time_seconds": def.LeadTimeSeconds,
			"scope":             "next_random_event",
			"description":       def.Description,
			"message":           "Insider access armed. You will receive the next scheduled random market event before everyone else.",
			"purchased_at":      now,
		})
		return insiderBuyOutcome{responsePayload: payload}, nil
	}

	current := e.nextRandomEvent
	if _, exists := current.Buyers[playerID]; exists {
		return insiderBuyOutcome{}, fmt.Errorf("insider preview is already armed for the next market event")
	}
	previewAt := current.Event.ScheduledAt.Add(-time.Duration(def.LeadTimeSeconds) * time.Second)
	if previewAt.After(now) {
		current.Buyers[playerID] = struct{}{}
		payload := marshal(map[string]any{
			"type":              "intel.purchase.armed",
			"intel_id":          def.ID,
			"kind":              def.Kind,
			"name":              def.Name,
			"price_paid":        def.Price,
			"lead_time_seconds": def.LeadTimeSeconds,
			"scope":             "next_random_event",
			"event_name":        current.Event.EventName,
			"scheduled_for":     current.Event.ScheduledAt,
			"preview_at":        previewAt,
			"description":       def.Description,
			"message":           "Insider access armed for the next random market event.",
			"purchased_at":      now,
		})
		return insiderBuyOutcome{responsePayload: payload}, nil
	}

	current.Buyers[playerID] = struct{}{}
	preview := e.buildInsiderPreview(def, current.Event, now)
	return insiderBuyOutcome{responsePayload: preview, dispatchPayload: preview}, nil
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

func (e *Engine) ScheduleNextRandomEvent(ctx context.Context, event marketevents.PlannedEvent) {
	if e.insiderDefinition() == nil {
		return
	}
	now := time.Now().UTC()
	e.mu.Lock()
	buyers := copyBuyerSet(e.pendingInsiderBuyers)
	e.pendingInsiderBuyers = make(map[string]struct{})
	e.nextRandomEvent = &scheduledRandomEvent{
		Event:       event,
		Buyers:      buyers,
		PreviewSent: false,
	}
	e.mu.Unlock()
	e.maybeDispatchScheduledPreview(ctx, event.ID, now)
}

func (e *Engine) maybeDispatchScheduledPreview(ctx context.Context, eventID string, now time.Time) {
	payload, playerIDs := e.prepareScheduledPreview(eventID, now)
	if payload != "" {
		e.dispatchPreview(playerIDs, payload)
		return
	}
	def := e.insiderDefinition()
	if def == nil {
		return
	}
	go func() {
		previewAt, ok := e.previewAt(eventID)
		if !ok {
			return
		}
		wait := time.Until(previewAt)
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
		payload, playerIDs := e.prepareScheduledPreview(eventID, time.Now().UTC())
		if payload == "" {
			return
		}
		e.dispatchPreview(playerIDs, payload)
		_ = def
	}()
}

func (e *Engine) previewAt(eventID string) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	def := e.insiderDefinitionLocked()
	if def == nil || e.nextRandomEvent == nil || e.nextRandomEvent.Event.ID != eventID {
		return time.Time{}, false
	}
	return e.nextRandomEvent.Event.ScheduledAt.Add(-time.Duration(def.LeadTimeSeconds) * time.Second), true
}

func (e *Engine) prepareScheduledPreview(eventID string, now time.Time) (string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	def := e.insiderDefinitionLocked()
	if def == nil || e.nextRandomEvent == nil || e.nextRandomEvent.Event.ID != eventID {
		return "", nil
	}
	if e.nextRandomEvent.PreviewSent {
		return "", nil
	}
	previewAt := e.nextRandomEvent.Event.ScheduledAt.Add(-time.Duration(def.LeadTimeSeconds) * time.Second)
	if previewAt.After(now) {
		return "", nil
	}
	playerIDs := buyerIDs(e.nextRandomEvent.Buyers)
	if len(playerIDs) == 0 {
		e.nextRandomEvent.PreviewSent = true
		return "", nil
	}
	payload := e.buildInsiderPreview(*def, e.nextRandomEvent.Event, now)
	e.nextRandomEvent.PreviewSent = true
	return payload, playerIDs
}

func (e *Engine) dispatchPreview(playerIDs []string, payload string) {
	if e.notifier == nil || payload == "" {
		return
	}
	for _, playerID := range playerIDs {
		e.notifier.NotifyPlayer(playerID, payload)
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, def := range e.orderedDefs {
		if def.Kind != KindRumor && def.Kind != KindFakeNews {
			continue
		}
		if !e.shouldTrigger(def.Chance) {
			continue
		}
		trigger := e.prepareTrigger(def)
		if !trigger.ok {
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
	ok            bool
	symbol        string
	multiplier    int
	name          string
	publicMessage string
	occurredAt    time.Time
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
	return preparedTrigger{
		ok:            true,
		symbol:        symbol,
		multiplier:    multiplier,
		name:          renderTemplate(def.Name, symbol, multiplier),
		publicMessage: publicMessage,
		occurredAt:    time.Now().UTC(),
	}
}

func (e *Engine) buildInsiderPreview(def preparedDefinition, event marketevents.PlannedEvent, previewedAt time.Time) string {
	message := strings.TrimSpace(def.PrivateMessage)
	if message == "" {
		message = "The next random market event is locked in. Position before the public tape reacts."
	}
	return marshal(map[string]any{
		"type":              "intel.insider.preview",
		"intel_id":          def.ID,
		"kind":              def.Kind,
		"name":              event.EventName,
		"message":           renderTemplate(message, event.Symbol, event.MultiplierPct),
		"symbol":            event.Symbol,
		"global":            event.Global,
		"multiplier_pct":    event.MultiplierPct,
		"duration_seconds":  event.DurationSeconds,
		"scheduled_for":     event.ScheduledAt,
		"previewed_at":      previewedAt,
		"lead_time_seconds": def.LeadTimeSeconds,
		"market_event": map[string]any{
			"id":               event.ID,
			"kind":             event.Kind,
			"event_name":       event.EventName,
			"message":          event.Message,
			"symbol":           event.Symbol,
			"global":           event.Global,
			"multiplier_pct":   event.MultiplierPct,
			"duration_seconds": event.DurationSeconds,
			"scheduled_at":     event.ScheduledAt,
		},
	})
}

func (e *Engine) insiderDefinition() *preparedDefinition {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.insiderDefinitionLocked()
}

func (e *Engine) insiderDefinitionLocked() *preparedDefinition {
	for _, def := range e.orderedDefs {
		if def.Kind == KindInsider {
			copyDef := def
			return &copyDef
		}
	}
	return nil
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

func copyBuyerSet(input map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(input))
	for playerID := range input {
		out[playerID] = struct{}{}
	}
	return out
}

func buyerIDs(input map[string]struct{}) []string {
	out := make([]string, 0, len(input))
	for playerID := range input {
		out = append(out, playerID)
	}
	sort.Strings(out)
	return out
}
