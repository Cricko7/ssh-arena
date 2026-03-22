# ssh-arena

Terminal-first multiplayer exchange game server in Go.

`ssh-arena` is a shared market simulation where all players trade on one global exchange. SSH is used only for bootstrap, and all gameplay happens over gRPC with JSON payloads.

## What the game does today

- one global exchange for every online player
- SSH bootstrap for first login and account recovery
- gRPC gameplay API for orders, chat, market streams, and charts
- price-time priority order books
- dynamic price engine with order-flow pressure and event shocks
- random market events, rumors, fake news, paid analytics, and insider previews
- random but balanced starting resources by role
- persisted player state across reconnects

## Player flow

There are now two supported onboarding flows.

### Flow A: download one binary and start playing

1. Download `game-client.exe`.
2. Run it:

```powershell
.\game-client.exe
```

3. If no local profile exists, the client asks for:

- `Server SSH address`
- `SSH username`
- `SSH password`

4. The client performs SSH bootstrap, saves the returned `player_id`, fetches the market catalog, and immediately opens the game session.

After that, the same player can just run:

```powershell
.\game-client.exe
```

### Flow B: connect over SSH first and copy the generated install command

1. Connect over SSH:

```bash
ssh -p 2222 alice@localhost
```

2. Enter the SSH password once.
3. The SSH gateway returns:

- bootstrap JSON with `player_id`, role, holdings, and a short-lived `bootstrap_token`
- a ready-to-run client download command for PowerShell
- a ready-to-run client download command for POSIX shell

4. Copy the generated command and run it locally.
5. The client starts with `--bootstrap-token`, exchanges it for the real player session, saves the profile, and opens the game without asking for the SSH password a second time.
## High-level architecture

```mermaid
flowchart TD
    Player["Player Client"] --> SSH["SSH Bootstrap Gateway\ncmd/ssh-server"]
    SSH --> Account["AccountService.EnsurePlayer"]
    Player --> GRPC["gRPC Gameplay API\ncmd/grpc-game"]
    GRPC --> Gameplay["gameplay.Engine"]
    Gameplay --> Exchange["exchange.Service"]
    Gameplay --> Chat["chat.Service"]
    Gameplay --> Charting["charting.Engine"]
    Gameplay --> Intel["intel.Engine"]
    Exchange --> OrderBook["orderbook"]
    Exchange --> PriceEngine["price_engine"]
    Exchange --> Events["marketevents.Engine"]
    Gameplay --> PlayerStore["state.PlayerStore"]
    Exchange --> Redis[(Redis cache / fan-out)]
    GRPC --> Postgres[(PostgreSQL container)]
```

## Runtime responsibilities

### `cmd/ssh-server`
- accepts password or public-key SSH login
- performs bootstrap only
- prints the advertised gRPC endpoint and bootstrap JSON
- closes the SSH session after bootstrap

### `cmd/grpc-game`
- starts the gameplay backend
- wires market, charting, chat, random events, and intel feeds together
- exposes `AccountService`, `GameService`, and `ChatService`

### `internal/gameplay`
- validates player actions
- keeps player portfolio and reservation state in sync
- routes `ExecuteAction` requests to the right subsystem
- exposes portfolio snapshots and private intel subscriptions

### `internal/exchange`
- owns the shared market instance
- manages the order books and matching pipeline
- recalculates prices after trades and shocks
- publishes market updates to subscribers

### `internal/intel`
- loads purchasable and public intel feeds from JSON
- supports `rumor`, `fake_news`, `paid_analytics`, and `insider`
- sends private insider previews before public market publication

### `internal/charting`
- emits periodic `price_chart_tick` snapshots
- includes price, history, VWAP, volatility, and top-of-book levels

### `internal/state`
- persists players to `data/players.json`
- keeps role, cash, reserved cash, holdings, and reserved stocks across reconnects

## Project structure

