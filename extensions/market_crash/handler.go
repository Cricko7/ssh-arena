package market_crash

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aeza/ssh-arena/internal/events"
)

type Handler struct{}

type payload struct {
	Symbol string `json:"symbol"`
	Bps    int64  `json:"bps"`
}

func init() {
	events.MustRegisterHandler("market.crash", Handler{})
}

func (Handler) Handle(ctx context.Context, env events.Env, trigger events.Trigger) error {
	var p payload
	if err := json.Unmarshal(trigger.Payload, &p); err != nil {
		return fmt.Errorf("decode event payload: %w", err)
	}
	if p.Bps <= 0 || p.Bps >= 10_000 {
		return fmt.Errorf("bps must be between 1 and 9999")
	}

	return env.TX.WithinSerializable(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE asset_markets
			SET last_price = GREATEST(1, (last_price * (10000 - $2)) / 10000),
			    version = version + 1,
			    updated_at = NOW()
			WHERE symbol = $1
		`, p.Symbol, p.Bps); err != nil {
			return fmt.Errorf("update market price: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO market_events (event_id, event_type, symbol, payload_json)
			VALUES (gen_random_uuid(), 'market.crash', $1, $2)
		`, p.Symbol, trigger.Payload); err != nil {
			return fmt.Errorf("insert market event: %w", err)
		}

		return nil
	})
}
