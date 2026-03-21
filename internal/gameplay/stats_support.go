package gameplay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aeza/ssh-arena/internal/orderbook"
	"github.com/aeza/ssh-arena/internal/state"
)

type tradeHistoryPayload struct {
	PlayerID     string `json:"player_id"`
	Symbol       string `json:"symbol"`
	SinceSeconds int64  `json:"since_seconds"`
	Limit        int    `json:"limit"`
}

type leaderboardPayload struct {
	WindowSeconds int64 `json:"window_seconds"`
	Limit         int   `json:"limit"`
}

func (e *Engine) handleTradeHistory(req ExecuteActionRequest) (string, error) {
	if e.tradeHistory == nil {
		return "", fmt.Errorf("trade history is not configured")
	}
	var payload tradeHistoryPayload
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return "", fmt.Errorf("decode trade.history payload: %w", err)
		}
	}
	query := state.TradeQuery{
		PlayerID: strings.TrimSpace(payload.PlayerID),
		Symbol:   strings.ToUpper(strings.TrimSpace(payload.Symbol)),
		Limit:    payload.Limit,
	}
	if payload.SinceSeconds > 0 {
		query.Since = time.Now().UTC().Add(-time.Duration(payload.SinceSeconds) * time.Second)
	}
	trades := e.tradeHistory.Query(query)
	return marshalJSON(map[string]any{
		"type":    "trade.history",
		"filters": payload,
		"count":   len(trades),
		"trades":  trades,
	}), nil
}

func (e *Engine) handleLeaderboard(req ExecuteActionRequest) (string, error) {
	if e.performance == nil || e.tradeHistory == nil {
		return "", fmt.Errorf("leaderboard is not configured")
	}
	var payload leaderboardPayload
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return "", fmt.Errorf("decode stats.leaderboard payload: %w", err)
		}
	}
	if payload.WindowSeconds <= 0 {
		payload.WindowSeconds = 3600
	}
	if payload.Limit <= 0 {
		payload.Limit = 10
	}
	now := time.Now().UTC()
	entries := e.performance.Leaderboard(time.Duration(payload.WindowSeconds)*time.Second, now, e.tradeHistory.All(), payload.Limit)
	return marshalJSON(map[string]any{
		"type":           "stats.leaderboard",
		"window_seconds": payload.WindowSeconds,
		"generated_at":   now,
		"count":          len(entries),
		"entries":        entries,
	}), nil
}

func (e *Engine) persistTradeEffects(players map[string]*state.Player, trades []orderbook.Trade) error {
	if len(trades) == 0 {
		return nil
	}
	if e.tradeHistory != nil {
		records := make([]state.TradeRecord, 0, len(trades))
		for _, trade := range trades {
			buyer := players[trade.BuyerID]
			seller := players[trade.SellerID]
			records = append(records, state.TradeRecord{
				ID:             trade.ID,
				Symbol:         trade.Symbol,
				Price:          trade.Price,
				Quantity:       trade.Quantity,
				Notional:       trade.Price * trade.Quantity,
				AggressorSide:  string(trade.AggressorSide),
				BuyOrderID:     trade.BuyOrderID,
				SellOrderID:    trade.SellOrderID,
				BuyerID:        trade.BuyerID,
				BuyerUsername:  usernameOrEmpty(buyer),
				BuyerRole:      trade.BuyerRole,
				SellerID:       trade.SellerID,
				SellerUsername: usernameOrEmpty(seller),
				SellerRole:     trade.SellerRole,
				ExecutedAt:     trade.ExecutedAt,
			})
		}
		if err := e.tradeHistory.Append(records); err != nil {
			return err
		}
	}
	return e.recordPerformanceSnapshots(players)
}

func (e *Engine) recordPlayerSnapshot(player state.Player) error {
	if e.performance == nil {
		return nil
	}
	snapshots, err := e.buildPerformanceSnapshots(map[string]*state.Player{player.PlayerID: &player})
	if err != nil {
		return err
	}
	return e.performance.Append(snapshots)
}

func (e *Engine) recordPerformanceSnapshots(players map[string]*state.Player) error {
	if e.performance == nil || len(players) == 0 {
		return nil
	}
	snapshots, err := e.buildPerformanceSnapshots(players)
	if err != nil {
		return err
	}
	return e.performance.Append(snapshots)
}

func (e *Engine) buildPerformanceSnapshots(players map[string]*state.Player) ([]state.EquitySnapshot, error) {
	prices, err := e.market.PriceMap()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(players))
	for playerID := range players {
		ids = append(ids, playerID)
	}
	sort.Strings(ids)
	now := time.Now().UTC()
	snapshots := make([]state.EquitySnapshot, 0, len(ids))
	for _, playerID := range ids {
		player := players[playerID]
		if player == nil {
			continue
		}
		holdingsValue := int64(0)
		for _, symbol := range e.symbols {
			price := prices[symbol].CurrentPrice
			holdingsValue += player.Portfolio[symbol] * price
		}
		snapshots = append(snapshots, state.EquitySnapshot{
			PlayerID:      player.PlayerID,
			Username:      player.Username,
			Role:          player.Role,
			Cash:          player.Cash,
			HoldingsValue: holdingsValue,
			TotalEquity:   player.Cash + holdingsValue,
			CapturedAt:    now,
		})
	}
	return snapshots, nil
}

func usernameOrEmpty(player *state.Player) string {
	if player == nil {
		return ""
	}
	return player.Username
}
