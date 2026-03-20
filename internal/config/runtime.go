package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type RuntimeConfig struct {
	ChartTickIntervalSeconds int    `yaml:"chart_tick_interval_seconds"`
	ChartHistoryPoints       int    `yaml:"chart_history_points"`
	ChartOrderbookDepth      int    `yaml:"chart_orderbook_depth"`
	PlayerStatePath          string `yaml:"player_state_path"`
	RandomEventIntervalSecs  int    `yaml:"random_event_interval_seconds"`
	RandomEventsPath         string `yaml:"random_events_path"`
	IntelEventIntervalSecs   int    `yaml:"intel_event_interval_seconds"`
	IntelEventsPath          string `yaml:"intel_events_path"`
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		ChartTickIntervalSeconds: 3,
		ChartHistoryPoints:       240,
		ChartOrderbookDepth:      10,
		PlayerStatePath:          "data/players.json",
		RandomEventIntervalSecs:  15,
		RandomEventsPath:         "events/random_events.json",
		IntelEventIntervalSecs:   12,
		IntelEventsPath:          "events/intel_feeds.json",
	}
}

func LoadRuntimeConfig(path string) (RuntimeConfig, error) {
	cfg := DefaultRuntimeConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return RuntimeConfig{}, fmt.Errorf("read runtime config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("decode runtime config: %w", err)
	}
	if cfg.ChartTickIntervalSeconds <= 0 {
		cfg.ChartTickIntervalSeconds = 3
	}
	if cfg.ChartHistoryPoints <= 0 {
		cfg.ChartHistoryPoints = 240
	}
	if cfg.ChartOrderbookDepth <= 0 {
		cfg.ChartOrderbookDepth = 10
	}
	if cfg.PlayerStatePath == "" {
		cfg.PlayerStatePath = "data/players.json"
	}
	if cfg.RandomEventIntervalSecs <= 0 {
		cfg.RandomEventIntervalSecs = 15
	}
	if cfg.RandomEventsPath == "" {
		cfg.RandomEventsPath = "events/random_events.json"
	}
	if cfg.IntelEventIntervalSecs <= 0 {
		cfg.IntelEventIntervalSecs = 12
	}
	if cfg.IntelEventsPath == "" {
		cfg.IntelEventsPath = "events/intel_feeds.json"
	}
	return cfg, nil
}
