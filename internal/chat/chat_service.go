package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Message struct {
	Type     string    `json:"type"`
	Channel  string    `json:"channel"`
	PlayerID string    `json:"player_id"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Body     string    `json:"body"`
	SentAt   time.Time `json:"sent_at"`
}

type Service struct {
	mu           sync.RWMutex
	nextID       int
	subscribers  map[int]chan string
	history      []string
	historyLimit int
}

func NewService(historyLimit int) *Service {
	if historyLimit <= 0 {
		historyLimit = 100
	}
	return &Service{
		subscribers:  make(map[int]chan string),
		historyLimit: historyLimit,
	}
}

func (s *Service) Subscribe(ctx context.Context) <-chan string {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	ch := make(chan string, 32)
	history := append([]string(nil), s.history...)
	s.subscribers[id] = ch
	s.mu.Unlock()

	go func() {
		for _, item := range history {
			ch <- item
		}

		<-ctx.Done()

		s.mu.Lock()
		delete(s.subscribers, id)
		close(ch)
		s.mu.Unlock()
	}()

	return ch
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

	raw, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("marshal chat message: %w", err)
	}
	payload := string(raw)

	s.mu.Lock()
	s.history = append(s.history, payload)
	if len(s.history) > s.historyLimit {
		s.history = s.history[len(s.history)-s.historyLimit:]
	}

	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- payload:
		case <-ctx.Done():
			s.mu.Unlock()
			return "", ctx.Err()
		default:
		}
	}
	s.mu.Unlock()

	return payload, nil
}
