package exchange

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aeza/ssh-arena/internal/chat"
	"github.com/aeza/ssh-arena/internal/orderbook"
)

type Service struct {
	mu           sync.Mutex
	books        map[string]*orderbook.Book
	tickers      map[string]Ticker
	priceEngine  *PriceEngine
	chat         *chat.Service
	cache        Cache
	recentTrades map[string][]orderbook.Trade
}

func NewService(tickers []Ticker, chatService *chat.Service, cache Cache) *Service {
	bookMap := make(map[string]*orderbook.Book, len(tickers))
	tickerMap := make(map[string]Ticker, len(tickers))
	for _, ticker := range tickers {
		bookMap[ticker.Symbol] = orderbook.New(ticker.Symbol, ticker.InitialPrice)
		tickerMap[ticker.Symbol] = ticker
	}

	return &Service{
		books:        bookMap,
		tickers:      tickerMap,
		priceEngine:  NewPriceEngine(tickers),
		chat:         chatService,
		cache:        cache,
		recentTrades: make(map[string][]orderbook.Trade, len(tickers)),
	}
}

func (s *Service) ListTickers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tickers))
	for symbol := range s.tickers {
		out = append(out, symbol)
	}
	return out
}

func (s *Service) PlaceOrder(ctx context.Context, input PlaceOrderInput) (PlaceOrderOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	book, ok := s.books[input.Symbol]
	if !ok {
		return PlaceOrderOutput{}, fmt.Errorf("unsupported symbol %q", input.Symbol)
	}

	orderID := input.RequestID
	if orderID == "" {
		orderID = uuid.NewString()
	}

	result, err := book.Place(orderbook.Order{
		ID:         orderID,
		RequestID:  input.RequestID,
		PlayerID:   input.PlayerID,
		PlayerRole: input.PlayerRole,
		Symbol:     input.Symbol,
		Side:       input.Side,
		Type:       input.Type,
		Price:      input.Price,
		Quantity:   input.Quantity,
	})
	if err != nil {
		return PlaceOrderOutput{}, err
	}

	price, err := s.priceEngine.Apply(input.Symbol, result.Trades, result.Snapshot)
	if err != nil {
		return PlaceOrderOutput{}, err
	}

	s.recentTrades[input.Symbol] = append(result.Trades, s.recentTrades[input.Symbol]...)
	if len(s.recentTrades[input.Symbol]) > 500 {
		s.recentTrades[input.Symbol] = s.recentTrades[input.Symbol][:500]
	}

	payload := map[string]any{
		"order":         result.Order,
		"orderbook":     result.Snapshot,
		"price":         price,
		"recent_trades": s.recentTrades[input.Symbol],
	}
	jsonPayload, err := MarshalEnvelope("market.update", payload)
	if err != nil {
		return PlaceOrderOutput{}, err
	}

	if s.cache != nil {
		if err := s.cache.PutSnapshot(ctx, input.Symbol, jsonPayload); err != nil {
			return PlaceOrderOutput{}, err
		}
		if err := s.cache.Publish(ctx, "arena.market."+input.Symbol, jsonPayload); err != nil {
			return PlaceOrderOutput{}, err
		}
	}

	return PlaceOrderOutput{
		Order:      result.Order,
		OrderBook:  result.Snapshot,
		Price:      price,
		Trades:     s.recentTrades[input.Symbol],
		JSON:       jsonPayload,
		OccurredAt: time.Now().UTC(),
	}, nil
}

func (s *Service) TriggerMarketEvent(ctx context.Context, input EventShockInput) (EventShockOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	occurredAt := input.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	affected := make([]string, 0, len(s.tickers))
	if input.Global {
		for symbol := range s.tickers {
			affected = append(affected, symbol)
		}
	} else {
		if _, ok := s.tickers[input.Symbol]; !ok {
			return EventShockOutput{}, fmt.Errorf("unsupported symbol %q", input.Symbol)
		}
		affected = append(affected, input.Symbol)
	}

	prices := make(map[string]PricePoint, len(affected))
	cachePayloads := make(map[string]string, len(affected))
	for _, symbol := range affected {
		price, err := s.priceEngine.TriggerShock(symbol, input.MultiplierPct, input.Duration, occurredAt)
		if err != nil {
			return EventShockOutput{}, err
		}
		prices[symbol] = price

		if s.cache != nil {
			book := s.books[symbol]
			snapshot := book.Snapshot(10)
			recentTrades := append([]orderbook.Trade(nil), s.recentTrades[symbol]...)
			jsonPayload, marshalErr := MarshalEnvelope("market.snapshot", map[string]any{
				"orderbook":     snapshot,
				"price":         price,
				"recent_trades": recentTrades,
			})
			if marshalErr == nil {
				cachePayloads[symbol] = jsonPayload
			}
		}
	}

	payload := map[string]any{
		"name":             input.EventName,
		"message":          input.Message,
		"global":           input.Global,
		"symbol":           input.Symbol,
		"affected_symbols": affected,
		"multiplier_pct":   input.MultiplierPct,
		"duration_seconds": int(input.Duration.Seconds()),
		"prices":           prices,
		"occurred_at":      occurredAt,
	}
	jsonPayload, err := MarshalEnvelope("market.event", payload)
	if err != nil {
		return EventShockOutput{}, err
	}

	if s.cache != nil {
		for symbol, payload := range cachePayloads {
			_ = s.cache.PutSnapshot(ctx, symbol, payload)
			_ = s.cache.Publish(ctx, "arena.market."+symbol, payload)
		}
		_ = s.cache.Publish(ctx, "arena.events", jsonPayload)
	}
	if s.chat != nil {
		_, _ = s.chat.Broadcast(ctx, chat.Message{
			Type:     "system.event",
			Channel:  "global",
			PlayerID: "system",
			Username: "market-bot",
			Role:     "System",
			Body:     input.Message,
			SentAt:   occurredAt,
		})
	}

	return EventShockOutput{
		JSON:            jsonPayload,
		AffectedSymbols: affected,
		Prices:          prices,
		OccurredAt:      occurredAt,
	}, nil
}

func (s *Service) SnapshotJSON(symbol string, depth int) (string, error) {
	snapshot, err := s.ChartSnapshot(symbol, depth)
	if err != nil {
		return "", err
	}

	return MarshalEnvelope("market.snapshot", map[string]any{
		"orderbook":     snapshot.OrderBook,
		"price":         snapshot.Price,
		"recent_trades": snapshot.RecentTrades,
	})
}

func (s *Service) ChartSnapshot(symbol string, depth int) (ChartSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	book, ok := s.books[symbol]
	if !ok {
		return ChartSnapshot{}, fmt.Errorf("unsupported symbol %q", symbol)
	}
	price, err := s.priceEngine.Current(symbol)
	if err != nil {
		return ChartSnapshot{}, err
	}
	recentTrades := append([]orderbook.Trade(nil), s.recentTrades[symbol]...)
	return ChartSnapshot{
		Symbol:       symbol,
		Price:        price,
		OrderBook:    book.Snapshot(depth),
		RecentTrades: recentTrades,
		CapturedAt:   time.Now().UTC(),
	}, nil
}

func (s *Service) BroadcastChat(ctx context.Context, message chat.Message) (string, error) {
	if s.chat == nil {
		return "", fmt.Errorf("chat service is not configured")
	}
	return s.chat.Broadcast(ctx, message)
}
