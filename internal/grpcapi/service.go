package grpcapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/gameplay"
)

type Service struct {
	gamev1.UnimplementedAccountServiceServer
	gamev1.UnimplementedGameServiceServer
	gamev1.UnimplementedChatServiceServer

	engine   *gameplay.Engine
	sequence atomic.Int64
}

func New(engine *gameplay.Engine) *Service {
	return &Service{engine: engine}
}

func (s *Service) EnsurePlayer(ctx context.Context, req *gamev1.PlayerBootstrapRequest) (*gamev1.PlayerBootstrapResponse, error) {
	result, err := s.engine.EnsurePlayer(ctx, gameplay.EnsurePlayerRequest{
		Username:             req.SSHUsername,
		RemoteAddr:           req.RemoteAddr,
		PublicKeyFingerprint: req.PublicKeyFingerprint,
	})
	if err != nil {
		return nil, err
	}
	return &gamev1.PlayerBootstrapResponse{
		PlayerID:      result.Player.PlayerID,
		Role:          result.Player.Role,
		Created:       result.Created,
		BootstrapJSON: result.BootstrapJSON,
	}, nil
}

func (s *Service) ExecuteAction(ctx context.Context, req *gamev1.ActionRequest) (*gamev1.ActionResponse, error) {
	requestID := req.RequestID
	if requestID == "" {
		requestID = fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	responseJSON, err := s.engine.ExecuteAction(ctx, gameplay.ExecuteActionRequest{
		RequestID: requestID,
		PlayerID:  req.PlayerID,
		ActionID:  req.ActionID,
		Payload:   json.RawMessage(req.PayloadJSON),
		Metadata:  req.Metadata,
	})
	if err != nil {
		return &gamev1.ActionResponse{
			RequestID:    requestID,
			ActionID:     req.ActionID,
			Status:       "error",
			ResponseJSON: fmt.Sprintf(`{"type":"error","message":%q}`, err.Error()),
		}, nil
	}
	return &gamev1.ActionResponse{
		RequestID:    requestID,
		ActionID:     req.ActionID,
		Status:       "ok",
		ResponseJSON: responseJSON,
	}, nil
}

func (s *Service) GetMarketStream(stream gamev1.GameService_GetMarketStreamServer) error {
	ctx := stream.Context()
	marketFeed := s.engine.MarketFeed(ctx)
	chatFeed := s.engine.ChatFeed(ctx)
	requests := make(chan *gamev1.MarketStreamRequest, 4)
	errCh := make(chan error, 1)

	go func() {
		defer close(requests)
		for {
			req, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}
			requests <- req
		}
	}()

	var playerID string
	var privateFeed <-chan string
	symbols := map[string]struct{}{}
	includePortfolio := false
	includeChat := false
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case req, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if req == nil {
				continue
			}
			playerID = req.PlayerID
			includePortfolio = req.IncludePortfolio
			includeChat = req.IncludeChat
			if includeChat {
				if err := s.sendInitialChatState(stream, playerID); err != nil {
					return err
				}
				if chatFeed == nil {
					chatFeed = s.engine.ChatFeed(ctx)
				}
			}
			if playerID != "" && privateFeed == nil {
				privateFeed = s.engine.SubscribePrivate(ctx, playerID)
			}
			symbols = make(map[string]struct{}, len(req.Symbols))
			for _, symbol := range req.Symbols {
				symbols[symbol] = struct{}{}
			}
			if err := s.sendInitialMarketState(stream, playerID, symbols, includePortfolio); err != nil {
				return err
			}
		case payload := <-marketFeed:
			if payload == "" || !matchesSymbols(payload, symbols) {
				continue
			}
			if err := stream.Send(s.envelope("market", payload)); err != nil {
				return err
			}
		case payload := <-chatFeed:
			if !includeChat || payload == "" {
				continue
			}
			if err := stream.Send(s.envelope("chat", payload)); err != nil {
				return err
			}
		case payload, ok := <-privateFeed:
			if !ok || payload == "" {
				continue
			}
			if err := stream.Send(s.envelope("private", payload)); err != nil {
				return err
			}
		case <-ticker.C:
			if err := s.sendInitialMarketState(stream, playerID, symbols, includePortfolio); err != nil {
				return err
			}
		}
	}
}

