package orderbook

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type OrderType string

const (
	OrderTypeLimit  OrderType = "limit"
	OrderTypeMarket OrderType = "market"
)

type OrderStatus string

const (
	OrderStatusOpen      OrderStatus = "open"
	OrderStatusPartial   OrderStatus = "partial"
	OrderStatusFilled    OrderStatus = "filled"
	OrderStatusCancelled OrderStatus = "cancelled"
)

type Order struct {
	ID                string      `json:"id"`
	RequestID         string      `json:"request_id"`
	PlayerID          string      `json:"player_id"`
	PlayerRole        string      `json:"player_role"`
	Symbol            string      `json:"symbol"`
	Side              Side        `json:"side"`
	Type              OrderType   `json:"type"`
	Price             int64       `json:"price"`
	Quantity          int64       `json:"quantity"`
	RemainingQuantity int64       `json:"remaining_quantity"`
	Status            OrderStatus `json:"status"`
	CreatedAt         time.Time   `json:"created_at"`
}

type Level struct {
	Price      int64 `json:"price"`
	Quantity   int64 `json:"quantity"`
	OrderCount int   `json:"order_count"`
}

type Trade struct {
	ID            string    `json:"id"`
	Symbol        string    `json:"symbol"`
	Price         int64     `json:"price"`
	Quantity      int64     `json:"quantity"`
	AggressorSide Side      `json:"aggressor_side"`
	BuyOrderID    string    `json:"buy_order_id"`
	SellOrderID   string    `json:"sell_order_id"`
	BuyerID       string    `json:"buyer_id"`
	SellerID      string    `json:"seller_id"`
	BuyerRole     string    `json:"buyer_role"`
	SellerRole    string    `json:"seller_role"`
	ExecutedAt    time.Time `json:"executed_at"`
}

type Snapshot struct {
	Symbol         string    `json:"symbol"`
	LastTradePrice int64     `json:"last_trade_price"`
	Sequence       int64     `json:"sequence"`
	Bids           []Level   `json:"bids"`
	Asks           []Level   `json:"asks"`
	GeneratedAt    time.Time `json:"generated_at"`
}

type PlacementResult struct {
	Order    Order    `json:"order"`
	Trades   []Trade  `json:"trades"`
	Snapshot Snapshot `json:"snapshot"`
	Resting  *Order   `json:"resting,omitempty"`
	Removed  []string `json:"removed_order_ids,omitempty"`
}

type Book struct {
	Symbol         string
	bids           map[int64][]*Order
	asks           map[int64][]*Order
	bidPrices      []int64
	askPrices      []int64
	lastTradePrice int64
	sequence       int64
}

func New(symbol string, lastTradePrice int64) *Book {
	return &Book{
		Symbol:         symbol,
		bids:           make(map[int64][]*Order),
		asks:           make(map[int64][]*Order),
		lastTradePrice: lastTradePrice,
	}
}

