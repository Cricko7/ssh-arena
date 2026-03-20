package exchange

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aeza/ssh-arena/internal/orderbook"
)

type Ticker struct {
	Symbol         string `json:"symbol"`
	Name           string `json:"name"`
	Sector         string `json:"sector"`
	InitialPrice   int64  `json:"initial_price"`
	TickSize       int64  `json:"tick_size"`
	LiquidityUnits int64  `json:"liquidity_units"`
	WhaleThreshold int64  `json:"whale_threshold"`
}

type Catalog struct {
	Tickers []Ticker `json:"tickers"`
}

type PricePoint struct {
	Symbol          string    `json:"symbol"`
	PreviousPrice   int64     `json:"previous_price"`
	CurrentPrice    int64     `json:"current_price"`
	NetVolume       int64     `json:"net_volume"`
	BuyPressure     int64     `json:"buy_pressure"`
	SellPressure    int64     `json:"sell_pressure"`
	WhaleVolume     int64     `json:"whale_volume"`
	WhaleMultiplier int64     `json:"whale_multiplier_bps"`
	MoveBps         int64     `json:"move_bps"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Portfolio struct {
	PlayerID  string           `json:"player_id"`
	Cash      int64            `json:"cash"`
	Stocks    map[string]int64 `json:"stocks"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type PlaceOrderInput struct {
	RequestID  string
	PlayerID   string
	PlayerRole string
	Symbol     string
	Side       orderbook.Side
	Type       orderbook.OrderType
	Price      int64
	Quantity   int64
}

type PlaceOrderOutput struct {
	Order      orderbook.Order    `json:"order"`
	OrderBook  orderbook.Snapshot `json:"orderbook"`
	Price      PricePoint         `json:"price"`
	Trades     []orderbook.Trade  `json:"trades"`
	Resting    *orderbook.Order   `json:"resting,omitempty"`
	Removed    []string           `json:"removed_order_ids,omitempty"`
	JSON       string             `json:"json"`
	OccurredAt time.Time          `json:"occurred_at"`
}

type ChartSnapshot struct {
	Symbol       string             `json:"symbol"`
	Price        PricePoint         `json:"price"`
	OrderBook    orderbook.Snapshot `json:"orderbook"`
	RecentTrades []orderbook.Trade  `json:"recent_trades"`
	CapturedAt   time.Time          `json:"captured_at"`
}

type EventShockInput struct {
	EventName     string
	Message       string
	Global        bool
	Symbol        string
	MultiplierPct int
	Duration      time.Duration
	OccurredAt    time.Time
}

type EventShockOutput struct {
	JSON            string                `json:"json"`
	AffectedSymbols []string              `json:"affected_symbols"`
	Prices          map[string]PricePoint `json:"prices"`
	OccurredAt      time.Time             `json:"occurred_at"`
}

func MarshalEnvelope(kind string, payload any) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"type":    kind,
		"payload": payload,
	})
	if err != nil {
		return "", fmt.Errorf("marshal %s envelope: %w", kind, err)
	}
	return string(raw), nil
}
