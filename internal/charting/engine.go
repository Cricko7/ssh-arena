package charting

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/orderbook"
)

type Config struct {
	TickInterval   time.Duration
	HistoryLimit   int
	OrderbookDepth int
}

type SubscriptionRequest struct {
	PlayerID     string
	Ticker       string
	HistoryLimit int
	Depth        int
}

type PriceChartTick struct {
	Type         string         `json:"type"`
	Ticker       string         `json:"ticker"`
	Timestamp    time.Time      `json:"timestamp"`
	CurrentPrice float64        `json:"current_price"`
	Volume1m     int64          `json:"volume_1m"`
	Change1mPct  float64        `json:"change_1m_pct"`
	Change5mPct  float64        `json:"change_5m_pct"`
	Change15mPct float64        `json:"change_15m_pct"`
	Volatility5m float64        `json:"volatility_5m"`
	VWAP5m       float64        `json:"vwap_5m"`
	OrderBook    ChartOrderBook `json:"orderbook"`
	History      []HistoryPoint `json:"history"`
}

type ChartOrderBook struct {
	Bids []ChartLevel `json:"bids"`
	Asks []ChartLevel `json:"asks"`
}

type ChartLevel struct {
	Price float64 `json:"price"`
	Qty   int64   `json:"qty"`
}

type HistoryPoint struct {
	TS     int64   `json:"ts"`
	Price  float64 `json:"price"`
	Volume int64   `json:"volume"`
}

type point struct {
	At     time.Time
	Price  float64
	Volume int64
}

type tickerState struct {
	History []point
}

type subscription struct {
	Request SubscriptionRequest
	Ch      chan string
}

type Engine struct {
	cfg         Config
	market      MarketSource
	mu          sync.RWMutex
	states      map[string]*tickerState
	subscribers map[string]map[int]subscription
	nextID      int
}

type MarketSource interface {
	ListTickers() []string
	ChartSnapshot(symbol string, depth int) (exchange.ChartSnapshot, error)
}

func NewEngine(cfg Config, market MarketSource) *Engine {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 3 * time.Second
	}
	if cfg.HistoryLimit <= 0 {
		cfg.HistoryLimit = 240
	}
	if cfg.OrderbookDepth <= 0 {
		cfg.OrderbookDepth = 10
	}

	states := make(map[string]*tickerState)
	for _, ticker := range market.ListTickers() {
		states[ticker] = &tickerState{}
	}

	return &Engine{
		cfg:         cfg,
		market:      market,
		states:      states,
		subscribers: make(map[string]map[int]subscription),
	}
}

func (e *Engine) Start(ctx context.Context) {
	for _, ticker := range e.market.ListTickers() {
		go e.runTicker(ctx, ticker)
	}
}

func (e *Engine) Subscribe(ctx context.Context, req SubscriptionRequest) (<-chan string, error) {
	if req.Ticker == "" {
		return nil, fmt.Errorf("ticker is required")
	}
	if req.HistoryLimit <= 0 {
		req.HistoryLimit = e.cfg.HistoryLimit
	}
	if req.Depth <= 0 {
		req.Depth = e.cfg.OrderbookDepth
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.states[req.Ticker]; !ok {
		return nil, fmt.Errorf("unsupported ticker %q", req.Ticker)
	}
	if _, ok := e.subscribers[req.Ticker]; !ok {
		e.subscribers[req.Ticker] = make(map[int]subscription)
	}
	id := e.nextID
	e.nextID++
	ch := make(chan string, 16)
	e.subscribers[req.Ticker][id] = subscription{Request: req, Ch: ch}

	go func() {
		<-ctx.Done()
		e.mu.Lock()
		delete(e.subscribers[req.Ticker], id)
		close(ch)
		e.mu.Unlock()
	}()

	return ch, nil
}

func (e *Engine) runTicker(ctx context.Context, ticker string) {
	tickerLoop := time.NewTicker(e.cfg.TickInterval)
	defer tickerLoop.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tickerLoop.C:
			e.emitTick(ctx, ticker)
		}
	}
}

func (e *Engine) emitTick(ctx context.Context, ticker string) {
	snapshot, err := e.market.ChartSnapshot(ticker, e.cfg.OrderbookDepth)
	if err != nil {
		return
	}

	now := time.Now().UTC()
	volume := tradeVolumeSince(snapshot.RecentTrades, now.Add(-e.cfg.TickInterval))
	p := point{At: now, Price: priceToFloat(snapshot.Price.CurrentPrice), Volume: volume}

	e.mu.Lock()
	state := e.states[ticker]
	state.History = append(state.History, p)
	if len(state.History) > e.cfg.HistoryLimit {
		state.History = state.History[len(state.History)-e.cfg.HistoryLimit:]
	}
	historyCopy := append([]point(nil), state.History...)
	subs := make([]subscription, 0, len(e.subscribers[ticker]))
	for _, sub := range e.subscribers[ticker] {
		subs = append(subs, sub)
	}
	e.mu.Unlock()

	for _, sub := range subs {
		tick := buildTick(snapshot, historyCopy, now, sub.Request.Depth, sub.Request.HistoryLimit)
		raw, err := json.Marshal(tick)
		if err != nil {
			continue
		}
		select {
		case sub.Ch <- string(raw):
		case <-ctx.Done():
			return
		default:
		}
	}
}

