package gameplay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aeza/ssh-arena/internal/charting"
	"github.com/aeza/ssh-arena/internal/chat"
	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/intel"
	"github.com/aeza/ssh-arena/internal/logx"
	"github.com/aeza/ssh-arena/internal/orderbook"
	"github.com/aeza/ssh-arena/internal/roles"
	"github.com/aeza/ssh-arena/internal/state"
)

type Engine struct {
	mu                  sync.Mutex
	players             *state.PlayerStore
	tradeHistory        *state.TradeStore
	performance         *state.PerformanceStore
	chatStore           *state.ChatStore
	allocator           *roles.Allocator
	market              *exchange.Service
	chat                *chat.Service
	charts              *charting.Engine
	intel               *intel.Engine
	symbols             []string
	openOrders          map[string]*openOrder
	privateSubs         map[string]map[int]chan string
	privateHistory      map[string][]string
	privateHistoryLimit int
	nextPrivateID       int
	bootstrapTokens     map[string]bootstrapGrant
	bootstrapTokenTTL   time.Duration
	logger              *slog.Logger
}

const (
	exchangeFeeBps          int64 = 10
	marketOrderSurchargeBps int64 = 15
	rapidFlipTaxBps         int64 = 35
)

const rapidFlipWindow = 5 * time.Minute
const bootstrapTokenTTL = 2 * time.Minute

type openOrder struct {
	Order            orderbook.Order
	ReservedCash     int64
	ReservedQuantity int64
}

type bootstrapGrant struct {
	PlayerID  string
	Username  string
	ExpiresAt time.Time
}

type TradeRulesSnapshot struct {
	ExchangeFeeBps          int64 `json:"exchange_fee_bps"`
	MarketOrderSurchargeBps int64 `json:"market_order_surcharge_bps"`
	RapidFlipTaxBps         int64 `json:"rapid_flip_tax_bps"`
	RapidFlipWindowSeconds  int64 `json:"rapid_flip_window_seconds"`
}

type TradeCostBreakdown struct {
	Notional             int64 `json:"notional"`
	ExchangeFee          int64 `json:"exchange_fee"`
	MarketOrderSurcharge int64 `json:"market_order_surcharge"`
	RapidFlipTax         int64 `json:"rapid_flip_tax"`
	Total                int64 `json:"total"`
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
	Type           string             `json:"type"`
	PlayerID       string             `json:"player_id"`
	Username       string             `json:"username"`
	Role           string             `json:"role"`
	Cash           int64              `json:"cash"`
	ReservedCash   int64              `json:"reserved_cash"`
	AvailableCash  int64              `json:"available_cash"`
	Portfolio      map[string]int64   `json:"portfolio"`
	ReservedStocks map[string]int64   `json:"reserved_stocks"`
	AvailableStock map[string]int64   `json:"available_stock"`
	FreshStock     map[string]int64   `json:"fresh_stock"`
	Rules          TradeRulesSnapshot `json:"rules"`
	UpdatedAt      time.Time          `json:"updated_at"`
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
	To   string `json:"to,omitempty"`
}

