package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/aeza/ssh-arena/internal/clientlog"
)

var profileLogger = clientlog.L("client.profile")

type Profile struct {
	Version    int       `json:"version"`
	SSHAddr    string    `json:"ssh_addr"`
	GRPCAddr   string    `json:"grpc_addr"`
	Username   string    `json:"username"`
	PlayerID   string    `json:"player_id"`
	Symbols    []string  `json:"symbols,omitempty"`
	LastTicker string    `json:"last_ticker,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func ResolveProfilePath(path string) (string, error) {
	if path != "" {
		profileLogger.Debug("using explicit client profile path", "path", path)
		return path, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	resolved := filepath.Join(configDir, "ssh-arena", "client.json")
	profileLogger.Debug("resolved client profile path", "path", resolved)
	return resolved, nil
}

func LoadProfile(path string) (Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, fmt.Errorf("decode client profile: %w", err)
	}
	profile.Symbols = normalizeProfileSymbols(profile.Symbols)
	profileLogger.Info("client profile loaded",
		"path", path,
		"player_id", profile.PlayerID,
		"username", profile.Username,
		"symbols", len(profile.Symbols),
		"last_ticker", profile.LastTicker,
	)
	return profile, nil
}

func SaveProfile(path string, profile Profile) error {
	profile.Version = 1
	profile.Symbols = normalizeProfileSymbols(profile.Symbols)
	profile.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create client profile dir: %w", err)
	}
	raw, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode client profile: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return fmt.Errorf("write client profile temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace client profile: %w", err)
	}
	profileLogger.Info("client profile saved",
		"path", path,
		"player_id", profile.PlayerID,
		"username", profile.Username,
		"symbols", len(profile.Symbols),
		"last_ticker", profile.LastTicker,
	)
	return nil
}

func normalizeProfileSymbols(symbols []string) []string {
	if len(symbols) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	slices.Sort(out)
	return out
}