func (s *Service) SubscribeToChart(stream gamev1.GameService_SubscribeToChartServer) error {
	ctx := stream.Context()
	out := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					errCh <- nil
				} else {
					errCh <- err
				}
				return
			}
			ch, err := s.engine.SubscribeChart(ctx, req.PlayerID, req.Ticker, int(req.HistoryLimit), int(req.OrderbookDepth))
			if err != nil {
				errCh <- err
				return
			}
			go func(source <-chan string) {
				for payload := range source {
					select {
					case out <- payload:
					case <-ctx.Done():
						return
					}
				}
			}(ch)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case payload := <-out:
			if err := stream.Send(&gamev1.PriceChartTick{
				Ticker:   extractTicker(payload),
				JSON:     payload,
				Sequence: s.nextSequence(),
			}); err != nil {
				return err
			}
		}
	}
}

func (s *Service) SendChat(ctx context.Context, req *gamev1.ChatRequest) (*gamev1.JsonEnvelope, error) {
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(req.JSON), &payload); err != nil {
		return nil, err
	}
	messageJSON, err := s.engine.SendChat(ctx, req.PlayerID, payload.Body, "")
	if err != nil {
		return nil, err
	}
	return s.envelope("chat", messageJSON), nil
}

func (s *Service) StreamChat(req *gamev1.MarketStreamRequest, stream gamev1.ChatService_StreamChatServer) error {
	ctx := stream.Context()
	feed := s.engine.ChatFeed(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case payload, ok := <-feed:
			if !ok {
				return nil
			}
			if payload == "" {
				continue
			}
			if err := stream.Send(s.envelope("chat", payload)); err != nil {
				return err
			}
		}
	}
}

func (s *Service) sendInitialMarketState(stream gamev1.GameService_GetMarketStreamServer, playerID string, symbols map[string]struct{}, includePortfolio bool) error {
	for symbol := range symbols {
		payload, err := s.engine.MarketSnapshotJSON(symbol, 10)
		if err != nil {
			continue
		}
		if err := stream.Send(s.envelope("market", payload)); err != nil {
			return err
		}
	}
	if includePortfolio && playerID != "" {
		payload, err := s.engine.PortfolioJSON(playerID)
		if err == nil {
			if err := stream.Send(s.envelope("portfolio", payload)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) sendInitialChatState(stream gamev1.GameService_GetMarketStreamServer, playerID string) error {
	for _, payload := range s.engine.ChatHistory() {
		if payload == "" {
			continue
		}
		if err := stream.Send(s.envelope("chat", payload)); err != nil {
			return err
		}
	}
	if playerID == "" {
		return nil
	}
	for _, payload := range s.engine.PrivateHistory(playerID) {
		if payload == "" {
			continue
		}
		if err := stream.Send(s.envelope("private", payload)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) envelope(topic, payload string) *gamev1.JsonEnvelope {
	return &gamev1.JsonEnvelope{Topic: topic, JSON: payload, Sequence: s.nextSequence()}
}

func (s *Service) nextSequence() int64 {
	return s.sequence.Add(1)
}

func matchesSymbols(payload string, symbols map[string]struct{}) bool {
	if len(symbols) == 0 {
		return true
	}
	var envelope struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return true
	}
	if symbol, _ := envelope.Payload["symbol"].(string); symbol != "" {
		_, ok := symbols[symbol]
		return ok
	}
	if affected, ok := envelope.Payload["affected_symbols"].([]any); ok {
		for _, item := range affected {
			if symbol, ok := item.(string); ok {
				if _, exists := symbols[symbol]; exists {
					return true
				}
			}
		}
		return false
	}
	return true
}

func extractTicker(payload string) string {
	var tick struct {
		Ticker string `json:"ticker"`
	}
	_ = json.Unmarshal([]byte(payload), &tick)
	return tick.Ticker
}