func NewEngine(players *state.PlayerStore, tradeHistory *state.TradeStore, performance *state.PerformanceStore, chatStore *state.ChatStore, allocator *roles.Allocator, market *exchange.Service, chatService *chat.Service, charts *charting.Engine) *Engine {
	symbols := market.ListTickers()
	sort.Strings(symbols)
	return &Engine{
		players:             players,
		tradeHistory:        tradeHistory,
		performance:         performance,
		chatStore:           chatStore,
		allocator:           allocator,
		market:              market,
		chat:                chatService,
		charts:              charts,
		symbols:             symbols,
		openOrders:          make(map[string]*openOrder),
		privateSubs:         make(map[string]map[int]chan string),
		privateHistory:      make(map[string][]string),
		privateHistoryLimit: 64,
		bootstrapTokens:     make(map[string]bootstrapGrant),
		bootstrapTokenTTL:   bootstrapTokenTTL,
		logger:              logx.L("gameplay"),
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
		bootstrapToken, expiresAt := e.issueBootstrapToken(player)
		bootstrapJSON, err := e.bootstrapJSON(player, bootstrapToken, expiresAt)
		if err != nil {
			return EnsurePlayerResponse{}, err
		}
		if err := e.recordPlayerSnapshot(player); err != nil {
			return EnsurePlayerResponse{}, err
		}
		e.logger.Info("player login", "username", req.Username, "player_id", player.PlayerID, "role", player.Role, "created", false, "remote_addr", req.RemoteAddr)
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
	bootstrapToken, expiresAt := e.issueBootstrapToken(player)
	bootstrapJSON, err := e.bootstrapJSON(player, bootstrapToken, expiresAt)
	if err != nil {
		return EnsurePlayerResponse{}, err
	}
	if err := e.recordPlayerSnapshot(player); err != nil {
		return EnsurePlayerResponse{}, err
	}
	e.logger.Info("player created", "username", req.Username, "player_id", player.PlayerID, "role", player.Role, "cash", player.Cash, "remote_addr", req.RemoteAddr)
	return EnsurePlayerResponse{Player: player, Created: true, BootstrapJSON: bootstrapJSON}, nil
}

func (e *Engine) ExecuteAction(ctx context.Context, req ExecuteActionRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var (
		response string
		err      error
	)

	switch req.ActionID {
	case "place_order", "exchange.place_order":
		response, err = e.handlePlaceOrder(ctx, req)
	case "cancel_order", "exchange.cancel_order":
		response, err = e.handleCancelOrder(ctx, req)
	case "portfolio.get", "player.portfolio", "portfolio":
		var player state.Player
		player, err = e.requirePlayer(req.PlayerID)
		if err == nil {
			response = marshalJSON(e.snapshotPlayer(player))
		}
	case "chat.send", "send_chat_message":
		response, err = e.handleSendChat(ctx, req)
	case "market.snapshot":
		response, err = e.handleMarketSnapshot(req)
	case "market.catalog", "market.list":
		response = e.handleMarketCatalog()
	case "bootstrap.exchange_token":
		response, err = e.handleBootstrapExchange(req)
	case "trade.history", "trades.history":
		response, err = e.handleTradeHistory(req)
	case "stats.leaderboard", "leaderboard":
		response, err = e.handleLeaderboard(req)
	case "intel.catalog", "intel.list":
		response, err = e.handleIntelCatalog()
	case "intel.buy":
		response, err = e.handleIntelBuy(ctx, req)
	default:
		err = fmt.Errorf("unsupported action %q", req.ActionID)
	}

	if err != nil {
		e.logger.Warn("action failed", "request_id", req.RequestID, "player_id", req.PlayerID, "action_id", req.ActionID, "error", err)
		return "", err
	}
	e.logger.Info("action handled", "request_id", req.RequestID, "player_id", req.PlayerID, "action_id", req.ActionID)
	return response, nil
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
	return e.chat.SubscribeLive(ctx)
}

func (e *Engine) ChatHistory() []string {
	if e.chat == nil {
		return nil
	}
	return e.chat.History()
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

func (e *Engine) SendChat(ctx context.Context, playerID string, body string, to string) (string, error) {
	player, err := e.requirePlayer(playerID)
	if err != nil {
		return "", err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("chat body is required")
	}
	to = strings.TrimSpace(to)
	if to == "" {
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

	recipient, err := e.resolvePlayerRef(to)
	if err != nil {
		return "", err
	}
	message := chat.Message{
		Type:              "chat.direct",
		Channel:           "direct",
		PlayerID:          player.PlayerID,
		Username:          player.Username,
		Role:              player.Role,
		Body:              body,
		RecipientID:       recipient.PlayerID,
		RecipientUsername: recipient.Username,
		SentAt:            time.Now().UTC(),
	}
	if e.chatStore != nil {
		if err := e.chatStore.Append(state.ChatMessage(message)); err != nil {
			return "", err
		}
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal direct chat message: %w", err)
	}
	payload := string(raw)
	if recipient.PlayerID != player.PlayerID {
		e.notifyPlayerLocked(recipient.PlayerID, payload)
	}
	e.notifyPlayerLocked(player.PlayerID, payload)
	return payload, nil
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
	if err := e.persistTradeEffects(touchedPlayers, result.Trades); err != nil {
		return "", err
	}

	e.logger.Info("order placed", "request_id", req.RequestID, "player_id", player.PlayerID, "symbol", payload.Symbol, "side", payload.Side, "type", payload.Type, "quantity", payload.Quantity, "price", payload.Price, "trades", len(result.Trades), "resting", result.Resting != nil)
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
	e.logger.Info("order cancelled", "request_id", req.RequestID, "player_id", player.PlayerID, "order_id", payload.OrderID, "symbol", payload.Symbol)
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
	messageJSON, err := e.SendChat(ctx, req.PlayerID, payload.Body, payload.To)
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

func (e *Engine) handleMarketCatalog() string {
	return marshalJSON(map[string]any{
		"type":    "market.catalog",
		"count":   len(e.symbols),
		"symbols": append([]string(nil), e.symbols...),
		"rules":   tradingRulesSnapshot(),
	})
}

func (e *Engine) handleMarketRules() string {
	return marshalJSON(map[string]any{
		"type":  "market.rules",
		"rules": tradingRulesSnapshot(),
	})
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
		notional := reservePrice * qty
		reserveCash := notional + estimateExchangeFee(notional)
		if kind == orderbook.OrderTypeMarket {
			reserveCash += estimateMarketSurcharge(notional)
		}
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

	buyerFee := estimateExchangeFee(cost)
	sellerFee := estimateExchangeFee(cost)
	buyerSurcharge := int64(0)
	sellerSurcharge := int64(0)
	if trade.BuyOrderID == incomingOrderID && incoming.Order.Type == orderbook.OrderTypeMarket {
		buyerSurcharge = estimateMarketSurcharge(cost)
	}
	if trade.SellOrderID == incomingOrderID && incoming.Order.Type == orderbook.OrderTypeMarket {
		sellerSurcharge = estimateMarketSurcharge(cost)
	}
	rapidFlipQty := consumeLotsForSale(seller, trade.Symbol, trade.Quantity, trade.ExecutedAt)
	sellerFlipTax := estimateRapidFlipTax(trade.Price * rapidFlipQty)

	buyer.Cash -= cost + buyerFee + buyerSurcharge
	buyer.Portfolio[trade.Symbol] += trade.Quantity
	addAcquiredLot(buyer, trade.Symbol, trade.Quantity, trade.Price, trade.ExecutedAt)
	if trade.BuyOrderID == incomingOrderID {
		buyer.ReservedCash -= cost + buyerFee + buyerSurcharge
		if buyer.ReservedCash < 0 {
			buyer.ReservedCash = 0
		}
		incoming.ReservedCash -= cost + buyerFee + buyerSurcharge
		if incoming.ReservedCash < 0 {
			incoming.ReservedCash = 0
		}
	} else if open := e.openOrders[trade.BuyOrderID]; open != nil {
		open.Order.RemainingQuantity -= trade.Quantity
		open.ReservedCash -= cost + buyerFee + buyerSurcharge
		if open.ReservedCash < 0 {
			open.ReservedCash = 0
		}
		buyer.ReservedCash -= cost + buyerFee + buyerSurcharge
		if buyer.ReservedCash < 0 {
			buyer.ReservedCash = 0
		}
		touchedOrders[trade.BuyOrderID] = struct{}{}
	}

	seller.Cash += cost - sellerFee - sellerSurcharge - sellerFlipTax
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

func (e *Engine) resolvePlayerRef(ref string) (state.Player, error) {
	if ref == "" {
		return state.Player{}, fmt.Errorf("recipient is required")
	}
	if player, ok := e.players.GetByPlayerID(ref); ok {
		return player, nil
	}
	if player, ok := e.players.Get(ref); ok {
		return player, nil
	}
	return state.Player{}, fmt.Errorf("player %q not found", ref)
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
	freshStocks := make(map[string]int64, len(player.Portfolio))
	for _, symbol := range e.symbols {
		availableStocks[symbol] = player.Portfolio[symbol] - player.ReservedStocks[symbol]
		freshStocks[symbol] = max64(0, rapidFlipExposure(player, symbol, time.Now().UTC())-player.ReservedStocks[symbol])
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
		FreshStock:     freshStocks,
		Rules:          tradingRulesSnapshot(),
		UpdatedAt:      time.Now().UTC(),
	}
}

func (e *Engine) bootstrapJSON(player state.Player, bootstrapToken string, expiresAt time.Time) (string, error) {
	payload := map[string]any{
		"type":                       "bootstrap",
		"player_id":                  player.PlayerID,
		"username":                   player.Username,
		"role":                       player.Role,
		"cash":                       player.Cash,
		"portfolio":                  player.Portfolio,
		"created_at":                 player.CreatedAt,
		"last_login":                 player.LastLoginAt,
		"bootstrap_token":            bootstrapToken,
		"bootstrap_token_expires_at": expiresAt,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (e *Engine) issueBootstrapToken(player state.Player) (string, time.Time) {
	now := time.Now().UTC()
	expiresAt := now.Add(e.bootstrapTokenTTL)
	for token, grant := range e.bootstrapTokens {
		if !grant.ExpiresAt.After(now) {
			delete(e.bootstrapTokens, token)
		}
	}
	token := uuid.NewString()
	e.bootstrapTokens[token] = bootstrapGrant{PlayerID: player.PlayerID, Username: player.Username, ExpiresAt: expiresAt}
	e.logger.Info("bootstrap token issued", "player_id", player.PlayerID, "username", player.Username, "expires_at", expiresAt)
	return token, expiresAt
}

func (e *Engine) consumeBootstrapToken(token string) (bootstrapGrant, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return bootstrapGrant{}, fmt.Errorf("bootstrap token is required")
	}
	now := time.Now().UTC()
	grant, ok := e.bootstrapTokens[token]
	if !ok {
		return bootstrapGrant{}, fmt.Errorf("bootstrap token is invalid or already used")
	}
	delete(e.bootstrapTokens, token)
	if !grant.ExpiresAt.After(now) {
		return bootstrapGrant{}, fmt.Errorf("bootstrap token expired")
	}
	return grant, nil
}

func (e *Engine) handleBootstrapExchange(req ExecuteActionRequest) (string, error) {
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode bootstrap token payload: %w", err)
	}
	grant, err := e.consumeBootstrapToken(payload.Token)
	if err != nil {
		return "", err
	}
	player, err := e.requirePlayer(grant.PlayerID)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"type":      "bootstrap.exchange_token",
		"player_id": player.PlayerID,
		"username":  player.Username,
		"role":      player.Role,
	}), nil
}
func tradingRulesSnapshot() TradeRulesSnapshot {
	return TradeRulesSnapshot{
		ExchangeFeeBps:          exchangeFeeBps,
		MarketOrderSurchargeBps: marketOrderSurchargeBps,
		RapidFlipTaxBps:         rapidFlipTaxBps,
		RapidFlipWindowSeconds:  int64(rapidFlipWindow / time.Second),
	}
}

func buyReservationTarget(price int64, qty int64, kind orderbook.OrderType) int64 {
	if price <= 0 || qty <= 0 {
		return 0
	}
	notional := price * qty
	total := notional + estimateExchangeFee(notional)
	if kind == orderbook.OrderTypeMarket {
		total += estimateMarketSurcharge(notional)
	}
	return total
}

func estimateExchangeFee(notional int64) int64 {
	return bpsCharge(notional, exchangeFeeBps)
}

func estimateMarketSurcharge(notional int64) int64 {
	return bpsCharge(notional, marketOrderSurchargeBps)
}

func estimateRapidFlipTax(notional int64) int64 {
	return bpsCharge(notional, rapidFlipTaxBps)
}

func bpsCharge(amount int64, bps int64) int64 {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return (amount*bps + 9999) / 10000
}

func addAcquiredLot(player *state.Player, symbol string, qty int64, price int64, acquiredAt time.Time) {
	if qty <= 0 {
		return
	}
	if player.Lots == nil {
		player.Lots = make(map[string][]state.PositionLot)
	}
	player.Lots[symbol] = append(player.Lots[symbol], state.PositionLot{Quantity: qty, Price: price, AcquiredAt: acquiredAt})
}

func consumeLotsForSale(player *state.Player, symbol string, qty int64, now time.Time) int64 {
	if qty <= 0 || len(player.Lots[symbol]) == 0 {
		return 0
	}
	lots := player.Lots[symbol]
	taxableQty := int64(0)
	remaining := qty
	writeIdx := 0
	for _, lot := range lots {
		if remaining <= 0 {
			lots[writeIdx] = lot
			writeIdx++
			continue
		}
		consume := min64(remaining, lot.Quantity)
		if now.Sub(lot.AcquiredAt) <= rapidFlipWindow {
			taxableQty += consume
		}
		lot.Quantity -= consume
		remaining -= consume
		if lot.Quantity > 0 {
			lots[writeIdx] = lot
			writeIdx++
		}
	}
	lots = lots[:writeIdx]
	if len(lots) == 0 {
		delete(player.Lots, symbol)
	} else {
		player.Lots[symbol] = lots
	}
	return taxableQty
}

func rapidFlipExposure(player state.Player, symbol string, now time.Time) int64 {
	var qty int64
	for _, lot := range player.Lots[symbol] {
		if now.Sub(lot.AcquiredAt) <= rapidFlipWindow {
			qty += lot.Quantity
		}
	}
	return qty
}

func min64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
