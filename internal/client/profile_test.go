package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	profile := Profile{
		SSHAddr:    "localhost:2222",
		GRPCAddr:   "localhost:9090",
		Username:   "alice",
		PlayerID:   "player-1",
		Symbols:    []string{"CRYPTO", "TECH", "CRYPTO"},
		LastTicker: "TECH",
	}
	if err := SaveProfile(path, profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	loaded, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if loaded.PlayerID != profile.PlayerID || loaded.GRPCAddr != profile.GRPCAddr {
		t.Fatalf("unexpected loaded profile: %+v", loaded)
	}
	if len(loaded.Symbols) != 2 || loaded.Symbols[0] != "CRYPTO" || loaded.Symbols[1] != "TECH" {
		t.Fatalf("unexpected loaded symbols: %+v", loaded.Symbols)
	}
}

func TestResolveProfilePathDefault(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", base)

	path, err := ResolveProfilePath("")
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	if filepath.Base(path) != "client.json" {
		t.Fatalf("unexpected path: %s", path)
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Fatalf("profile dir should not be created yet: %s", filepath.Dir(path))
	}
}
