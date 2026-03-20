package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/roles"
)

type Player struct {
	PlayerID    string           `json:"player_id"`
	Username    string           `json:"username"`
	Role        string           `json:"role"`
	Cash        int64            `json:"cash"`
	Portfolio   map[string]int64 `json:"portfolio"`
	CreatedAt   time.Time        `json:"created_at"`
	LastLoginAt time.Time        `json:"last_login_at"`
}

type snapshot struct {
	Players map[string]Player `json:"players"`
}

type PlayerStore struct {
	mu      sync.Mutex
	path    string
	players map[string]Player
}

func LoadPlayerStore(path string) (*PlayerStore, error) {
	store := &PlayerStore{
		path:    path,
		players: make(map[string]Player),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PlayerStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read player store: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("decode player store: %w", err)
	}
	if snap.Players != nil {
		s.players = snap.Players
	}
	return nil
}

func (s *PlayerStore) Get(username string) (Player, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	player, ok := s.players[username]
	return player, ok
}

func (s *PlayerStore) Upsert(player Player) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.players[player.Username] = player
	return s.persistLocked()
}

func (s *PlayerStore) RoleStats() roles.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stats roles.Stats
	for _, player := range s.players {
		switch player.Role {
		case string(roles.RoleBuyer):
			stats.Buyers++
		case string(roles.RoleHolder):
			stats.Holders++
		case string(roles.RoleWhale):
			stats.Whales++
		}
	}
	return stats
}

func (s *PlayerStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create player store dir: %w", err)
	}
	raw, err := json.MarshalIndent(snapshot{Players: s.players}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode player store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return fmt.Errorf("write player store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace player store: %w", err)
	}
	return nil
}
