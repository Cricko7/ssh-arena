CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE players (
    player_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE player_identities (
    identity_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id UUID NOT NULL REFERENCES players(player_id),
    auth_kind TEXT NOT NULL,
    auth_subject TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (auth_kind, auth_subject)
);

CREATE TABLE wallets (
    player_id UUID PRIMARY KEY REFERENCES players(player_id),
    balance BIGINT NOT NULL DEFAULT 0,
    reserved_balance BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE asset_markets (
    symbol TEXT PRIMARY KEY,
    last_price BIGINT NOT NULL,
    tick_size BIGINT NOT NULL,
    version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE portfolios (
    player_id UUID NOT NULL REFERENCES players(player_id),
    symbol TEXT NOT NULL REFERENCES asset_markets(symbol),
    quantity BIGINT NOT NULL DEFAULT 0,
    reserved_quantity BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (player_id, symbol)
);

CREATE TABLE orders (
    order_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id UUID NOT NULL REFERENCES players(player_id),
    symbol TEXT NOT NULL REFERENCES asset_markets(symbol),
    side TEXT NOT NULL,
    order_type TEXT NOT NULL,
    price BIGINT,
    quantity BIGINT NOT NULL,
    remaining_quantity BIGINT NOT NULL,
    status TEXT NOT NULL,
    request_id UUID NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE trades (
    trade_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol TEXT NOT NULL REFERENCES asset_markets(symbol),
    buy_order_id UUID NOT NULL REFERENCES orders(order_id),
    sell_order_id UUID NOT NULL REFERENCES orders(order_id),
    buyer_id UUID NOT NULL REFERENCES players(player_id),
    seller_id UUID NOT NULL REFERENCES players(player_id),
    quantity BIGINT NOT NULL,
    price BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE ledger_entries (
    entry_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id UUID NOT NULL,
    player_id UUID NOT NULL REFERENCES players(player_id),
    entry_type TEXT NOT NULL,
    amount BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE action_journal (
    request_id UUID PRIMARY KEY,
    player_id UUID NOT NULL REFERENCES players(player_id),
    action_id TEXT NOT NULL,
    status TEXT NOT NULL,
    response_json JSONB,
    committed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE market_events (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL,
    symbol TEXT,
    payload_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_orders_symbol_status_price_created
    ON orders (symbol, status, price, created_at);

CREATE INDEX idx_trades_symbol_created
    ON trades (symbol, created_at DESC);

CREATE INDEX idx_ledger_entries_player_created
    ON ledger_entries (player_id, created_at DESC);