```text
.
|-- cmd/
|   |-- grpc-game/
|   |   `-- main.go
|   `-- ssh-server/
|       `-- main.go
|-- config/
|   |-- actions/
|   `-- roles.json
|-- docs/
|   `-- CLIENT_DEVELOPMENT_GUIDE.md
|-- events/
|   |-- intel_feeds.json
|   |-- random_events.json
|   `-- stocks.json
|-- extensions/
|   |-- market_crash/
|   `-- player_transfer_money/
|-- gen/
|   `-- game/v1/
|-- internal/
|   |-- actions/
|   |-- charting/
|   |-- chat/
|   |-- config/
|   |-- events/
|   |-- exchange/
|   |-- gameplay/
|   |-- grpcapi/
|   |-- grpcjson/
|   |-- intel/
|   |-- jsonfile/
|   |-- marketevents/
|   |-- orderbook/
|   |-- platform/
|   |-- roles/
|   `-- state/
|-- migrations/
|-- proto/
|   `-- game/v1/game.proto
|-- Dockerfile
|-- docker-compose.yml
|-- config.yaml
`-- README.md
```

## Roles and starting resources

Players are assigned one of three roles on first bootstrap:

- `Buyer`: more cash, lighter inventory
- `Holder`: less cash, deeper inventory
- `Whale`: large cash and inventory, able to move thin books

Role assignment is randomized but population-balanced:

- around 10% whales
- the rest split between buyers and holders
- starting resources vary within role templates instead of being identical clones

## Tickers

Default markets are loaded from `events/stocks.json`.

The starter list includes:

- `TECH`
- `ENERGY`
- `FOOD`
- `CRYPTO`
- `DEFENSE`
- `PHARMA`
- `ENTERTAINMENT`
- `TRANSPORT`

## Market model

### Matching

The order book supports:

- limit and market orders
- price-time priority
- partial fills
- resting orders
- cancellation

### Price formation

The price engine combines:

- signed buy and sell pressure
- top-of-book imbalance
- recent flow memory
- mean reversion bias
- whale amplification
- temporary event shocks

### Intel and market content

Two JSON-driven content systems shape the market:

- `events/random_events.json` for public random events
- `events/intel_feeds.json` for rumors, fake news, paid analytics, and insiders

Current intel kinds:

- `rumor`
- `fake_news`
- `paid_analytics`
- `insider`

## API surface

### SSH bootstrap

Connect with:

```bash
ssh -p 2222 alice@localhost
```

The SSH gateway is bootstrap-only. It does not host the full game shell anymore.

A real session now returns three important payloads:

1. advertised endpoints
2. bootstrap JSON
3. install-and-run commands with a one-time bootstrap token

Typical response:

```text
welcome, alice
ssh_endpoint=localhost:2222
grpc_endpoint=localhost:9090
{"type":"bootstrap","player_id":"...","username":"alice","role":"Holder","cash":125224,"portfolio":{...},"bootstrap_token":"<ONE_TIME_TOKEN>","bootstrap_token_expires_at":"2026-03-22T12:34:56Z"}
{"type":"ssh.client.install","powershell_binary":"... --ssh 'localhost:2222' --grpc 'localhost:9090' --user 'alice' --bootstrap-token '<ONE_TIME_TOKEN>'","powershell_script":"...","shell":"..."}
{"type":"ssh.bootstrap.complete","message":"Use one of the install-and-run commands above to download the client and immediately open the game. Next launches can use the saved profile with no extra flags."}
disconnecting from SSH bootstrap gateway
```

The token is generated by the server automatically and already inserted into the printed command. The player does not need to copy it by hand.
### gRPC services

- `AccountService.EnsurePlayer`
- `GameService.ExecuteAction`
- `GameService.GetMarketStream`
- `GameService.SubscribeToChart`
- `ChatService.SendChat`
- `ChatService.StreamChat`

All gameplay payloads are JSON strings inside gRPC messages.

### Market stream topics

`GetMarketStream` emits topics such as:

- `market`
- `chat`
- `portfolio`
- `private`

`private` is used for player-specific intel, such as insider previews.

## Supported actions

The current gameplay backend accepts these action ids:

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

### Standalone client usage

If the player already has the binary, the simplest flow is:

```powershell
.\game-client.exe
```

On first launch the client will prompt for SSH address, username, and password, then save a local profile.

If the player came from SSH bootstrap, they can also use the generated one-liner command printed by the SSH gateway.

Example PowerShell command generated by the server:

```powershell
$dir=Join-Path $HOME '.ssh-arena\bin'; New-Item -ItemType Directory -Force -Path $dir | Out-Null; $exe=Join-Path $dir 'game-client.exe'; irm 'https://raw.githubusercontent.com/Cricko7/ssh-arena/main/bin/game-client.exe' -OutFile $exe; & $exe --ssh 'localhost:2222' --grpc 'localhost:9090' --user 'alice' --bootstrap-token '<ONE_TIME_TOKEN>'
```

Example POSIX shell command generated by the server:

```bash
tmp="$(mktemp)"; curl -fsSL 'https://raw.githubusercontent.com/Cricko7/ssh-arena/main/scripts/install-client.sh' -o "$tmp"; sh "$tmp" --ssh 'localhost:2222' --grpc 'localhost:9090' --user 'alice' --bootstrap-token '<ONE_TIME_TOKEN>'
```
## Configuration

Runtime settings live in `config.yaml`:

```yaml
chart_tick_interval_seconds: 3
chart_history_points: 2400
chart_orderbook_depth: 10
player_state_path: data/players.json
random_event_interval_seconds: 90
random_events_path: events/random_events.json
intel_event_interval_seconds: 12
intel_events_path: events/intel_feeds.json
```

## Persistence and current limitations

Persisted today:

- player identity
- role
- cash and reserved cash
- holdings and reserved stocks
- timestamps

Current limitation:

- live order books and resting orders are still process-local
- after a full backend restart, player accounts remain but live books are rebuilt

The repository still contains PostgreSQL transaction scaffolding, but the current live exchange loop is not yet fully backed by PostgreSQL for every mutation.

## Client development

See `docs/CLIENT_DEVELOPMENT_GUIDE.md` for:

- bootstrap flow
- JSON gRPC codec details
- Go and Python client examples
- market, chart, chat, and private intel streams
- action payload examples

