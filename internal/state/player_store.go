package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/logx"
	"github.com/aeza/ssh-arena/internal/roles"
)

type PositionLot struct {
	Quantity   int64     `json:"quantity"`
	Price      int64     `json:"price"`
	AcquiredAt time.Time `json:"acquired_at"`
}

type Player struct {
	PlayerID       string                   `json:"player_id"`
	Username       string                   `json:"username"`
	Role           string                   `json:"role"`
	Cash           int64                    `json:"cash"`
	ReservedCash   int64                    `json:"reserved_cash,omitempty"`
	Portfolio      map[string]int64         `json:"portfolio"`
	ReservedStocks map[string]int64         `json:"reserved_stocks,omitempty"`
	Lots           map[string][]PositionLot `json:"lots,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	LastLoginAt    time.Time                `json:"last_login_at"`
}

type snapshot struct {
	Players map[string]Player `json:"players"`
}

type PlayerStore struct {
	mu      sync.Mutex
	path    string
	players map[string]Player
	logger  *slog.Logger
}

func LoadPlayerStore(path string) (*PlayerStore, error) {
	store := &PlayerStore{
		path:    path,
		players: make(map[string]Player),
		logger:  logx.L("state.player"),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	store.logger.Info("player store ready", "path", path, "players", len(store.players))
	return store, nil
}

func (s *PlayerStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("player store file not found", "path", s.path)
			return nil
		}
		return fmt.Errorf("read player store: %w", err)
	}
	if len(raw) == 0 {
		s.logger.Info("player store file empty", "path", s.path)
		return nil
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("decode player store: %w", err)
	}
	if snap.Players != nil {
		for username, player := range snap.Players {
			if player.Portfolio == nil {
				player.Portfolio = make(map[string]int64)
			}
			if player.ReservedStocks == nil {
				player.ReservedStocks = make(map[string]int64)
			}
			if player.Lots == nil {
				player.Lots = make(map[string][]PositionLot)
			}
			s.players[username] = player
		}
	}
	return nil
}

func (s *PlayerStore) Get(username string) (Player, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	player, ok := s.players[username]
	return clonePlayer(player), ok
}

func (s *PlayerStore) GetByPlayerID(playerID string) (Player, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, player := range s.players {
		if player.PlayerID == playerID {
			return clonePlayer(player), true
		}
	}
	return Player{}, false
}

func (s *PlayerStore) Upsert(player Player) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if player.Portfolio == nil {
		player.Portfolio = make(map[string]int64)
	}
	if player.ReservedStocks == nil {
		player.ReservedStocks = make(map[string]int64)
	}
	if player.Lots == nil {
		player.Lots = make(map[string][]PositionLot)
	}
	s.players[player.Username] = clonePlayer(player)
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.logger.Info("player persisted", "username", player.Username, "player_id", player.PlayerID, "role", player.Role)
	return nil
}

func (s *PlayerStore) UpdateByPlayerID(playerID string, update func(*Player) error) (Player, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, player := range s.players {
		if player.PlayerID != playerID {
			continue
		}
		if player.Portfolio == nil {
			player.Portfolio = make(map[string]int64)
		}
		if player.ReservedStocks == nil {
			player.ReservedStocks = make(map[string]int64)
		}
		if player.Lots == nil {
			player.Lots = make(map[string][]PositionLot)
		}
		if err := update(&player); err != nil {
			return Player{}, err
		}
		s.players[username] = clonePlayer(player)
		if err := s.persistLocked(); err != nil {
			return Player{}, err
		}
		s.logger.Info("player updated", "username", player.Username, "player_id", player.PlayerID, "role", player.Role)
		return clonePlayer(player), nil
	}
	return Player{}, fmt.Errorf("player %q not found", playerID)
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

func clonePlayer(player Player) Player {
	copyPlayer := player
	copyPlayer.Portfolio = cloneMap(player.Portfolio)
	copyPlayer.ReservedStocks = cloneMap(player.ReservedStocks)
	copyPlayer.Lots = cloneLotsMap(player.Lots)
	return copyPlayer
}

func cloneMap(source map[string]int64) map[string]int64 {
	if source == nil {
		return make(map[string]int64)
	}
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneLotsMap(source map[string][]PositionLot) map[string][]PositionLot {
	if source == nil {
		return make(map[string][]PositionLot)
	}
	result := make(map[string][]PositionLot, len(source))
	for symbol, lots := range source {
		copied := make([]PositionLot, len(lots))
		copy(copied, lots)
		result[symbol] = copied
	}
	return result
}
