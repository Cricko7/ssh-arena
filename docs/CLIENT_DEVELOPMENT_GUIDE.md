# CLIENT_DEVELOPMENT_GUIDE

This guide explains how to build a client for `ssh-arena` in its current form.

The most important thing to know up front is this:

- SSH is used only for bootstrap and account recovery
- actual gameplay happens over gRPC
- the server uses a JSON codec over gRPC, so payloads are JSON rather than protobuf binary frames

That design keeps the server easy to script against and makes terminal clients much simpler.

## 1. Connection overview

The full player flow is:

1. Connect over SSH.
2. Read the bootstrap JSON.
3. Save the `player_id`.
4. Connect to gRPC.
5. Subscribe to streams.
6. Send gameplay actions through `ExecuteAction`.

### SSH bootstrap example

```bash
ssh -p 2222 alice@localhost
```

Typical response:

```text
welcome, alice
grpc_endpoint=grpc-game:9090
{"cash":125224,"created_at":"2026-03-20T20:24:02.81335148Z","last_login":"2026-03-20T20:24:02.813351608Z","player_id":"b2fb6a13-8abb-48cf-8356-fccc3392cbb9","portfolio":{"CRYPTO":65,"DEFENSE":58,"ENERGY":56,"ENTERTAINMENT":54,"FOOD":53,"PHARMA":53,"TECH":69,"TRANSPORT":57},"role":"Holder","type":"bootstrap","username":"alice"}
{"type":"ssh.bootstrap.complete","message":"Use gRPC for gameplay commands, chat, charts and market streams."}
disconnecting from SSH bootstrap gateway
```

### Important Docker note

If you are running the server via Docker Compose, the bootstrap line may show:

```text
grpc_endpoint=grpc-game:9090
```

That is the internal container network address.

If your client runs on your host machine or in WSL, connect to:

```text
localhost:9090
```

## 2. Runtime transport details

The transport is unusual and worth calling out clearly.

The server does expose gRPC service names and method names from `proto/game/v1/game.proto`, but the message serialization is JSON via a custom codec.

That means:

- you can still use gRPC streams and RPC semantics
- you should serialize request structs as JSON
- you should deserialize response bodies from JSON
- standard generated protobuf-only clients will not work unless they are configured to use JSON serialization

Inside this repository, the relevant files are:

- [`gen/game/v1/game.go`](../gen/game/v1/game.go)
- [`internal/grpcjson/codec.go`](../internal/grpcjson/codec.go)
- [`proto/game/v1/game.proto`](../proto/game/v1/game.proto)

## 3. API summary

### Services

- `AccountService.EnsurePlayer`
- `GameService.ExecuteAction`
- `GameService.GetMarketStream`
- `GameService.SubscribeToChart`
- `ChatService.SendChat`
- `ChatService.StreamChat`

### Core message shapes

```proto
message JsonEnvelope {
  string topic = 1;
  string json = 2;
  int64 sequence = 3;
}

message ActionRequest {
  string request_id = 1;
  string player_id = 2;
  string action_id = 3;
  string payload_json = 4;
  map<string, string> metadata = 5;
}

message ActionResponse {
  string request_id = 1;
  string action_id = 2;
  string status = 3;
  string response_json = 4;
}

message MarketStreamRequest {
  string player_id = 1;
  repeated string symbols = 2;
  bool include_orderbook = 3;
  bool include_trades = 4;
  bool include_portfolio = 5;
  bool include_chat = 6;
}

message ChartSubscriptionRequest {
  string player_id = 1;
  string ticker = 2;
  uint32 history_limit = 3;
  uint32 orderbook_depth = 4;
}
```

## 4. Stream model

### Market stream

`GameService.GetMarketStream` is a bidirectional stream.

You send one or more `MarketStreamRequest` messages describing what you want, and the server returns `JsonEnvelope` messages.

Envelope topics currently include:

- `market`
- `chat`
- `portfolio`
- `private`

Use `private` for personal intel such as insider previews.

### Chart stream

`GameService.SubscribeToChart` is a bidirectional stream.

You send a `ChartSubscriptionRequest` per ticker, and the server replies with `PriceChartTick` messages.

Each chart tick contains:

- current price
- 1m volume
- 1m, 5m, and 15m percent change
- 5m volatility
- 5m VWAP
- top-of-book bids and asks
- recent history points for terminal chart rendering

### Chat stream

`ChatService.StreamChat` is a server-streaming RPC for global chat only.

If you already use `GetMarketStream` with `include_chat=true`, you usually do not need a separate chat stream.

## 5. Supported actions

Use these action ids with `GameService.ExecuteAction`:

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

## 6. Common JSON payloads

### Place order

```json
{
  "symbol": "TECH",
  "side": "buy",
  "type": "limit",
  "price": 1000,
  "quantity": 10
}
```

### Cancel order

```json
{
  "symbol": "TECH",
  "order_id": "9de6d1ef-4132-45fd-8f7b-ec78d3d6f06d"
}
```

### Portfolio request

```json
{}
```

### Market snapshot request

```json
{
  "symbol": "TECH"
}
```

### Chat message

```json
{
  "body": "TECH looks strong"
}
```

### Intel catalog request

```json
{}
```

### Buy analytics or insider access

```json
{
  "intel_id": "analytics.crypto.momentum"
}
```

## 7. Example JSON payloads from the server

### Bootstrap

```json
{
  "cash": 125224,
  "created_at": "2026-03-20T20:24:02.81335148Z",
  "last_login": "2026-03-20T20:24:02.813351608Z",
  "player_id": "b2fb6a13-8abb-48cf-8356-fccc3392cbb9",
  "portfolio": {
    "CRYPTO": 65,
    "DEFENSE": 58,
    "ENERGY": 56,
    "ENTERTAINMENT": 54,
    "FOOD": 53,
    "PHARMA": 53,
    "TECH": 69,
    "TRANSPORT": 57
  },
  "role": "Holder",
  "type": "bootstrap",
  "username": "alice"
}
```

### Market snapshot

```json
{
  "type": "market.snapshot",
  "payload": {
    "symbol": "TECH",
    "orderbook": {
      "bids": [
        {"price": 1005, "quantity": 130, "order_count": 4}
      ],
      "asks": [
        {"price": 1010, "quantity": 90, "order_count": 3}
      ]
    },
    "price": {
      "symbol": "TECH",
      "current_price": 1010,
      "previous_price": 1000,
      "move_bps": 100
    }
  }
}
```

### Chart tick

```json
{
  "type": "price_chart_tick",
  "ticker": "TECH",
  "timestamp": "2026-03-20T14:35:42Z",
  "current_price": 142.75,
  "volume_1m": 12400,
  "change_1m_pct": 1.8,
  "change_5m_pct": 4.2,
  "change_15m_pct": -2.1,
  "volatility_5m": 0.023,
  "vwap_5m": 141.9,
  "orderbook": {
    "bids": [{"price": 142.5, "qty": 1200}],
    "asks": [{"price": 143.0, "qty": 1500}]
  },
  "history": [
    {"ts": 1710941742000, "price": 141.2, "volume": 850},
    {"ts": 1710941802000, "price": 141.8, "volume": 1200}
  ]
}
```

### Public market event

```json
{
  "type": "market.event",
  "payload": {
    "kind": "fake_news",
    "name": "Фейковая новость о прорыве в CRYPTO",
    "message": "В сети разгоняют новость о прорыве в CRYPTO. Толпа покупает на эмоциях.",
    "global": false,
    "symbol": "CRYPTO",
    "affected_symbols": ["CRYPTO"],
    "multiplier_pct": 13,
    "duration_seconds": 55,
    "occurred_at": "2026-03-20T20:40:00Z"
  }
}
```

### Private insider preview

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

### Paid analytics response

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
    "global": false,
    "bias": "bullish",
    "expected_move_range": "5-12",
    "current_price": 1145,
    "last_move_bps": 73
  }
}
```

## 8. Go client example

This example uses the repository's generated service definitions from `gen/game/v1` and a local JSON gRPC codec.

### Minimal codec for external Go clients

```go
package main

import "encoding/json"

