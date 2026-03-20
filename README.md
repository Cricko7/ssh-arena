# ssh-arena

Terminal-first multiplayer exchange game server in Go.

`ssh-arena` now runs as a shared persistent market for all players:

- one global exchange for everyone online
- SSH bootstrap for first login and account recovery
- gRPC for all gameplay, chat, market streams, and chart data
- JSON payloads inside every gRPC message for easy terminal and bot clients
- price-time priority order books for 8 starter tickers
- random market events, rumors, fake news, paid analytics, and insider previews
- random but balanced starting resources by role
- player state persistence across reconnects

## What the game currently supports

Players can:

- connect once over SSH and receive a persistent `player_id`
- reconnect later and keep the same role, cash, and holdings
- place limit and market buy or sell orders
- cancel open orders
- inspect portfolio state and market snapshots
- subscribe to real-time order book, trade, chat, private intel, and chart feeds
- buy paid analytics and event-specific insider previews
- react to random public market events, rumors, and fake news
- participate in one shared market with every other player on the server

## Current architecture

### Connection model

The network flow is intentionally split into two parts:

1. SSH is used only for bootstrap.
2. The SSH gateway calls `AccountService.EnsurePlayer`.
3. The player receives a bootstrap JSON payload.
4. The SSH session closes.
5. The client then connects to gRPC for actual gameplay.

This keeps SSH simple and secure while moving all game logic to one backend API.

### Main services

- `cmd/ssh-server`: bootstrap-only SSH gateway
- `cmd/grpc-game`: gameplay backend
- `internal/exchange`: order book, matching, price engine, market events
- `internal/charting`: periodic chart ticks with history and top-of-book data
- `internal/chat`: global chat broadcast
- `internal/intel`: insiders, rumors, fake news, and paid analytics
- `internal/gameplay`: player actions, persistence glue, portfolio updates
- `internal/grpcapi`: gRPC transport layer

### Shared market model

There is exactly one exchange per running server process.

If 10 players connect, they all trade on the same books.
If 1000 players connect, they still trade on the same books.

There are no separate rooms, seasons, or private sessions at the transport level.

## Project layout

```text
.
|-- cmd/
|   |-- grpc-game/
|   `-- ssh-server/
|-- config/
|   |-- roles.json
|   `-- actions/
|-- docs/
|   `-- CLIENT_DEVELOPMENT_GUIDE.md
|-- events/
|   |-- stocks.json
|   |-- random_events.json
|   `-- intel_feeds.json
|-- gen/
|   `-- game/v1/
|-- internal/
|   |-- charting/
|   |-- chat/
|   |-- config/
|   |-- exchange/
|   |-- gameplay/
|   |-- grpcapi/
|   |-- grpcjson/
|   |-- intel/
|   |-- marketevents/
|   |-- orderbook/
|   |-- roles/
|   `-- state/
|-- proto/
|   `-- game/v1/game.proto
|-- Dockerfile
|-- docker-compose.yml
|-- config.yaml
`-- README.md
```

## Roles and starting resources

Players are assigned roles automatically on first bootstrap:

- about 10% become `Whale`
- the rest are balanced between `Buyer` and `Holder`

Resource generation is randomized within balanced role templates, so two `Holder` players are similar but not identical.

Role intent:

- `Buyer`: more cash, light inventory
- `Holder`: less cash, deeper inventory
- `Whale`: very large cash and inventory, enough to move thin books

## Tickers

The default market is loaded from [`events/stocks.json`](events/stocks.json):

- `TECH`
- `ENERGY`
- `FOOD`
- `CRYPTO`
- `DEFENSE`
- `PHARMA`
- `ENTERTAINMENT`
- `TRANSPORT`

## Market behavior

### Matching

`internal/orderbook` implements:

- price-time priority
- limit and market orders
- bid and ask levels
- partial fills
- resting orders
- order cancellation

### Price engine

`internal/exchange/price_engine.go` moves prices using a more market-like model than a simple trade-last-price update:

- net buy versus sell pressure
- top-of-book imbalance
- recent signed flow memory
- VWAP-like trade context
- mean reversion pressure
- whale amplification on large aggressive flow
- temporary event shock bias that decays over time

### Event and intel system

There are now two data-driven market content systems:

- [`events/random_events.json`](events/random_events.json): public random events
- [`events/intel_feeds.json`](events/intel_feeds.json): rumors, fake news, paid analytics, insiders

Kinds currently supported in `intel_feeds.json`:

- `rumor`
- `fake_news`
- `paid_analytics`
- `insider`

Behavior:

- `rumor` and `fake_news` are public and can move one ticker or the whole market
- `paid_analytics` is bought on demand and returns a private report immediately
- `insider` is bought on demand and arms a private preview for one future event
- when that insider event later triggers, buyers receive an `intel.insider.preview` on the private stream before the public event is published

## Data persistence

What is currently persisted:

- player identity
- role
- cash
- reserved cash
- portfolio
- reserved stocks
- timestamps

This state is stored in `data/players.json` and survives reconnects.

Important current limitation:

- live order books and open resting orders are still process-local in memory
- after a full backend restart, player accounts persist but live market books are rebuilt from scratch

