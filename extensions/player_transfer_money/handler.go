package player_transfer_money

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aeza/ssh-arena/internal/actions"
)

var errDuplicateRequest = errors.New("duplicate request_id")

type Handler struct{}

type input struct {
	ToPlayerID string `json:"to_player_id"`
	Amount     int64  `json:"amount"`
}

func init() {
	actions.MustRegisterHandler("player.transfer_money", Handler{})
}

func (Handler) Execute(ctx context.Context, env actions.Env, req actions.Request) (actions.Result, error) {
	var payload input
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return actions.Result{}, fmt.Errorf("decode payload: %w", err)
	}
	if payload.Amount <= 0 {
		return actions.Result{}, fmt.Errorf("amount must be positive")
	}
	if payload.ToPlayerID == "" || payload.ToPlayerID == req.PlayerID {
		return actions.Result{}, fmt.Errorf("invalid recipient")
	}

	response := map[string]any{
		"from_player_id": req.PlayerID,
		"to_player_id":   payload.ToPlayerID,
		"amount":         payload.Amount,
	}

	if err := env.TX.WithinSerializable(ctx, func(ctx context.Context, tx pgx.Tx) error {
		requestUUID, err := uuid.Parse(req.RequestID)
		if err != nil {
			return fmt.Errorf("request_id must be uuid: %w", err)
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO action_journal (request_id, player_id, action_id, status)
			VALUES ($1, $2, $3, 'started')
			ON CONFLICT (request_id) DO NOTHING
		`, requestUUID, req.PlayerID, req.ActionID)
		if err != nil {
			return fmt.Errorf("insert action journal: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return errDuplicateRequest
		}

		first, second := lockOrder(req.PlayerID, payload.ToPlayerID)

		if _, err := tx.Exec(ctx, `SELECT player_id FROM wallets WHERE player_id = $1 FOR UPDATE`, first); err != nil {
			return fmt.Errorf("lock first wallet: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT player_id FROM wallets WHERE player_id = $1 FOR UPDATE`, second); err != nil {
			return fmt.Errorf("lock second wallet: %w", err)
		}

		var balance int64
		if err := tx.QueryRow(ctx, `SELECT balance FROM wallets WHERE player_id = $1`, req.PlayerID).Scan(&balance); err != nil {
			return fmt.Errorf("load sender balance: %w", err)
		}
		if balance < payload.Amount {
			return fmt.Errorf("insufficient funds")
		}

		if _, err := tx.Exec(ctx, `
			UPDATE wallets
			SET balance = balance - $2, version = version + 1, updated_at = NOW()
			WHERE player_id = $1
		`, req.PlayerID, payload.Amount); err != nil {
			return fmt.Errorf("debit sender: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE wallets
			SET balance = balance + $2, version = version + 1, updated_at = NOW()
			WHERE player_id = $1
		`, payload.ToPlayerID, payload.Amount); err != nil {
			return fmt.Errorf("credit receiver: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries (entry_id, request_id, player_id, entry_type, amount)
			VALUES
				(gen_random_uuid(), $1, $2, 'transfer_out', -$4),
				(gen_random_uuid(), $1, $3, 'transfer_in', $4)
		`, requestUUID, req.PlayerID, payload.ToPlayerID, payload.Amount); err != nil {
			return fmt.Errorf("insert ledger: %w", err)
		}

		responseJSON, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode action response: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE action_journal
			SET status = 'committed', response_json = $2, committed_at = NOW()
			WHERE request_id = $1
		`, requestUUID, responseJSON); err != nil {
			return fmt.Errorf("finalize action journal: %w", err)
		}

		return nil
	}); err != nil {
		return actions.Result{}, err
	}

	return actions.Result{
		EventName: "wallet.transfer.completed",
		Payload:   response,
	}, nil
}

func lockOrder(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}