type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func (jsonCodec) Name() string                    { return "json" }
```

### Full bootstrap-to-chart example

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "time"

    "github.com/guptarohit/asciigraph"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/encoding"

    gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
)

type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)     { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func (jsonCodec) Name() string                      { return "json" }

type PriceChartTick struct {
    Type         string  `json:"type"`
    Ticker       string  `json:"ticker"`
    CurrentPrice float64 `json:"current_price"`
    Change1mPct  float64 `json:"change_1m_pct"`
    Change5mPct  float64 `json:"change_5m_pct"`
    Volume1m     int64   `json:"volume_1m"`
    History      []struct {
        TS     int64   `json:"ts"`
        Price  float64 `json:"price"`
        Volume int64   `json:"volume"`
    } `json:"history"`
}

func main() {
    encoding.RegisterCodec(jsonCodec{})

    conn, err := grpc.Dial(
        "localhost:9090",
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    playerID := "b2fb6a13-8abb-48cf-8356-fccc3392cbb9"
    gameClient := gamev1.NewGameServiceClient(conn)

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
    defer cancel()

    stream, err := gameClient.SubscribeToChart(ctx)
    if err != nil {
        log.Fatal(err)
    }

    if err := stream.Send(&gamev1.ChartSubscriptionRequest{
        PlayerID:       playerID,
        Ticker:         "TECH",
        HistoryLimit:   120,
        OrderbookDepth: 10,
    }); err != nil {
        log.Fatal(err)
    }

    for {
        msg, err := stream.Recv()
        if err != nil {
            log.Fatal(err)
        }

        var tick PriceChartTick
        if err := json.Unmarshal([]byte(msg.JSON), &tick); err != nil {
            log.Printf("bad json: %v", err)
            continue
        }

        prices := make([]float64, 0, len(tick.History))
        for _, point := range tick.History {
            prices = append(prices, point.Price)
        }

        fmt.Print("\033[2J\033[H")
        fmt.Printf("%s price=%.2f 1m=%+.2f%% 5m=%+.2f%% volume_1m=%d\n\n",
            tick.Ticker,
            tick.CurrentPrice,
            tick.Change1mPct,
            tick.Change5mPct,
            tick.Volume1m,
        )
        fmt.Println(asciigraph.Plot(prices, asciigraph.Height(12)))
    }
}
```

### Example action call from Go

```go
resp, err := gameClient.ExecuteAction(ctx, &gamev1.ActionRequest{
    RequestID:   "req-1",
    PlayerID:    playerID,
    ActionID:    "intel.catalog",
    PayloadJSON: `{}`,
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(resp.ResponseJSON)
```

## 9. Python client example

Python gRPC does not support the same generated-client shortcut as cleanly here because the server uses JSON serialization instead of protobuf binary messages.

The easiest approach is to use generic method handlers with JSON serializers.

### Install packages

```bash
python -m pip install grpcio rich asciichartpy
```

### Generic Python chart subscriber

```python
import json
import grpc
import asciichartpy
from rich.console import Console

console = Console()


def encode(obj):
    return json.dumps(obj).encode("utf-8")


def decode(data):
    return json.loads(data.decode("utf-8"))


def chart_requests(player_id, ticker):
    yield {
        "player_id": player_id,
        "ticker": ticker,
        "history_limit": 120,
        "orderbook_depth": 10,
    }


channel = grpc.insecure_channel("localhost:9090")
subscribe = channel.stream_stream(
    "/game.v1.GameService/SubscribeToChart",
    request_serializer=encode,
    response_deserializer=decode,
)

for msg in subscribe(chart_requests("b2fb6a13-8abb-48cf-8356-fccc3392cbb9", "TECH")):
    tick = json.loads(msg["json"])
    prices = [point["price"] for point in tick["history"]]
    console.clear()
    console.print(
        f"[bold cyan]{tick['ticker']}[/bold cyan] "
        f"price={tick['current_price']:.2f} "
        f"1m={tick['change_1m_pct']:+.2f}% "
        f"5m={tick['change_5m_pct']:+.2f}%"
    )
    console.print(asciichartpy.plot(prices, {"height": 12}))
```

### Generic Python action call

