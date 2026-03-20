package exchange

import (
	"fmt"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/orderbook"
)

type PriceEngine struct {
	mu      sync.Mutex
	configs map[string]Ticker
	state   map[string]PricePoint
}

func NewPriceEngine(tickers []Ticker) *PriceEngine {
	configs := make(map[string]Ticker, len(tickers))
	state := make(map[string]PricePoint, len(tickers))
	for _, ticker := range tickers {
		configs[ticker.Symbol] = ticker
		state[ticker.Symbol] = PricePoint{
			Symbol:        ticker.Symbol,
			PreviousPrice: ticker.InitialPrice,
			CurrentPrice:  ticker.InitialPrice,
			UpdatedAt:     time.Now().UTC(),
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

	current := e.state[symbol]
	if current.CurrentPrice == 0 {
		current.CurrentPrice = cfg.InitialPrice
		current.PreviousPrice = cfg.InitialPrice
	}

	var buyVolume int64
	var sellVolume int64
	var whaleVolume int64
	var whaleSide int64
	var sameSideBurst int64

	for _, trade := range trades {
		switch trade.AggressorSide {
		case orderbook.SideBuy:
			buyVolume += trade.Quantity
			sameSideBurst++
		case orderbook.SideSell:
			sellVolume += trade.Quantity
			sameSideBurst--
		}

		if trade.BuyerRole == "Whale" {
			whaleVolume += trade.Quantity
			whaleSide++
		}
		if trade.SellerRole == "Whale" {
			whaleVolume += trade.Quantity
			whaleSide--
		}
	}

	imbalance := topDepth(snapshot.Bids, 3) - topDepth(snapshot.Asks, 3)
	baseDepth := cfg.LiquidityUnits
	if baseDepth <= 0 {
		baseDepth = 1000
	}

	netVolume := buyVolume - sellVolume
	volumeBps := clampBps((netVolume * 10000) / max64(1, baseDepth))
	bookBps := clampBps((imbalance * 5000) / max64(1, baseDepth))
	coordinationBps := int64(0)
	if sameSideBurst >= 3 {
		coordinationBps = 250
	}
	if sameSideBurst <= -3 {
		coordinationBps = -250
	}

	whaleMultiplier := int64(10000)
	if whaleVolume > 0 {
		whaleBps := (whaleVolume * 10000) / max64(1, cfg.WhaleThreshold)
		if whaleBps > 12000 {
			whaleBps = 12000
		}
		whaleMultiplier += whaleBps
	}

	rawMove := volumeBps + bookBps + coordinationBps
	if whaleSide < 0 {
		rawMove = rawMove - (whaleMultiplier-10000)/5
	} else if whaleSide > 0 {
		rawMove = rawMove + (whaleMultiplier-10000)/5
	}
	moveBps := clampBps((rawMove * whaleMultiplier) / 10000)

	nextPrice := alignToTick(current.CurrentPrice+(current.CurrentPrice*moveBps)/10000, cfg.TickSize)
	if len(trades) > 0 && snapshot.LastTradePrice > 0 {
		nextPrice = alignToTick((nextPrice+snapshot.LastTradePrice)/2, cfg.TickSize)
	}
	if nextPrice <= 0 {
		nextPrice = cfg.TickSize
	}

	updated := PricePoint{
		Symbol:          symbol,
		PreviousPrice:   current.CurrentPrice,
		CurrentPrice:    nextPrice,
		NetVolume:       netVolume,
		BuyPressure:     buyVolume,
		SellPressure:    sellVolume,
		WhaleVolume:     whaleVolume,
		WhaleMultiplier: whaleMultiplier,
		MoveBps:         moveBps,
		UpdatedAt:       time.Now().UTC(),
	}
	e.state[symbol] = updated
	return updated, nil
}

func (e *PriceEngine) Current(symbol string) (PricePoint, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.state[symbol]
	if !ok {
		return PricePoint{}, fmt.Errorf("unknown ticker %q", symbol)
	}
	return state, nil
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

func clampBps(value int64) int64 {
	switch {
	case value > 3500:
		return 3500
	case value < -3500:
		return -3500
	default:
		return value
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
