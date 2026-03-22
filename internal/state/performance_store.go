package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/aeza/ssh-arena/internal/jsonfile"
	"github.com/aeza/ssh-arena/internal/logx"
)

type EquitySnapshot struct {
	PlayerID      string    `json:"player_id"`
	Username      string    `json:"username"`
	Role          string    `json:"role"`
	Cash          int64     `json:"cash"`
	HoldingsValue int64     `json:"holdings_value"`
	TotalEquity   int64     `json:"total_equity"`
	CapturedAt    time.Time `json:"captured_at"`
}

type LeaderboardEntry struct {
	Rank          int       `json:"rank"`
	PlayerID      string    `json:"player_id"`
	Username      string    `json:"username"`
	Role          string    `json:"role"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
	EquityThen    int64     `json:"equity_then"`
	EquityNow     int64     `json:"equity_now"`
	PnL           int64     `json:"pnl"`
	ReturnPct     float64   `json:"return_pct"`
	TradeCount    int       `json:"trade_count"`
	BuyVolume     int64     `json:"buy_volume"`
	SellVolume    int64     `json:"sell_volume"`
	Turnover      int64     `json:"turnover"`
	LastUpdatedAt time.Time `json:"last_updated_at"`
}

type performanceSnapshot struct {
	Snapshots []EquitySnapshot `json:"snapshots"`
}

type PerformanceStore struct {
	mu           sync.Mutex
	path         string
	maxSnapshots int
	snapshots    []EquitySnapshot
	logger       *slog.Logger
}

func LoadPerformanceStore(path string, maxSnapshots int) (*PerformanceStore, error) {
	if maxSnapshots <= 0 {
		maxSnapshots = 100000
	}
	store := &PerformanceStore{
		path:         path,
		maxSnapshots: maxSnapshots,
		snapshots:    make([]EquitySnapshot, 0),
		logger:       logx.L("state.performance"),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	store.loggerOrDefault().Info("performance store ready", "path", path, "snapshots", len(store.snapshots), "max_snapshots", maxSnapshots)
	return store, nil
}

func (s *PerformanceStore) load() error {
	var snap performanceSnapshot
	if err := jsonfile.Read(s.path, &snap); err != nil {
		if os.IsNotExist(err) {
			s.loggerOrDefault().Info("performance store file not found", "path", s.path)
			return nil
		}
		return fmt.Errorf("read performance store: %w", err)
	}
	s.snapshots = append(s.snapshots[:0], snap.Snapshots...)
	return nil
}

func (s *PerformanceStore) Append(records []EquitySnapshot) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.snapshots = append(s.snapshots, records...)
	if len(s.snapshots) > s.maxSnapshots {
		s.snapshots = slices.Clone(s.snapshots[len(s.snapshots)-s.maxSnapshots:])
	}
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.loggerOrDefault().Info("performance snapshots persisted", "count", len(records), "snapshots", len(s.snapshots))
	return nil
}

func (s *PerformanceStore) Leaderboard(window time.Duration, now time.Time, trades []TradeRecord, limit int) []LeaderboardEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 10
	}
	if window <= 0 {
		window = time.Hour
	}
	windowStart := now.Add(-window)

	type playerStats struct {
		baseline EquitySnapshot
		hasBase  bool
		latest   EquitySnapshot
		hasLast  bool
		trades   int
		buyQty   int64
		sellQty  int64
		turnover int64
	}

	statsByPlayer := make(map[string]*playerStats)
	ensure := func(playerID string) *playerStats {
		if statsByPlayer[playerID] == nil {
			statsByPlayer[playerID] = &playerStats{}
		}
		return statsByPlayer[playerID]
	}

	for _, snap := range s.snapshots {
		playerStats := ensure(snap.PlayerID)
		if snap.CapturedAt.Before(windowStart) || snap.CapturedAt.Equal(windowStart) {
			if !playerStats.hasBase || snap.CapturedAt.After(playerStats.baseline.CapturedAt) {
				playerStats.baseline = snap
				playerStats.hasBase = true
			}
		}
		if snap.CapturedAt.After(windowStart) && !playerStats.hasBase {
			playerStats.baseline = snap
			playerStats.hasBase = true
		}
		if !playerStats.hasLast || snap.CapturedAt.After(playerStats.latest.CapturedAt) {
			playerStats.latest = snap
			playerStats.hasLast = true
		}
	}

	for _, trade := range trades {
		if trade.ExecutedAt.Before(windowStart) || trade.ExecutedAt.After(now) {
			continue
		}
		buyer := ensure(trade.BuyerID)
		buyer.trades++
		buyer.buyQty += trade.Quantity
		buyer.turnover += trade.Notional

		seller := ensure(trade.SellerID)
		seller.trades++
		seller.sellQty += trade.Quantity
		seller.turnover += trade.Notional
	}

	entries := make([]LeaderboardEntry, 0, len(statsByPlayer))
	for playerID, stats := range statsByPlayer {
		if !stats.hasLast || !stats.hasBase {
			continue
		}
		entry := LeaderboardEntry{
			PlayerID:      playerID,
			Username:      stats.latest.Username,
			Role:          stats.latest.Role,
			WindowStart:   windowStart,
			WindowEnd:     now,
			EquityThen:    stats.baseline.TotalEquity,
			EquityNow:     stats.latest.TotalEquity,
			PnL:           stats.latest.TotalEquity - stats.baseline.TotalEquity,
			TradeCount:    stats.trades,
			BuyVolume:     stats.buyQty,
			SellVolume:    stats.sellQty,
			Turnover:      stats.turnover,
			LastUpdatedAt: stats.latest.CapturedAt,
		}
		if stats.baseline.TotalEquity > 0 {
			entry.ReturnPct = math.Round((float64(entry.PnL)/float64(stats.baseline.TotalEquity))*10000) / 100
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].PnL == entries[j].PnL {
			if entries[i].ReturnPct == entries[j].ReturnPct {
				return entries[i].Turnover > entries[j].Turnover
			}
			return entries[i].ReturnPct > entries[j].ReturnPct
		}
		return entries[i].PnL > entries[j].PnL
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}
	s.loggerOrDefault().Info("leaderboard calculated", "window", window, "limit", limit, "entries", len(entries))
	return entries
}

func (s *PerformanceStore) loggerOrDefault() *slog.Logger {
	if s.logger == nil {
		s.logger = logx.L("state.performance")
	}
	return s.logger
}

func (s *PerformanceStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create performance store dir: %w", err)
	}
	raw, err := json.MarshalIndent(performanceSnapshot{Snapshots: s.snapshots}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode performance store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return fmt.Errorf("write performance store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace performance store: %w", err)
	}
	return nil
}
