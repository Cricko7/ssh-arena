package exchange

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/orderbook"
)

type temporaryEffect struct {
	Name      string
	Bias      float64
	StartedAt time.Time
	ExpiresAt time.Time
}

type marketState struct {
	price      PricePoint
	fairValue  float64
	trend      float64
	pressure   float64
	volatility float64
	effects    []temporaryEffect
}

type PriceEngine struct {
	mu      sync.Mutex
	configs map[string]Ticker
	state   map[string]marketState
}

func NewPriceEngine(tickers []Ticker) *PriceEngine {
	configs := make(map[string]Ticker, len(tickers))
	state := make(map[string]marketState, len(tickers))
	for _, ticker := range tickers {
		configs[ticker.Symbol] = ticker
		initial := PricePoint{
			Symbol:        ticker.Symbol,
			PreviousPrice: ticker.InitialPrice,
			CurrentPrice:  ticker.InitialPrice,
			UpdatedAt:     time.Now().UTC(),
		}
		state[ticker.Symbol] = marketState{
			price:     initial,
			fairValue: float64(ticker.InitialPrice),
		}
	}

	return &PriceEngine{configs: configs, state: state}
}

func (e *PriceEngine) Apply(symbol string, trades []orderbook.Trade, snapshot orderbook.Snapshot) (PricePoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg, ok := e.configs[symbol]
	if !ok {
		return PricePoint{}, fmt.Errorf("unknown ticker %q", symbol)
	}
	now := time.Now().UTC()
	currentState := e.refreshStateLocked(symbol, now)
	current := currentState.price
	if current.CurrentPrice == 0 {
		current.CurrentPrice = cfg.InitialPrice
		current.PreviousPrice = cfg.InitialPrice
		currentState.fairValue = float64(cfg.InitialPrice)
	}

	buyVolume, sellVolume, whaleSignedVolume := tradeFlows(trades)
	depthBid := topDepth(snapshot.Bids, 5)
	depthAsk := topDepth(snapshot.Asks, 5)
	depthImbalance := normalized(depthBid, depthAsk)
	flowImbalance := normalized(buyVolume, sellVolume)
	microPrice := bookMicroPrice(snapshot, current.CurrentPrice)
	tradeVWAP := tradesVWAP(trades, current.CurrentPrice)
	currentPrice := float64(current.CurrentPrice)
	if currentPrice <= 0 {
		currentPrice = float64(cfg.InitialPrice)
	}

	activeBias := currentState.activeBias(now)
	currentState.fairValue = (currentState.fairValue * 0.982) + (tradeVWAP * 0.018)
	microDislocation := percentMove(microPrice, currentPrice)
	meanReversion := percentMove(currentState.fairValue, currentPrice) * 0.18
	whaleImpulse := 0.0
	if cfg.WhaleThreshold > 0 {
		whaleImpulse = (float64(whaleSignedVolume) / float64(cfg.WhaleThreshold)) * 0.35
	}

	instantImpact := (flowImbalance * 0.014) + (depthImbalance * 0.008) + (microDislocation * 0.45) + meanReversion + whaleImpulse + (activeBias * 0.55)
	currentState.pressure = (currentState.pressure * 0.72) + (instantImpact * 0.28)
	currentState.trend = (currentState.trend * 0.82) + (flowImbalance * 0.18) + (activeBias * 0.10)

	move := instantImpact + (currentState.pressure * 0.65) + (currentState.trend * 0.25)
	if len(trades) == 0 {
		move = (meanReversion * 0.65) + (microDislocation * 0.20) + (currentState.pressure * 0.10) + (activeBias * 0.35)
	}

	currentState.volatility = (currentState.volatility * 0.90) + (math.Abs(move) * 0.10)
	volatilityRegime := 1.0 + math.Min(1.35, currentState.volatility*18)
	move *= volatilityRegime
	move = clampFloat(move, -0.035, 0.035)

	nextPrice := alignToTick(int64(math.Round(currentPrice*(1.0+move))), cfg.TickSize)
	if nextPrice <= 0 {
		nextPrice = cfg.TickSize
	}
	moveBps := int64(math.Round(move * 10000))
	whaleMultiplier := int64(10000 + math.Min(12000, math.Abs(whaleImpulse)*10000))

	updated := PricePoint{
		Symbol:          symbol,
		PreviousPrice:   current.CurrentPrice,
		CurrentPrice:    nextPrice,
		NetVolume:       buyVolume - sellVolume,
		BuyPressure:     buyVolume,
		SellPressure:    sellVolume,
		WhaleVolume:     abs64(whaleSignedVolume),
		WhaleMultiplier: whaleMultiplier,
		MoveBps:         moveBps,
		UpdatedAt:       now,
	}
	currentState.price = updated
	e.state[symbol] = currentState
	return updated, nil
}

