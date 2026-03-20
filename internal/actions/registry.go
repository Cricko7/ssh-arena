package actions

import (
	"context"
	"fmt"
	"sync"
)

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

func (r *Registry) RegisterHandler(actionID string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[actionID] = handler
}

func (r *Registry) Dispatch(ctx context.Context, env Env, req Request) (Result, error) {
	r.mu.RLock()
	handler, ok := r.handlers[req.ActionID]
	r.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("action %q is not registered", req.ActionID)
	}

	return handler.Execute(ctx, env, req)
}

func MustRegisterHandler(actionID string, handler Handler) {
	Default.RegisterHandler(actionID, handler)
}