```python
import json
import grpc


def encode(obj):
    return json.dumps(obj).encode("utf-8")


def decode(data):
    return json.loads(data.decode("utf-8"))


channel = grpc.insecure_channel("localhost:9090")
execute_action = channel.unary_unary(
    "/game.v1.GameService/ExecuteAction",
    request_serializer=encode,
    response_deserializer=decode,
)

resp = execute_action({
    "request_id": "req-2",
    "player_id": "b2fb6a13-8abb-48cf-8356-fccc3392cbb9",
    "action_id": "place_order",
    "payload_json": json.dumps({
        "symbol": "TECH",
        "side": "buy",
        "type": "limit",
        "price": 1000,
        "quantity": 5,
    }),
    "metadata": {},
})

print(resp["response_json"])
```

## 10. Full pipeline examples

### Minimal market stream subscription

Send this request first:

```json
{
  "player_id": "b2fb6a13-8abb-48cf-8356-fccc3392cbb9",
  "symbols": ["TECH", "CRYPTO"],
  "include_orderbook": true,
  "include_trades": true,
  "include_portfolio": true,
  "include_chat": true
}
```

Then handle envelopes by topic:

- `market`: snapshots, trades, public events
- `chat`: global chat and system chat events
- `portfolio`: portfolio snapshots
- `private`: insider previews and other personal intel

### Buy insider access, then wait on private stream

1. Call `ExecuteAction` with `action_id = "intel.buy"`.
2. Use payload `{"intel_id":"insider.musk.buy"}`.
3. Keep `GetMarketStream` open.
4. Wait for a `private` topic envelope.
5. Parse `envelope.json` and look for `type = "intel.insider.preview"`.

## 11. Parsing strategy

The safest client-side pattern is:

1. decode the outer gRPC message
2. extract the JSON string field
3. decode that JSON into a generic envelope or typed struct
4. branch on `type`

### Go pattern

```go
type Envelope struct {
    Type string `json:"type"`
}

var env Envelope
if err := json.Unmarshal([]byte(msg.JSON), &env); err != nil {
    return err
}

switch env.Type {
case "market.snapshot":
case "market.update":
case "market.event":
case "price_chart_tick":
case "chat.message":
case "intel.insider.preview":
}
```

### Python pattern

```python
payload = json.loads(msg["json"])
kind = payload.get("type")
if kind == "market.event":
    handle_event(payload)
elif kind == "intel.insider.preview":
    handle_private_intel(payload)
```

## 12. Error handling and reconnection

Always expect:

- SSH disconnect after bootstrap
- gRPC stream closure
- malformed or future JSON fields
- server restarts
- sequence gaps

Recommended behavior:

- treat SSH disconnect after bootstrap as normal
- persist `player_id` locally
- reconnect gRPC with exponential backoff
- resubscribe to all streams after reconnect
- ignore unknown JSON fields
- rebuild local UI state from fresh snapshots after reconnect

### Simple Go reconnect loop

```go
for {
    err := runClient()
    log.Printf("client disconnected: %v", err)
    time.Sleep(2 * time.Second)
}
```

## 13. Chart rendering options

### Go + asciigraph

Best option for getting something working in minutes.

```bash
go get github.com/guptarohit/asciigraph
```

### Go + Bubble Tea or termui

Recommended layout:

- top summary: price, 1m, 5m, 15m, volatility, VWAP
- center chart: `history`
- right column: top 10 bids and asks
- bottom row: recent market events, chat, private intel

### Python + rich + asciichartpy

Good for fast prototyping with decent terminal visuals.

### Shell + jq

If another helper process writes JSONL to stdout or disk, you can inspect it quickly:

```bash
watch -n 1 "tail -n 1 chart.jsonl | jq '{ticker, current_price, change_1m_pct, volume_1m}'"
```

## 14. Extending your client

Because gameplay payloads are JSON, client-side feature growth is cheap.

When the server adds new mechanics such as:

- new public event kinds
- new intel product kinds
- leaderboards
- alliance updates
- sabotage mechanics

you usually only need to:

1. add one new JSON struct or parser
2. route on a new `type`
3. render it in your UI

## 15. Honest current limitations

A few important realities for client developers:

- SSH is bootstrap-only and is expected to disconnect right away.
- The server currently persists player accounts and holdings, but live order books are still process-local.
- After a full backend restart, accounts persist but resting live orders do not.
- The repository includes `proto/game/v1/game.proto`, but the wire format in practice is JSON codec over gRPC.

If you design your client around snapshots plus resubscription, you will be in a good place even as the server evolves.
