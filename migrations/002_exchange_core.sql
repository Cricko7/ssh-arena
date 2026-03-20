CREATE TABLE player_roles (
    player_id UUID PRIMARY KEY REFERENCES players(player_id),
    role_name TEXT NOT NULL,
    bootstrap_json JSONB NOT NULL,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE price_history (
    history_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol TEXT NOT NULL REFERENCES asset_markets(symbol),
    previous_price BIGINT NOT NULL,
    current_price BIGINT NOT NULL,
    move_bps BIGINT NOT NULL,
    volume BIGINT NOT NULL DEFAULT 0,
    payload_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE chat_messages (
    message_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id UUID REFERENCES players(player_id),
    channel_name TEXT NOT NULL DEFAULT 'global',
    payload_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_price_history_symbol_created
    ON price_history (symbol, created_at DESC);

CREATE INDEX idx_chat_messages_channel_created
    ON chat_messages (channel_name, created_at DESC);
