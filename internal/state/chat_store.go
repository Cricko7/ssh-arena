package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/jsonfile"
)

type ChatMessage struct {
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

type ChatQuery struct {
	PlayerID string
	Channel  string
	Limit    int
}

type chatSnapshot struct {
	Messages []ChatMessage `json:"messages"`
}

type ChatStore struct {
	mu         sync.Mutex
	path       string
	maxRecords int
	messages   []ChatMessage
}

func LoadChatStore(path string, maxRecords int) (*ChatStore, error) {
	if maxRecords <= 0 {
		maxRecords = 5000
	}
	store := &ChatStore{
		path:       path,
		maxRecords: maxRecords,
		messages:   make([]ChatMessage, 0),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ChatStore) load() error {
	var snap chatSnapshot
	if err := jsonfile.Read(s.path, &snap); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read chat store: %w", err)
	}
	s.messages = append(s.messages[:0], snap.Messages...)
	return nil
}

func (s *ChatStore) Append(record ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.SentAt.IsZero() {
		record.SentAt = time.Now().UTC()
	}
	s.messages = append(s.messages, record)
	if len(s.messages) > s.maxRecords {
		s.messages = slices.Clone(s.messages[len(s.messages)-s.maxRecords:])
	}
	return s.persistLocked()
}

func (s *ChatStore) Query(query ChatQuery) []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	out := make([]ChatMessage, 0, limit)
	for i := len(s.messages) - 1; i >= 0; i-- {
		record := s.messages[i]
		if query.Channel != "" && record.Channel != query.Channel {
			continue
		}
		if query.PlayerID != "" {
			if record.Channel == "global" {
				// always include recent global messages
			} else if record.PlayerID != query.PlayerID && record.RecipientID != query.PlayerID {
				continue
			}
		}
		out = append(out, record)
		if len(out) >= limit {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *ChatStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create chat store dir: %w", err)
	}
	raw, err := json.MarshalIndent(chatSnapshot{Messages: s.messages}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode chat store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return fmt.Errorf("write chat store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace chat store: %w", err)
	}
	return nil
}