The codebase still contains PostgreSQL transaction scaffolding, but the current live gameplay loop is not yet fully backed by PostgreSQL for every exchange mutation.

## Streams and APIs

### SSH bootstrap

Connect with:

```bash
ssh -p 2222 alice@localhost
```

Typical output:

```text
welcome, alice
grpc_endpoint=grpc-game:9090
{"type":"bootstrap",...}
{"type":"ssh.bootstrap.complete","message":"Use gRPC for gameplay commands, chat, charts and market streams."}
disconnecting from SSH bootstrap gateway
```

Important note when using Docker:

- `grpc_endpoint=grpc-game:9090` is the internal Docker network address
- external clients running on your host machine should usually connect to `localhost:9090`

### gRPC gameplay

Services:

- `AccountService.EnsurePlayer`
- `GameService.ExecuteAction`
- `GameService.GetMarketStream`
- `GameService.SubscribeToChart`
- `ChatService.SendChat`
- `ChatService.StreamChat`

All response payloads are JSON strings.

### Market stream topics

`GetMarketStream` emits `JsonEnvelope` messages with topics such as:

- `market`
- `chat`
- `portfolio`
- `private`

`private` is used for personal intel like insider previews.

## Supported actions

The gameplay backend currently accepts these action ids:

- `place_order`
- `exchange.place_order`
- `cancel_order`
- `exchange.cancel_order`
- `portfolio.get`
- `player.portfolio`
- `portfolio`
- `market.snapshot`
- `chat.send`
- `send_chat_message`
- `intel.catalog`
- `intel.list`
- `intel.buy`

### Example action payloads

Place order:

```json
{
  "symbol": "TECH",
  "side": "buy",
  "type": "limit",
  "price": 1000,
  "quantity": 10
}
```

Cancel order:

```json
{
  "symbol": "TECH",
  "order_id": "9de6d1ef-4132-45fd-8f7b-ec78d3d6f06d"
}
```

Buy analytics or insider feed:

```json
{
  "intel_id": "analytics.crypto.momentum"
}
```

## Example payloads

Bootstrap:

```json
{
  "type": "bootstrap",
  "player_id": "b2fb6a13-8abb-48cf-8356-fccc3392cbb9",
  "username": "alice",
  "role": "Holder",
  "cash": 125224,
  "portfolio": {
    "CRYPTO": 65,
    "DEFENSE": 58,
    "ENERGY": 56,
    "ENTERTAINMENT": 54,
    "FOOD": 53,
    "PHARMA": 53,
    "TECH": 69,
    "TRANSPORT": 57
  }
}
```

Market event:

```json
{
  "type": "market.event",
  "payload": {
    "kind": "rumor",
    "name": "Слухи о расследовании по TECH",
    "message": "По TECH пошли слухи о возможном расследовании. Лента быстро краснеет.",
    "global": false,
    "symbol": "TECH",
    "affected_symbols": ["TECH"],
    "multiplier_pct": -11,
    "duration_seconds": 70,
    "occurred_at": "2026-03-20T20:15:00Z"
  }
}
```

Private insider preview:

```json
{
  "type": "intel.insider.preview",
  "intel_id": "insider.musk.buy",
  "kind": "insider",
  "name": "Инсайд: крупная покупка TECH",
  "message": "Через несколько секунд рынок увидит новость по TECH. Готовься к импульсу вверх.",
  "symbol": "TECH",
  "global": false,
  "multiplier_pct": 17,
  "duration_seconds": 75,
  "scheduled_for": "2026-03-20T21:10:00Z",
  "previewed_at": "2026-03-20T21:09:42Z"
}
```

Paid analytics result:

```json
{
  "type": "intel.buy.result",
  "item": {
    "id": "analytics.crypto.momentum",
    "kind": "paid_analytics",
    "name": "Momentum-аналитика по <ticker>",
    "description": "Платный аналитический отчёт с bias, текущей ценой и стаканом.",
    "price": 6500
  },
  "cost": 6500,
  "payload": {
    "type": "intel.analytics.report",
    "symbol": "CRYPTO",
    "bias": "bullish",
    "expected_move_range": "5-12"
  }
}
```

## Build and run

### Docker

```bash
docker compose down -v --remove-orphans
docker compose up --build
```

### Local Go run

```bash
go mod tidy
go test ./...
go run ./cmd/grpc-game
go run ./cmd/ssh-server
```

## Configuration

Runtime settings live in [`config.yaml`](config.yaml):

```yaml
chart_tick_interval_seconds: 3
chart_history_points: 240
chart_orderbook_depth: 10
player_state_path: data/players.json
random_event_interval_seconds: 15
random_events_path: events/random_events.json
intel_event_interval_seconds: 12
intel_events_path: events/intel_feeds.json
```

## Client development

See the full client guide in [`docs/CLIENT_DEVELOPMENT_GUIDE.md`](docs/CLIENT_DEVELOPMENT_GUIDE.md).

It covers:

- SSH bootstrap flow
- JSON gRPC codec usage
- Go and Python client examples
- market, chart, chat, and private intel streams
- action payload examples
- rendering chart ticks in terminal clients