func buildTick(snapshot exchange.ChartSnapshot, history []point, now time.Time, depth int, historyLimit int) PriceChartTick {
	if depth <= 0 {
		depth = 10
	}
	if historyLimit <= 0 || historyLimit > len(history) {
		historyLimit = len(history)
	}
	trimmedHistory := history
	if historyLimit > 0 {
		trimmedHistory = history[len(history)-historyLimit:]
	}

	tick := PriceChartTick{
		Type:         "price_chart_tick",
		Ticker:       snapshot.Symbol,
		Timestamp:    now,
		CurrentPrice: priceToFloat(snapshot.Price.CurrentPrice),
		Volume1m:     tradeVolumeSince(snapshot.RecentTrades, now.Add(-time.Minute)),
		Change1mPct:  changePct(history, now.Add(-time.Minute)),
		Change5mPct:  changePct(history, now.Add(-5*time.Minute)),
		Change15mPct: changePct(history, now.Add(-15*time.Minute)),
		Volatility5m: volatility(history, now.Add(-5*time.Minute)),
		VWAP5m:       vwap(snapshot.RecentTrades, now.Add(-5*time.Minute), snapshot.Price.CurrentPrice),
		OrderBook: ChartOrderBook{
			Bids: projectLevels(snapshot.OrderBook.Bids, depth),
			Asks: projectLevels(snapshot.OrderBook.Asks, depth),
		},
	}
	for _, item := range trimmedHistory {
		tick.History = append(tick.History, HistoryPoint{
			TS:     item.At.UnixMilli(),
			Price:  item.Price,
			Volume: item.Volume,
		})
	}
	return tick
}

func projectLevels(levels []orderbook.Level, depth int) []ChartLevel {
	out := make([]ChartLevel, 0, min(depth, len(levels)))
	for i, level := range levels {
		if i >= depth {
			break
		}
		out = append(out, ChartLevel{Price: priceToFloat(level.Price), Qty: level.Quantity})
	}
	return out
}

func tradeVolumeSince(trades []orderbook.Trade, cutoff time.Time) int64 {
	var volume int64
	for _, trade := range trades {
		if trade.ExecutedAt.Before(cutoff) {
			continue
		}
		volume += trade.Quantity
	}
	return volume
}

func changePct(history []point, cutoff time.Time) float64 {
	if len(history) == 0 {
		return 0
	}
	current := history[len(history)-1].Price
	base := history[0].Price
	for _, item := range history {
		if !item.At.Before(cutoff) {
			base = item.Price
			break
		}
	}
	if base == 0 {
		return 0
	}
	return round(((current-base)/base)*100, 4)
}

func volatility(history []point, cutoff time.Time) float64 {
	series := make([]float64, 0, len(history))
	for _, item := range history {
		if item.At.Before(cutoff) {
			continue
		}
		series = append(series, item.Price)
	}
	if len(series) < 2 {
		return 0
	}
	returns := make([]float64, 0, len(series)-1)
	for i := 1; i < len(series); i++ {
		if series[i-1] == 0 {
			continue
		}
		returns = append(returns, (series[i]-series[i-1])/series[i-1])
	}
	if len(returns) == 0 {
		return 0
	}
	mean := 0.0
	for _, value := range returns {
		mean += value
	}
	mean /= float64(len(returns))
	variance := 0.0
	for _, value := range returns {
		diff := value - mean
		variance += diff * diff
	}
	variance /= float64(len(returns))
	return round(math.Sqrt(variance), 6)
}

func vwap(trades []orderbook.Trade, cutoff time.Time, fallback int64) float64 {
	var volume int64
	var turnover int64
	for _, trade := range trades {
		if trade.ExecutedAt.Before(cutoff) {
			continue
		}
		volume += trade.Quantity
		turnover += trade.Price * trade.Quantity
	}
	if volume == 0 {
		return priceToFloat(fallback)
	}
	return round(float64(turnover)/float64(volume)/100.0, 4)
}

func priceToFloat(price int64) float64 {
	return round(float64(price)/100.0, 4)
}

func round(value float64, precision int) float64 {
	power := math.Pow(10, float64(precision))
	return math.Round(value*power) / power
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
