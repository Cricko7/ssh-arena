package gameplay

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aeza/ssh-arena/internal/charting"
	"github.com/aeza/ssh-arena/internal/chat"
	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/orderbook"
	"github.com/aeza/ssh-arena/internal/roles"
	"github.com/aeza/ssh-arena/internal/state"
)

type Engine struct {
	mu         sync.Mutex
	players    *state.PlayerStore
	allocator  *roles.Allocator
	market     *exchange.Service
	chat       *chat.Service
	charts     *charting.Engine
	symbols    []string
	openOrders map[string]*openOrder
}

type openOrder struct {
	Order            orderbook.Order
	ReservedCash     int64
	ReservedQuantity int64
}

type EnsurePlayerRequest struct {
	Username             string
	RemoteAddr           string
	PublicKeyFingerprint string
}

type EnsurePlayerResponse struct {
	Player        state.Player
	Created       bool
	BootstrapJSON string
}

type ExecuteActionRequest struct {
	RequestID string
	PlayerID  string
	ActionID  string
	Payload   json.RawMessage
	Metadata  map[string]string
}

type PortfolioSnapshot struct {
	Type           string           `json:"type"`
	PlayerID       string           `json:"player_id"`
	Username       string           `json:"username"`
	Role           string           `json:"role"`
	Cash           int64            `json:"cash"`
	ReservedCash   int64            `json:"reserved_cash"`
	AvailableCash  int64            `json:"available_cash"`
	Portfolio      map[string]int64 `json:"portfolio"`
	ReservedStocks map[string]int64 `json:"reserved_stocks"`
	AvailableStock map[string]int64 `json:"available_stock"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type placeOrderPayload struct {
	Symbol   string `json:"symbol"`
	Side     string `json:"side"`
	Type     string `json:"type"`
	Price    int64  `json:"price"`
	Quantity int64  `json:"quantity"`
}

type cancelOrderPayload struct {
	Symbol  string `json:"symbol"`
	OrderID string `json:"order_id"`
}

type chatPayload struct {
	Body string `json:"body"`
}

func NewEngine(players *state.PlayerStore, allocator *roles.Allocator, market *exchange.Service, chatService *chat.Service, charts *charting.Engine) *Engine {
	symbols := market.ListTickers()
	sort.Strings(symbols)
	return &Engine{
		players:    players,
		allocator:  allocator,
		market:     market,
		chat:       chatService,
		charts:     charts,
		symbols:    symbols,
		openOrders: make(map[string]*openOrder),
	}
}

func (e *Engine) EnsurePlayer(_ context.Context, req EnsurePlayerRequest) (EnsurePlayerResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if player, ok := e.players.Get(req.Username); ok {
		player.LastLoginAt = time.Now().UTC()
		if err := e.players.Upsert(player); err != nil {
			return EnsurePlayerResponse{}, err
		}
		bootstrapJSON, err := e.bootstrapJSON(player)
		if err != nil {
			return EnsurePlayerResponse{}, err
		}
		return EnsurePlayerResponse{Player: player, BootstrapJSON: bootstrapJSON}, nil
	}

	assignment := e.allocator.Assign(req.Username, e.players.RoleStats(), e.symbols)
	player := state.Player{
		PlayerID:       uuid.NewString(),
		Username:       req.Username,
		Role:           string(assignment.Role),
		Cash:           assignment.Cash,
		ReservedCash:   0,
		Portfolio:      cloneMap(assignment.Holdings),
		ReservedStocks: make(map[string]int64),
		CreatedAt:      time.Now().UTC(),
		LastLoginAt:    time.Now().UTC(),
	}
	if err := e.players.Upsert(player); err != nil {
		return EnsurePlayerResponse{}, err
	}
	bootstrapJSON, err := e.bootstrapJSON(player)
	if err != nil {
		return EnsurePlayerResponse{}, err
	}
	return EnsurePlayerResponse{Player: player, Created: true, BootstrapJSON: bootstrapJSON}, nil
}

func (e *Engine) ExecuteAction(ctx context.Context, req ExecuteActionRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch req.ActionID {
	case "place_order", "exchange.place_order":
		return e.handlePlaceOrder(ctx, req)
	case "cancel_order", "exchange.cancel_order":
		return e.handleCancelOrder(ctx, req)
	case "portfolio.get", "player.portfolio", "portfolio":
		player, err := e.requirePlayer(req.PlayerID)
		if err != nil {
			return "", err
		}
		return marshalJSON(e.snapshotPlayer(player)), nil
	case "chat.send", "send_chat_message":
		return e.handleSendChat(ctx, req)
	case "market.snapshot":
		return e.handleMarketSnapshot(req)
	default:
		return "", fmt.Errorf("unsupported action %q", req.ActionID)
	}
}

func (e *Engine) MarketFeed(ctx context.Context) <-chan string {
	return e.market.Subscribe(ctx)
}

func (e *Engine) ChatFeed(ctx context.Context) <-chan string {
	if e.chat == nil {
		ch := make(chan string)
		close(ch)
		return ch
	}
	return e.chat.Subscribe(ctx)
}

func (e *Engine) SubscribeChart(ctx context.Context, playerID string, ticker string, historyLimit int, depth int) (<-chan string, error) {
	if e.charts == nil {
		return nil, fmt.Errorf("chart service is not configured")
	}
	return e.charts.Subscribe(ctx, charting.SubscriptionRequest{
		PlayerID:     playerID,
		Ticker:       ticker,
		HistoryLimit: historyLimit,
		Depth:        depth,
	})
}

func (e *Engine) SendChat(ctx context.Context, playerID string, body string) (string, error) {
	player, err := e.requirePlayer(playerID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("chat body is required")
	}
	return e.chat.Broadcast(ctx, chat.Message{
		Type:     "chat.message",
		Channel:  "global",
		PlayerID: player.PlayerID,
		Username: player.Username,
		Role:     player.Role,
		Body:     body,
		SentAt:   time.Now().UTC(),
	})
}

func (e *Engine) handlePlaceOrder(ctx context.Context, req ExecuteActionRequest) (string, error) {
	player, err := e.requirePlayer(req.PlayerID)
	if err != nil {
		return "", err
	}
	var payload placeOrderPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode place_order payload: %w", err)
	}
	payload.Symbol = strings.ToUpper(strings.TrimSpace(payload.Symbol))
	if payload.Symbol == "" || payload.Quantity <= 0 {
		return "", fmt.Errorf("symbol and positive quantity are required")
	}
	orderSide := orderbook.Side(strings.ToLower(payload.Side))
	orderType := orderbook.OrderType(strings.ToLower(payload.Type))
	if orderType == "" {
		orderType = orderbook.OrderTypeLimit
	}

	reserveCash, reserveQty, err := e.reserveForIncoming(&player, payload.Symbol, orderSide, orderType, payload.Price, payload.Quantity)
	if err != nil {
		return "", err
	}
	if err := e.players.Upsert(player); err != nil {
		return "", err
	}

	result, err := e.market.PlaceOrder(ctx, exchange.PlaceOrderInput{
		RequestID:  req.RequestID,
		PlayerID:   player.PlayerID,
		PlayerRole: player.Role,
		Symbol:     payload.Symbol,
		Side:       orderSide,
		Type:       orderType,
		Price:      payload.Price,
		Quantity:   payload.Quantity,
	})
	if err != nil {
		e.releaseIncomingReservation(&player, payload.Symbol, orderSide, reserveCash, reserveQty)
		_ = e.players.Upsert(player)
		return "", err
	}

	incoming := &openOrder{Order: result.Order, ReservedCash: reserveCash, ReservedQuantity: reserveQty}
	touchedPlayers := map[string]*state.Player{player.PlayerID: &player}
	touchedOrders := map[string]struct{}{}
	for _, trade := range result.Trades {
		if err := e.applyTrade(trade, result.Order.ID, incoming, touchedPlayers, touchedOrders); err != nil {
			return "", err
		}
	}

	if result.Resting != nil {
		incoming.Order = *result.Resting
		e.finalizeRestingReservation(payload.Symbol, orderSide, incoming, touchedPlayers[player.PlayerID])
		e.openOrders[result.Resting.ID] = incoming
		touchedOrders[result.Resting.ID] = struct{}{}
	} else {
		e.releaseIncomingReservation(touchedPlayers[player.PlayerID], payload.Symbol, orderSide, incoming.ReservedCash, incoming.ReservedQuantity)
	}

	for _, removedID := range result.Removed {
		e.cleanupOpenOrder(removedID, touchedPlayers)
	}
	for orderID := range touchedOrders {
		e.normalizeOpenOrder(orderID, touchedPlayers)
	}
	for _, changedPlayer := range touchedPlayers {
		if err := e.players.Upsert(*changedPlayer); err != nil {
			return "", err
		}
	}

	portfolio := e.snapshotPlayer(*touchedPlayers[player.PlayerID])
	response := map[string]any{
		"type":      "action.place_order.result",
		"market":    decodeJSON(result.JSON),
		"portfolio": portfolio,
	}
	return marshalJSON(response), nil
}

func (e *Engine) handleCancelOrder(ctx context.Context, req ExecuteActionRequest) (string, error) {
	player, err := e.requirePlayer(req.PlayerID)
	if err != nil {
		return "", err
	}
	var payload cancelOrderPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode cancel_order payload: %w", err)
	}
	payload.Symbol = strings.ToUpper(strings.TrimSpace(payload.Symbol))
	openOrder, ok := e.openOrders[payload.OrderID]
	if !ok {
		return "", fmt.Errorf("order %q not found", payload.OrderID)
	}
	if openOrder.Order.PlayerID != player.PlayerID {
		return "", fmt.Errorf("order %q belongs to another player", payload.OrderID)
	}
	if payload.Symbol == "" {
		payload.Symbol = openOrder.Order.Symbol
	}
	marketJSON, err := e.market.CancelOrder(ctx, payload.Symbol, payload.OrderID)
	if err != nil {
		return "", err
	}
	e.cleanupOpenOrder(payload.OrderID, map[string]*state.Player{player.PlayerID: &player})
	if err := e.players.Upsert(player); err != nil {
		return "", err
	}
	response := map[string]any{
		"type":      "action.cancel_order.result",
		"market":    decodeJSON(marketJSON),
		"portfolio": e.snapshotPlayer(player),
	}
	return marshalJSON(response), nil
}

func (e *Engine) handleSendChat(ctx context.Context, req ExecuteActionRequest) (string, error) {
	var payload chatPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode chat payload: %w", err)
	}
	messageJSON, err := e.SendChat(ctx, req.PlayerID, payload.Body)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"type":    "action.chat.send.result",
		"message": decodeJSON(messageJSON),
	}), nil
}

func (e *Engine) handleMarketSnapshot(req ExecuteActionRequest) (string, error) {
	symbol := ""
	var payload struct {
		Symbol string `json:"symbol"`
	}
	_ = json.Unmarshal(req.Payload, &payload)
	symbol = strings.ToUpper(strings.TrimSpace(payload.Symbol))
	if symbol == "" {
		if len(e.symbols) == 0 {
			return "", fmt.Errorf("no markets configured")
		}
		symbol = e.symbols[0]
	}
	jsonPayload, err := e.market.SnapshotJSON(symbol, 10)
	if err != nil {
		return "", err
	}
	return jsonPayload, nil
}

func (e *Engine) reserveForIncoming(player *state.Player, symbol string, side orderbook.Side, kind orderbook.OrderType, price int64, qty int64) (int64, int64, error) {
	switch side {
	case orderbook.SideBuy:
		reservePrice := price
		if reservePrice <= 0 {
			snapshot, err := e.market.ChartSnapshot(symbol, 1)
			if err != nil {
				return 0, 0, err
			}
			reservePrice = snapshot.Price.CurrentPrice
			if reservePrice <= 0 {
				reservePrice = 100
			}
		}
		if kind == orderbook.OrderTypeMarket {
			reservePrice = int64(float64(reservePrice) * 1.25)
		}
		reserveCash := reservePrice * qty
		availableCash := player.Cash - player.ReservedCash
		if availableCash < reserveCash {
			return 0, 0, fmt.Errorf("insufficient cash: need %d, available %d", reserveCash, availableCash)
		}
		player.ReservedCash += reserveCash
		return reserveCash, 0, nil
	case orderbook.SideSell:
		availableStock := player.Portfolio[symbol] - player.ReservedStocks[symbol]
		if availableStock < qty {
			return 0, 0, fmt.Errorf("insufficient stock %s: need %d, available %d", symbol, qty, availableStock)
		}
		player.ReservedStocks[symbol] += qty
		return 0, qty, nil
	default:
		return 0, 0, fmt.Errorf("unsupported side %q", side)
	}
}

func (e *Engine) releaseIncomingReservation(player *state.Player, symbol string, side orderbook.Side, reserveCash int64, reserveQty int64) {
	switch side {
	case orderbook.SideBuy:
		player.ReservedCash -= reserveCash
		if player.ReservedCash < 0 {
			player.ReservedCash = 0
		}
	case orderbook.SideSell:
		player.ReservedStocks[symbol] -= reserveQty
		if player.ReservedStocks[symbol] < 0 {
			player.ReservedStocks[symbol] = 0
		}
	}
}

func (e *Engine) applyTrade(trade orderbook.Trade, incomingOrderID string, incoming *openOrder, touchedPlayers map[string]*state.Player, touchedOrders map[string]struct{}) error {
	cost := trade.Price * trade.Quantity
	buyer, err := e.getTouchedPlayer(trade.BuyerID, touchedPlayers)
	if err != nil {
		return err
	}
	seller, err := e.getTouchedPlayer(trade.SellerID, touchedPlayers)
	if err != nil {
		return err
	}

	buyer.Cash -= cost
	buyer.Portfolio[trade.Symbol] += trade.Quantity
	if trade.BuyOrderID == incomingOrderID {
		buyer.ReservedCash -= cost
		if buyer.ReservedCash < 0 {
			buyer.ReservedCash = 0
		}
		incoming.ReservedCash -= cost
		if incoming.ReservedCash < 0 {
			incoming.ReservedCash = 0
		}
	} else if open := e.openOrders[trade.BuyOrderID]; open != nil {
		open.Order.RemainingQuantity -= trade.Quantity
		open.ReservedCash -= cost
		if open.ReservedCash < 0 {
			open.ReservedCash = 0
		}
		buyer.ReservedCash -= cost
		if buyer.ReservedCash < 0 {
			buyer.ReservedCash = 0
		}
		touchedOrders[trade.BuyOrderID] = struct{}{}
	}

	seller.Cash += cost
	seller.Portfolio[trade.Symbol] -= trade.Quantity
	if seller.Portfolio[trade.Symbol] < 0 {
		seller.Portfolio[trade.Symbol] = 0
	}
	if trade.SellOrderID == incomingOrderID {
		seller.ReservedStocks[trade.Symbol] -= trade.Quantity
		if seller.ReservedStocks[trade.Symbol] < 0 {
			seller.ReservedStocks[trade.Symbol] = 0
		}
		incoming.ReservedQuantity -= trade.Quantity
		if incoming.ReservedQuantity < 0 {
			incoming.ReservedQuantity = 0
		}
	} else if open := e.openOrders[trade.SellOrderID]; open != nil {
		open.Order.RemainingQuantity -= trade.Quantity
		open.ReservedQuantity -= trade.Quantity
		if open.ReservedQuantity < 0 {
			open.ReservedQuantity = 0
		}
		seller.ReservedStocks[trade.Symbol] -= trade.Quantity
		if seller.ReservedStocks[trade.Symbol] < 0 {
			seller.ReservedStocks[trade.Symbol] = 0
		}
		touchedOrders[trade.SellOrderID] = struct{}{}
	}
	return nil
}

func (e *Engine) finalizeRestingReservation(symbol string, side orderbook.Side, open *openOrder, player *state.Player) {
	switch side {
	case orderbook.SideBuy:
		target := open.Order.Price * open.Order.RemainingQuantity
		if open.ReservedCash > target {
			release := open.ReservedCash - target
			player.ReservedCash -= release
			if player.ReservedCash < 0 {
				player.ReservedCash = 0
			}
			open.ReservedCash = target
		}
	case orderbook.SideSell:
		target := open.Order.RemainingQuantity
		if open.ReservedQuantity > target {
			release := open.ReservedQuantity - target
			player.ReservedStocks[symbol] -= release
			if player.ReservedStocks[symbol] < 0 {
				player.ReservedStocks[symbol] = 0
			}
			open.ReservedQuantity = target
		}
	}
}

func (e *Engine) cleanupOpenOrder(orderID string, touchedPlayers map[string]*state.Player) {
	open := e.openOrders[orderID]
	if open == nil {
		return
	}
	player, err := e.getTouchedPlayer(open.Order.PlayerID, touchedPlayers)
	if err == nil {
		if open.ReservedCash > 0 {
			player.ReservedCash -= open.ReservedCash
			if player.ReservedCash < 0 {
				player.ReservedCash = 0
			}
		}
		if open.ReservedQuantity > 0 {
			player.ReservedStocks[open.Order.Symbol] -= open.ReservedQuantity
			if player.ReservedStocks[open.Order.Symbol] < 0 {
				player.ReservedStocks[open.Order.Symbol] = 0
			}
		}
	}
	delete(e.openOrders, orderID)
}

func (e *Engine) normalizeOpenOrder(orderID string, touchedPlayers map[string]*state.Player) {
	open := e.openOrders[orderID]
	if open == nil {
		return
	}
	player, err := e.getTouchedPlayer(open.Order.PlayerID, touchedPlayers)
	if err != nil {
		return
	}
	if open.Order.RemainingQuantity <= 0 {
		e.cleanupOpenOrder(orderID, touchedPlayers)
		return
	}
	if open.Order.Side == orderbook.SideBuy {
		target := open.Order.Price * open.Order.RemainingQuantity
		if open.ReservedCash > target {
			release := open.ReservedCash - target
			player.ReservedCash -= release
			if player.ReservedCash < 0 {
				player.ReservedCash = 0
			}
			open.ReservedCash = target
		}
	} else {
		target := open.Order.RemainingQuantity
		if open.ReservedQuantity > target {
			release := open.ReservedQuantity - target
			player.ReservedStocks[open.Order.Symbol] -= release
			if player.ReservedStocks[open.Order.Symbol] < 0 {
				player.ReservedStocks[open.Order.Symbol] = 0
			}
			open.ReservedQuantity = target
		}
	}
}

func (e *Engine) requirePlayer(playerID string) (state.Player, error) {
	player, ok := e.players.GetByPlayerID(playerID)
	if !ok {
		return state.Player{}, fmt.Errorf("player %q not found", playerID)
	}
	return player, nil
}

func (e *Engine) getTouchedPlayer(playerID string, touched map[string]*state.Player) (*state.Player, error) {
	if player := touched[playerID]; player != nil {
		return player, nil
	}
	player, ok := e.players.GetByPlayerID(playerID)
	if !ok {
		return nil, fmt.Errorf("player %q not found", playerID)
	}
	touched[playerID] = &player
	return touched[playerID], nil
}

func (e *Engine) snapshotPlayer(player state.Player) PortfolioSnapshot {
	availableStocks := make(map[string]int64, len(player.Portfolio))
	for _, symbol := range e.symbols {
		availableStocks[symbol] = player.Portfolio[symbol] - player.ReservedStocks[symbol]
	}
	return PortfolioSnapshot{
		Type:           "portfolio.snapshot",
		PlayerID:       player.PlayerID,
		Username:       player.Username,
		Role:           player.Role,
		Cash:           player.Cash,
		ReservedCash:   player.ReservedCash,
		AvailableCash:  player.Cash - player.ReservedCash,
		Portfolio:      cloneMap(player.Portfolio),
		ReservedStocks: cloneMap(player.ReservedStocks),
		AvailableStock: availableStocks,
		UpdatedAt:      time.Now().UTC(),
	}
}

func (e *Engine) bootstrapJSON(player state.Player) (string, error) {
	payload := map[string]any{
		"type":       "bootstrap",
		"player_id":  player.PlayerID,
		"username":   player.Username,
		"role":       player.Role,
		"cash":       player.Cash,
		"portfolio":  player.Portfolio,
		"created_at": player.CreatedAt,
		"last_login": player.LastLoginAt,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeJSON(raw string) any {
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return raw
	}
	return out
}

func marshalJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func cloneMap(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (e *Engine) PortfolioJSON(playerID string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	player, err := e.requirePlayer(playerID)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.snapshotPlayer(player)), nil
}

func (e *Engine) MarketSnapshotJSON(symbol string, depth int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.market.SnapshotJSON(symbol, depth)
}
