package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/logx"
	"github.com/aeza/ssh-arena/internal/state"
)

type Message struct {
	Type              string    `json:"type"`
	Channel           string    `json:"channel"`
	PlayerID          string    `json:"player_id"`
	Username          string    `json:"username"`
	Role              string    `json:"role"`
	Body              string    `json:"body"`
	RecipientID       string    `json:"recipient_id,omitempty"`
	RecipientUsername string    `json:"recipient_username,omitempty"`
	SentAt            time.Time `json:"sent_at"`
}

type Service struct {
	mu           sync.RWMutex
	nextID       int
	subscribers  map[int]chan string
	history      []string
	historyLimit int
	store        *state.ChatStore
	logger       *slog.Logger
}

func NewService(historyLimit int, store *state.ChatStore) *Service {
	if historyLimit <= 0 {
		historyLimit = 100
	}
	service := &Service{
		subscribers:  make(map[int]chan string),
		historyLimit: historyLimit,
		store:        store,
		logger:       logx.L("chat"),
	}
	if store != nil {
		for _, item := range store.Query(state.ChatQuery{Channel: "global", Limit: historyLimit}) {
			if payload, err := marshalMessage(item); err == nil {
				service.history = append(service.history, payload)
			}
		}
		service.logger.Info("chat history restored", "count", len(service.history), "limit", historyLimit)
	}
	return service
}

func (s *Service) Subscribe(ctx context.Context) <-chan string {
	history := s.History()
	ch := s.SubscribeLive(ctx)
	go func() {
		for _, item := range history {
			select {
			case ch <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

func (s *Service) SubscribeLive(ctx context.Context) chan string {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	ch := make(chan string, 32)
	s.subscribers[id] = ch
	subscriberCount := len(s.subscribers)
	s.mu.Unlock()
	s.logger.Info("chat subscriber connected", "subscriber_id", id, "subscribers", subscriberCount)

	go func() {
		<-ctx.Done()

		s.mu.Lock()
		delete(s.subscribers, id)
		remaining := len(s.subscribers)
		close(ch)
		s.mu.Unlock()
		s.logger.Info("chat subscriber disconnected", "subscriber_id", id, "subscribers", remaining)
	}()

	return ch
}

func (s *Service) History() []string {
	s.mu.RLock()
	history := append([]string(nil), s.history...)
	store := s.store
	limit := s.historyLimit
	s.mu.RUnlock()
	if store == nil {
		return history
	}
	history = history[:0]
	for _, item := range store.Query(state.ChatQuery{Channel: "global", Limit: limit}) {
		if payload, err := marshalMessage(item); err == nil {
			history = append(history, payload)
		}
	}
	return history
}

func (s *Service) Broadcast(ctx context.Context, message Message) (string, error) {
	if message.Type == "" {
		message.Type = "chat.message"
	}
	if message.Channel == "" {
		message.Channel = "global"
	}
	if message.SentAt.IsZero() {
		message.SentAt = time.Now().UTC()
	}

	payload, err := marshalMessage(state.ChatMessage(message))
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.history = append(s.history, payload)
	if len(s.history) > s.historyLimit {
		s.history = s.history[len(s.history)-s.historyLimit:]
	}
	if s.store != nil {
		if err := s.store.Append(state.ChatMessage(message)); err != nil {
			s.mu.Unlock()
			s.logger.Warn("persist chat message failed", "channel", message.Channel, "player_id", message.PlayerID, "error", err)
			return "", err
		}
	}
	subscriberCount := len(s.subscribers)
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- payload:
		case <-ctx.Done():
			s.mu.Unlock()
			s.logger.Warn("broadcast chat cancelled", "channel", message.Channel, "player_id", message.PlayerID, "error", ctx.Err())
			return "", ctx.Err()
		default:
		}
	}
	s.mu.Unlock()

	s.logger.Info("chat message broadcast", "type", message.Type, "channel", message.Channel, "player_id", message.PlayerID, "recipient_id", message.RecipientID, "body_length", len(message.Body), "subscribers", subscriberCount)
	return payload, nil
}

func marshalMessage(message state.ChatMessage) (string, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal chat message: %w", err)
	}
	return string(raw), nil
}
