package actions

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type Definition struct {
	ID           string         `json:"id"`
	Version      int            `json:"version"`
	Route        string         `json:"route"`
	DisplayName  string         `json:"display_name"`
	RequiresAuth bool           `json:"requires_auth"`
	Idempotent   bool           `json:"idempotent"`
	CooldownMs   int            `json:"cooldown_ms"`
	InputSchema  map[string]any `json:"input_schema"`
}

type Request struct {
	RequestID string
	PlayerID  string
	ActionID  string
	Payload   json.RawMessage
	Metadata  map[string]string
}

type Result struct {
	EventName string
	Payload   any
}

type Handler interface {
	Execute(context.Context, Env, Request) (Result, error)
}

type TxManager interface {
	WithinSerializable(context.Context, func(context.Context, pgx.Tx) error) error
}

type EventPublisher interface {
	Publish(ctx context.Context, channel string, payload any) error
}

type Env struct {
	TX     TxManager
	Events EventPublisher
}
