package gameplay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aeza/ssh-arena/internal/intel"
	"github.com/aeza/ssh-arena/internal/state"
)

func (e *Engine) SetIntelEngine(engine *intel.Engine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.intel = engine
}

func (e *Engine) SubscribePrivate(ctx context.Context, playerID string) <-chan string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.privateSubs[playerID]; !ok {
		e.privateSubs[playerID] = make(map[int]chan string)
	}
	id := e.nextPrivateID
	e.nextPrivateID++
	ch := make(chan string, 32)
	e.privateSubs[playerID][id] = ch
	go func() {
		<-ctx.Done()
		e.mu.Lock()
		if subs := e.privateSubs[playerID]; subs != nil {
			delete(subs, id)
			if len(subs) == 0 {
				delete(e.privateSubs, playerID)
			}
		}
		close(ch)
		e.mu.Unlock()
	}()
	return ch
}

func (e *Engine) PrivateHistory(playerID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.privateHistorySnapshot(playerID)...)
}

func (e *Engine) privateHistorySnapshot(playerID string) []string {
	var history []string
	if e.chatStore != nil {
		for _, item := range e.chatStore.Query(state.ChatQuery{PlayerID: playerID, Limit: e.privateHistoryLimit}) {
			if item.Channel == "global" {
				continue
			}
			raw, err := json.Marshal(item)
			if err != nil {
				continue
			}
			history = append(history, string(raw))
		}
	}
	for _, item := range e.privateHistory[playerID] {
		history = append(history, item)
	}
	if len(history) > e.privateHistoryLimit {
		history = history[len(history)-e.privateHistoryLimit:]
	}
	return history
}

func (e *Engine) NotifyPlayer(playerID string, payload string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifyPlayerLocked(playerID, payload)
}

func (e *Engine) notifyPlayerLocked(playerID string, payload string) {
	e.privateHistory[playerID] = append(e.privateHistory[playerID], payload)
	if len(e.privateHistory[playerID]) > e.privateHistoryLimit {
		e.privateHistory[playerID] = e.privateHistory[playerID][len(e.privateHistory[playerID])-e.privateHistoryLimit:]
	}
	for _, ch := range e.privateSubs[playerID] {
		select {
		case ch <- payload:
		default:
		}
	}
}

func (e *Engine) handleIntelCatalog() (string, error) {
	if e.intel == nil {
		return "", fmt.Errorf("intel service is not configured")
	}
	return marshalJSON(map[string]any{
		"type":  "intel.catalog",
		"items": e.intel.Catalog(),
	}), nil
}

func (e *Engine) handleIntelBuy(ctx context.Context, req ExecuteActionRequest) (string, error) {
	if e.intel == nil {
		return "", fmt.Errorf("intel service is not configured")
	}
	player, err := e.requirePlayer(req.PlayerID)
	if err != nil {
		return "", err
	}
	var payload struct {
		IntelID string `json:"intel_id"`
	}
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode intel.buy payload: %w", err)
	}
	quote, err := e.intel.Quote(payload.IntelID)
	if err != nil {
		return "", err
	}
	availableCash := player.Cash - player.ReservedCash
	if availableCash < quote.Price {
		return "", fmt.Errorf("insufficient cash for intel: need %d, available %d", quote.Price, availableCash)
	}
	player.Cash -= quote.Price
	if err := e.players.Upsert(player); err != nil {
		return "", err
	}
	result, err := e.intel.Buy(ctx, player.PlayerID, payload.IntelID)
	if err != nil {
		player.Cash += quote.Price
		_ = e.players.Upsert(player)
		return "", err
	}
	if err := e.recordPlayerSnapshot(player); err != nil {
		return "", err
	}

	return marshalJSON(map[string]any{
		"type":      "intel.buy.result",
		"item":      quote,
		"cost":      result.Cost,
		"portfolio": e.snapshotPlayer(player),
		"payload":   decodeJSON(result.PayloadJSON),
	}), nil
}
