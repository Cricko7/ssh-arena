package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aeza/ssh-arena/internal/roles"
)

func TestPlayerStorePersistsPlayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "players.json")
	store, err := LoadPlayerStore(path)
	if err != nil {
		t.Fatalf("load store: %v", err)
	}

	player := Player{
		PlayerID:    "player-1",
		Username:    "alice",
		Role:        string(roles.RoleBuyer),
		Cash:        1000,
		Portfolio:   map[string]int64{"TECH": 10},
		CreatedAt:   time.Now().UTC(),
		LastLoginAt: time.Now().UTC(),
	}
	if err := store.Upsert(player); err != nil {
		t.Fatalf("upsert player: %v", err)
	}

	reloaded, err := LoadPlayerStore(path)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	got, ok := reloaded.Get("alice")
	if !ok {
		t.Fatal("expected alice in store")
	}
	if got.PlayerID != player.PlayerID || got.Cash != player.Cash {
		t.Fatalf("unexpected player after reload: %+v", got)
	}
}