func (b *Book) Place(order Order) (PlacementResult, error) {
	if err := validate(order); err != nil {
		return PlacementResult{}, err
	}

	if order.CreatedAt.IsZero() {
		order.CreatedAt = time.Now().UTC()
	}
	if order.RemainingQuantity == 0 {
		order.RemainingQuantity = order.Quantity
	}

	result := PlacementResult{Order: order}
	for result.Order.RemainingQuantity > 0 && b.canCross(result.Order) {
		resting, err := b.bestOpposingOrder(result.Order.Side)
		if err != nil {
			return PlacementResult{}, err
		}

		fillQty := min(result.Order.RemainingQuantity, resting.RemainingQuantity)
		tradePrice := resting.Price
		if resting.Type == OrderTypeMarket {
			tradePrice = b.lastTradePrice
		}
		if tradePrice == 0 {
			tradePrice = result.Order.Price
		}

		trade := Trade{
			ID:            fmt.Sprintf("%s-%d", result.Order.RequestID, len(result.Trades)+1),
			Symbol:        result.Order.Symbol,
			Price:         tradePrice,
			Quantity:      fillQty,
			AggressorSide: result.Order.Side,
			ExecutedAt:    time.Now().UTC(),
		}
		if result.Order.Side == SideBuy {
			trade.BuyOrderID = result.Order.ID
			trade.SellOrderID = resting.ID
			trade.BuyerID = result.Order.PlayerID
			trade.SellerID = resting.PlayerID
			trade.BuyerRole = result.Order.PlayerRole
			trade.SellerRole = resting.PlayerRole
		} else {
			trade.BuyOrderID = resting.ID
			trade.SellOrderID = result.Order.ID
			trade.BuyerID = resting.PlayerID
			trade.SellerID = result.Order.PlayerID
			trade.BuyerRole = resting.PlayerRole
			trade.SellerRole = result.Order.PlayerRole
		}

		result.Order.RemainingQuantity -= fillQty
		resting.RemainingQuantity -= fillQty
		b.lastTradePrice = tradePrice
		result.Trades = append(result.Trades, trade)
		b.sequence++

		if resting.RemainingQuantity == 0 {
			resting.Status = OrderStatusFilled
			if err := b.removeHead(resting.Side, resting.Price); err != nil {
				return PlacementResult{}, err
			}
			result.Removed = append(result.Removed, resting.ID)
		} else {
			resting.Status = OrderStatusPartial
		}
	}

	switch {
	case result.Order.RemainingQuantity == 0:
		result.Order.Status = OrderStatusFilled
	case result.Order.RemainingQuantity < result.Order.Quantity:
		if result.Order.Type == OrderTypeMarket {
			result.Order.Status = OrderStatusPartial
		} else {
			result.Order.Status = OrderStatusPartial
			copyOrder := result.Order
			b.addResting(&copyOrder)
			result.Resting = &copyOrder
		}
	default:
		if result.Order.Type == OrderTypeMarket {
			result.Order.Status = OrderStatusCancelled
		} else {
			result.Order.Status = OrderStatusOpen
			copyOrder := result.Order
			b.addResting(&copyOrder)
			result.Resting = &copyOrder
		}
	}

	b.sequence++
	result.Snapshot = b.Snapshot(10)
	result.Order = normalizeOrder(result.Order)
	return result, nil
}

func (b *Book) Cancel(orderID string) (Order, Snapshot, error) {
	for _, price := range append([]int64(nil), b.bidPrices...) {
		orders := b.bids[price]
		for i, order := range orders {
			if order.ID != orderID {
				continue
			}
			cancelled := *order
			cancelled.Status = OrderStatusCancelled
			orders = append(orders[:i], orders[i+1:]...)
			if len(orders) == 0 {
				delete(b.bids, price)
				b.bidPrices = removePrice(b.bidPrices, price)
			} else {
				b.bids[price] = orders
			}
			b.sequence++
			return cancelled, b.Snapshot(10), nil
		}
	}
	for _, price := range append([]int64(nil), b.askPrices...) {
		orders := b.asks[price]
		for i, order := range orders {
			if order.ID != orderID {
				continue
			}
			cancelled := *order
			cancelled.Status = OrderStatusCancelled
			orders = append(orders[:i], orders[i+1:]...)
			if len(orders) == 0 {
				delete(b.asks, price)
				b.askPrices = removePrice(b.askPrices, price)
			} else {
				b.asks[price] = orders
			}
			b.sequence++
			return cancelled, b.Snapshot(10), nil
		}
	}
	return Order{}, Snapshot{}, fmt.Errorf("order %q not found", orderID)
}

func (b *Book) Snapshot(depth int) Snapshot {
	if depth <= 0 {
		depth = 10
	}

	snapshot := Snapshot{
		Symbol:         b.Symbol,
		LastTradePrice: b.lastTradePrice,
		Sequence:       b.sequence,
		GeneratedAt:    time.Now().UTC(),
	}

	for i, price := range b.bidPrices {
		if i >= depth {
			break
		}
		snapshot.Bids = append(snapshot.Bids, levelFromOrders(price, b.bids[price]))
	}
	for i, price := range b.askPrices {
		if i >= depth {
			break
		}
		snapshot.Asks = append(snapshot.Asks, levelFromOrders(price, b.asks[price]))
	}

	return snapshot
}

func (b *Book) JSONSnapshot(depth int) (string, error) {
	raw, err := json.Marshal(b.Snapshot(depth))
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}
	return string(raw), nil
}

