package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
)

type Definition struct {
	ID          string         `json:"id"`
	Version     int            `json:"version"`
	Description string         `json:"description"`
	Trigger     string         `json:"trigger"`
	Handler     string         `json:"handler"`
	Impact      map[string]any `json:"impact"`
}

type Trigger struct {
	EventID string
	Payload json.RawMessage
}

type Handler interface {
	Handle(context.Context, Env, Trigger) error
}

type TxManager interface {
	WithinSerializable(context.Context, func(context.Context, pgx.Tx) error) error
}

type Env struct {
	TX TxManager
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]Definition
	handlers    map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{
		definitions: make(map[string]Definition),
		handlers:    make(map[string]Handler),
	}
}

var Default = NewRegistry()

func (r *Registry) RegisterDefinition(def Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.definitions[def.ID] = def
}

func (r *Registry) RegisterHandler(eventID string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[eventID] = handler
}

func (r *Registry) Dispatch(ctx context.Context, env Env, trigger Trigger) error {
	r.mu.RLock()
	handler, ok := r.handlers[trigger.EventID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("event %q is not registered", trigger.EventID)
	}

	return handler.Handle(ctx, env, trigger)
}

func MustRegisterHandler(eventID string, handler Handler) {
	Default.RegisterHandler(eventID, handler)
}