func (e *PriceEngine) TriggerShock(symbol string, multiplierPct int, duration time.Duration, occurredAt time.Time) (PricePoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg, ok := e.configs[symbol]
	if !ok {
		return PricePoint{}, fmt.Errorf("unknown ticker %q", symbol)
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	if duration <= 0 {
		duration = 30 * time.Second
	}

	currentState := e.refreshStateLocked(symbol, occurredAt)
	current := currentState.price
	if current.CurrentPrice == 0 {
		current.CurrentPrice = cfg.InitialPrice
		current.PreviousPrice = cfg.InitialPrice
		currentState.fairValue = float64(cfg.InitialPrice)
	}

	shockMove := clampFloat(float64(multiplierPct)/100.0, -0.35, 0.35)
	nextPrice := alignToTick(int64(math.Round(float64(current.CurrentPrice)*(1.0+shockMove))), cfg.TickSize)
	if nextPrice <= 0 {
		nextPrice = cfg.TickSize
	}

	currentState.effects = append(currentState.effects, temporaryEffect{
		Name:      symbol,
		Bias:      shockMove * 0.60,
		StartedAt: occurredAt,
		ExpiresAt: occurredAt.Add(duration),
	})
	currentState.fairValue = math.Max(float64(cfg.TickSize), currentState.fairValue*(1.0+(shockMove*0.55)))
	currentState.trend = clampFloat(currentState.trend+(shockMove*0.35), -0.6, 0.6)
	currentState.pressure = clampFloat(currentState.pressure+(shockMove*0.45), -0.8, 0.8)
	currentState.volatility = clampFloat(currentState.volatility+math.Abs(shockMove)*0.25, 0, 1.5)
	currentState.price = PricePoint{
		Symbol:          symbol,
		PreviousPrice:   current.CurrentPrice,
		CurrentPrice:    nextPrice,
		NetVolume:       0,
		BuyPressure:     0,
		SellPressure:    0,
		WhaleVolume:     0,
		WhaleMultiplier: 10000,
		MoveBps:         int64(math.Round(shockMove * 10000)),
		UpdatedAt:       occurredAt,
	}
	currentState = e.pruneExpiredLocked(currentState, occurredAt)
	currentState.effects[len(currentState.effects)-1].Name = symbol
	e.state[symbol] = currentState
	return currentState.price, nil
}

func (e *PriceEngine) Current(symbol string) (PricePoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.state[symbol]
	if !ok {
		return PricePoint{}, fmt.Errorf("unknown ticker %q", symbol)
	}
	state = e.refreshStateLocked(symbol, time.Now().UTC())
	e.state[symbol] = state
	return state.price, nil
}

func (e *PriceEngine) refreshStateLocked(symbol string, now time.Time) marketState {
	state := e.state[symbol]
	cfg := e.configs[symbol]
	state = e.pruneExpiredLocked(state, now)
	if state.price.CurrentPrice == 0 {
		state.price = PricePoint{
			Symbol:        symbol,
			PreviousPrice: cfg.InitialPrice,
			CurrentPrice:  cfg.InitialPrice,
			UpdatedAt:     now,
		}
		if state.fairValue == 0 {
			state.fairValue = float64(cfg.InitialPrice)
		}
		return state
	}

	elapsed := now.Sub(state.price.UpdatedAt)
	if elapsed <= 0 {
		return state
	}
	bias := state.activeBias(now)
	if bias == 0 {
		return state
	}
	step := clampFloat(bias*math.Min(0.18, elapsed.Seconds()/30.0), -0.012, 0.012)
	if step == 0 {
		return state
	}
	nextPrice := alignToTick(int64(math.Round(float64(state.price.CurrentPrice)*(1.0+step))), cfg.TickSize)
	if nextPrice == state.price.CurrentPrice || nextPrice <= 0 {
		return state
	}
	state.price = PricePoint{
		Symbol:          symbol,
		PreviousPrice:   state.price.CurrentPrice,
		CurrentPrice:    nextPrice,
		NetVolume:       0,
		BuyPressure:     0,
		SellPressure:    0,
		WhaleVolume:     0,
		WhaleMultiplier: 10000,
		MoveBps:         int64(math.Round(step * 10000)),
		UpdatedAt:       now,
	}
	state.fairValue = math.Max(float64(cfg.TickSize), state.fairValue*(1.0+(step*0.10)))
	return state
}

func (e *PriceEngine) pruneExpiredLocked(state marketState, now time.Time) marketState {
	if len(state.effects) == 0 {
		return state
	}
	active := state.effects[:0]
	for _, effect := range state.effects {
		if !effect.ExpiresAt.After(now) {
			continue
		}
		active = append(active, effect)
	}
	state.effects = active
	return state
}

func (s marketState) activeBias(now time.Time) float64 {
	bias := 0.0
	for _, effect := range s.effects {
		total := effect.ExpiresAt.Sub(effect.StartedAt)
		if total <= 0 {
			continue
		}
		remaining := effect.ExpiresAt.Sub(now)
		if remaining <= 0 {
			continue
		}
		fraction := remaining.Seconds() / total.Seconds()
		bias += effect.Bias * fraction
	}
	return clampFloat(bias, -0.45, 0.45)
}

func tradeFlows(trades []orderbook.Trade) (int64, int64, int64) {
	var buyVolume int64
	var sellVolume int64
	var whaleSigned int64
	for _, trade := range trades {
		switch trade.AggressorSide {
		case orderbook.SideBuy:
			buyVolume += trade.Quantity
		case orderbook.SideSell:
			sellVolume += trade.Quantity
		}
		if trade.BuyerRole == "Whale" {
			whaleSigned += trade.Quantity
		}
		if trade.SellerRole == "Whale" {
			whaleSigned -= trade.Quantity
		}
	}
	return buyVolume, sellVolume, whaleSigned
}

func tradesVWAP(trades []orderbook.Trade, fallback int64) float64 {
	var turnover int64
	var volume int64
	for _, trade := range trades {
		turnover += trade.Price * trade.Quantity
		volume += trade.Quantity
	}
	if volume == 0 {
		return float64(fallback)
	}
	return float64(turnover) / float64(volume)
}

func bookMicroPrice(snapshot orderbook.Snapshot, fallback int64) float64 {
	if len(snapshot.Bids) == 0 || len(snapshot.Asks) == 0 {
		return float64(fallback)
	}
	bestBid := snapshot.Bids[0]
	bestAsk := snapshot.Asks[0]
	denominator := float64(bestBid.Quantity + bestAsk.Quantity)
	if denominator == 0 {
		return float64(fallback)
	}
	return ((float64(bestAsk.Price) * float64(bestBid.Quantity)) + (float64(bestBid.Price) * float64(bestAsk.Quantity))) / denominator
}

func topDepth(levels []orderbook.Level, maxLevels int) int64 {
	var total int64
	for i, level := range levels {
		if i >= maxLevels {
			break
		}
		total += level.Quantity
	}
	return total
}

func alignToTick(price, tick int64) int64 {
	if tick <= 0 {
		return price
	}
	if price < tick {
		return tick
	}
	return (price / tick) * tick
}

func normalized(a, b int64) float64 {
	total := float64(a + b)
	if total == 0 {
		return 0
	}
	return float64(a-b) / total
}

func percentMove(target, current float64) float64 {
	if current == 0 {
		return 0
	}
	return (target - current) / current
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
