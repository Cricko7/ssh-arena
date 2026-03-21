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

type TradeRecord struct {
	ID             string    `json:"id"`
	Symbol         string    `json:"symbol"`
	Price          int64     `json:"price"`
	Quantity       int64     `json:"quantity"`
	Notional       int64     `json:"notional"`
	AggressorSide  string    `json:"aggressor_side"`
	BuyOrderID     string    `json:"buy_order_id"`
	SellOrderID    string    `json:"sell_order_id"`
	BuyerID        string    `json:"buyer_id"`
	BuyerUsername  string    `json:"buyer_username"`
	BuyerRole      string    `json:"buyer_role"`
	SellerID       string    `json:"seller_id"`
	SellerUsername string    `json:"seller_username"`
	SellerRole     string    `json:"seller_role"`
	ExecutedAt     time.Time `json:"executed_at"`
}

type TradeQuery struct {
	PlayerID string
	Symbol   string
	Since    time.Time
	Until    time.Time
	Limit    int
}

type tradeSnapshot struct {
	Trades []TradeRecord `json:"trades"`
}

type TradeStore struct {
	mu         sync.Mutex
	path       string
	maxRecords int
	trades     []TradeRecord
}

func LoadTradeStore(path string, maxRecords int) (*TradeStore, error) {
	if maxRecords <= 0 {
		maxRecords = 50000
	}
	store := &TradeStore{
		path:       path,
		maxRecords: maxRecords,
		trades:     make([]TradeRecord, 0),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *TradeStore) load() error {
	var snap tradeSnapshot
	if err := jsonfile.Read(s.path, &snap); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read trade store: %w", err)
	}
	s.trades = append(s.trades[:0], snap.Trades...)
	return nil
}

func (s *TradeStore) Append(records []TradeRecord) error {
	if len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.trades = append(s.trades, records...)
	if len(s.trades) > s.maxRecords {
		s.trades = slices.Clone(s.trades[len(s.trades)-s.maxRecords:])
	}
	return s.persistLocked()
}

func (s *TradeStore) Query(query TradeQuery) []TradeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	out := make([]TradeRecord, 0, limit)
	for i := len(s.trades) - 1; i >= 0; i-- {
		record := s.trades[i]
		if query.Symbol != "" && record.Symbol != query.Symbol {
			continue
		}
		if query.PlayerID != "" && record.BuyerID != query.PlayerID && record.SellerID != query.PlayerID {
			continue
		}
		if !query.Since.IsZero() && record.ExecutedAt.Before(query.Since) {
			continue
		}
		if !query.Until.IsZero() && record.ExecutedAt.After(query.Until) {
			continue
		}
		out = append(out, record)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (s *TradeStore) All() []TradeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.trades)
}

func (s *TradeStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create trade store dir: %w", err)
	}
	raw, err := json.MarshalIndent(tradeSnapshot{Trades: s.trades}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trade store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return fmt.Errorf("write trade store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace trade store: %w", err)
	}
	return nil
}