func (b *Book) bestOpposingOrder(side Side) (*Order, error) {
	switch side {
	case SideBuy:
		if len(b.askPrices) == 0 {
			return nil, fmt.Errorf("no asks")
		}
		price := b.askPrices[0]
		orders := b.asks[price]
		if len(orders) == 0 {
			return nil, fmt.Errorf("empty ask level")
		}
		return orders[0], nil
	case SideSell:
		if len(b.bidPrices) == 0 {
			return nil, fmt.Errorf("no bids")
		}
		price := b.bidPrices[0]
		orders := b.bids[price]
		if len(orders) == 0 {
			return nil, fmt.Errorf("empty bid level")
		}
		return orders[0], nil
	default:
		return nil, fmt.Errorf("unsupported side %q", side)
	}
}

func (b *Book) canCross(order Order) bool {
	switch order.Side {
	case SideBuy:
		if len(b.askPrices) == 0 {
			return false
		}
		bestAsk := b.askPrices[0]
		return order.Type == OrderTypeMarket || order.Price >= bestAsk
	case SideSell:
		if len(b.bidPrices) == 0 {
			return false
		}
		bestBid := b.bidPrices[0]
		return order.Type == OrderTypeMarket || order.Price <= bestBid
	default:
		return false
	}
}

func (b *Book) addResting(order *Order) {
	if order.RemainingQuantity <= 0 {
		return
	}

	switch order.Side {
	case SideBuy:
		if _, ok := b.bids[order.Price]; !ok {
			b.bidPrices = append(b.bidPrices, order.Price)
			slices.SortFunc(b.bidPrices, func(a, c int64) int {
				switch {
				case a > c:
					return -1
				case a < c:
					return 1
				default:
					return 0
				}
			})
		}
		b.bids[order.Price] = append(b.bids[order.Price], order)
	case SideSell:
		if _, ok := b.asks[order.Price]; !ok {
			b.askPrices = append(b.askPrices, order.Price)
			slices.Sort(b.askPrices)
		}
		b.asks[order.Price] = append(b.asks[order.Price], order)
	}
}

func (b *Book) removeHead(side Side, price int64) error {
	switch side {
	case SideBuy:
		orders := b.bids[price]
		if len(orders) == 0 {
			return fmt.Errorf("missing bid level")
		}
		orders = orders[1:]
		if len(orders) == 0 {
			delete(b.bids, price)
			b.bidPrices = removePrice(b.bidPrices, price)
			return nil
		}
		b.bids[price] = orders
		return nil
	case SideSell:
		orders := b.asks[price]
		if len(orders) == 0 {
			return fmt.Errorf("missing ask level")
		}
		orders = orders[1:]
		if len(orders) == 0 {
			delete(b.asks, price)
			b.askPrices = removePrice(b.askPrices, price)
			return nil
		}
		b.asks[price] = orders
		return nil
	default:
		return fmt.Errorf("unsupported side %q", side)
	}
}

func levelFromOrders(price int64, orders []*Order) Level {
	level := Level{Price: price, OrderCount: len(orders)}
	for _, order := range orders {
		level.Quantity += order.RemainingQuantity
	}
	return level
}

func removePrice(prices []int64, target int64) []int64 {
	index := slices.Index(prices, target)
	if index == -1 {
		return prices
	}
	return append(prices[:index], prices[index+1:]...)
}

func normalizeOrder(order Order) Order {
	switch {
	case order.RemainingQuantity == 0:
		order.Status = OrderStatusFilled
	case order.RemainingQuantity < order.Quantity:
		order.Status = OrderStatusPartial
	case order.Type == OrderTypeMarket:
		order.Status = OrderStatusCancelled
	default:
		order.Status = OrderStatusOpen
	}
	return order
}

func validate(order Order) error {
	if order.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}
	if order.PlayerID == "" {
		return fmt.Errorf("player_id is required")
	}
	if order.ID == "" {
		return fmt.Errorf("order id is required")
	}
	if order.Quantity <= 0 {
		return fmt.Errorf("quantity must be positive")
	}
	if order.Side != SideBuy && order.Side != SideSell {
		return fmt.Errorf("unsupported side %q", order.Side)
	}
	if order.Type != OrderTypeLimit && order.Type != OrderTypeMarket {
		return fmt.Errorf("unsupported order type %q", order.Type)
	}
	if order.Type == OrderTypeLimit && order.Price <= 0 {
		return fmt.Errorf("limit price must be positive")
	}
	return nil
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
