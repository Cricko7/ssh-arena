# CLIENT_DEVELOPMENT_GUIDE

This guide explains how to build a client for `ssh-arena`.

It is written for developers who want to connect over SSH, talk to the gameplay backend over gRPC, parse JSON payloads, and render real-time market charts in the terminal.

The goal is simple: you should be able to copy-paste the examples here and get a live chart on screen in under 15 minutes.

## 1. Project Overview for Client Developers

The server has two network faces:

- SSH: login, identity bootstrap, simple terminal shell
- gRPC: gameplay actions, chat, market data, chart data

The important design decision is that **all gameplay payloads are JSON strings inside protobuf messages**.

That means your client can:

- use gRPC for transport and streaming
- use ordinary JSON parsing for all business payloads
- avoid deep coupling to internal server structs

Typical client workflow:

1. Connect over SSH to authenticate and obtain a bootstrap payload.
2. Read the bootstrap JSON for `player_id`, `role`, cash, and starting portfolio.
3. Open a gRPC connection to the game backend.
4. Subscribe to market and chart streams.
5. Render JSON payloads in your terminal UI.
6. Send gameplay commands with JSON strings through `ExecuteAction`.

## 2. Connection Model

### SSH for login and bootstrap

Example SSH login:

```bash
ssh -p 2222 alice@localhost
```

On first login the server prints a bootstrap JSON payload such as:

```json
{
  "type": "bootstrap",
  "player_id": "1f61e5d8-7059-4d9e-9fc7-56b2bb950f07",
  "username": "alice",
  "role": "Buyer",
  "cash": 348000,
  "portfolio": {
    "TECH": 8,
    "ENERGY": 7,
    "FOOD": 8,
    "CRYPTO": 8,
    "DEFENSE": 8,
    "PHARMA": 7,
    "ENTERTAINMENT": 8,
    "TRANSPORT": 8
  }
}
```

Save the `player_id`. Your gRPC client will typically use it in requests.

### gRPC for gameplay and streaming

Default server address:

```text
localhost:9090
```

Client responsibilities:

- create a gRPC channel
- send `ActionRequest` messages for gameplay
- subscribe to market stream and chart stream
- parse the embedded JSON

## 3. gRPC API Summary

Relevant protobuf messages:

```proto
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

message PriceChartTick {
  string ticker = 1;
  string json = 2;
  int64 sequence = 3;
}
```

Important RPCs:

```proto
service GameService {
  rpc ExecuteAction(ActionRequest) returns (ActionResponse);
  rpc GetMarketStream(stream MarketStreamRequest) returns (stream JsonEnvelope);
  rpc SubscribeToChart(stream ChartSubscriptionRequest) returns (stream PriceChartTick);
}

service ChatService {
  rpc SendChat(ChatRequest) returns (JsonEnvelope);
  rpc StreamChat(MarketStreamRequest) returns (stream JsonEnvelope);
}
```

## 4. Server Chart Stream

The chart engine publishes a `price_chart_tick` JSON document every configurable interval.

Default server config:

```yaml
chart_tick_interval_seconds: 3
chart_history_points: 240
chart_orderbook_depth: 10
```

Each tick contains enough information to build a live chart and order book panel.

Example payload:

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
    "bids": [
      {"price": 142.5, "qty": 1200},
      {"price": 142.25, "qty": 800}
    ],
    "asks": [
      {"price": 143.0, "qty": 1500},
      {"price": 143.25, "qty": 600}
    ]
  },
  "history": [
    {"ts": 1710941742000, "price": 141.2, "volume": 850},
    {"ts": 1710941802000, "price": 141.8, "volume": 1200}
  ]
}
```

## 5. How to Subscribe to Chart Stream

Use the dedicated bidirectional stream.

The request can be as small as:

```json
{
  "player_id": "1f61e5d8-7059-4d9e-9fc7-56b2bb950f07",
  "ticker": "TECH",
  "history_limit": 240,
  "orderbook_depth": 10
}
```

You usually send one request per ticker you want to watch.

## 6. Go gRPC Connection Example

### Install packages

```bash
go get google.golang.org/grpc
go get google.golang.org/protobuf
go get github.com/guptarohit/asciigraph
```

Generate Go stubs from the repo root:

```bash
protoc --go_out=. --go-grpc_out=. proto/game/v1/game.proto
```

### Minimal Go client main.go

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

    gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
)

type PriceChartTick struct {
    Type         string  `json:"type"`
    Ticker       string  `json:"ticker"`
    Timestamp    string  `json:"timestamp"`
    CurrentPrice float64 `json:"current_price"`
    Volume1m     int64   `json:"volume_1m"`
    Change1mPct  float64 `json:"change_1m_pct"`
    Change5mPct  float64 `json:"change_5m_pct"`
    Change15mPct float64 `json:"change_15m_pct"`
    Volatility5m float64 `json:"volatility_5m"`
    VWAP5m       float64 `json:"vwap_5m"`
    History      []struct {
        TS     int64   `json:"ts"`
        Price  float64 `json:"price"`
        Volume int64   `json:"volume"`
    } `json:"history"`
}

func main() {
    conn, err := grpc.Dial(
        "localhost:9090",
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    client := gamev1.NewGameServiceClient(conn)

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
    defer cancel()

    stream, err := client.SubscribeToChart(ctx)
    if err != nil {
        log.Fatal(err)
    }

    if err := stream.Send(&gamev1.ChartSubscriptionRequest{
        PlayerId:       "1f61e5d8-7059-4d9e-9fc7-56b2bb950f07",
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
        if err := json.Unmarshal([]byte(msg.Json), &tick); err != nil {
            log.Printf("bad json: %v", err)
            continue
        }

        prices := make([]float64, 0, len(tick.History))
        for _, point := range tick.History {
            prices = append(prices, point.Price)
        }

        fmt.Print("\033[2J\033[H")
        fmt.Printf("%s  price=%.2f  1m=%+.2f%%  5m=%+.2f%%  vol_1m=%d\n\n",
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

This is the fastest way to get a working line chart.

## 7. Python gRPC Connection Example

### Install packages

```bash
python -m pip install grpcio grpcio-tools rich asciichartpy
```

Generate Python stubs:

```bash
python -m grpc_tools.protoc -I./proto --python_out=. --grpc_python_out=. proto/game/v1/game.proto
```

### Minimal Python chart subscriber

```python
import json
import grpc
import asciichartpy
from rich.console import Console

import game_pb2
import game_pb2_grpc

console = Console()


def request_stream():
    yield game_pb2.ChartSubscriptionRequest(
        player_id="1f61e5d8-7059-4d9e-9fc7-56b2bb950f07",
        ticker="TECH",
        history_limit=120,
        orderbook_depth=10,
    )


def main():
    channel = grpc.insecure_channel("localhost:9090")
    stub = game_pb2_grpc.GameServiceStub(channel)

    for msg in stub.SubscribeToChart(request_stream()):
        tick = json.loads(msg.json)
        prices = [point["price"] for point in tick["history"]]
        console.clear()
        console.print(
            f"[bold cyan]{tick['ticker']}[/bold cyan] "
            f"price={tick['current_price']:.2f} "
            f"1m={tick['change_1m_pct']:+.2f}% "
            f"5m={tick['change_5m_pct']:+.2f}%"
        )
        console.print(asciichartpy.plot(prices, {'height': 12}))


if __name__ == "__main__":
    main()
```

## 8. How to Parse JSON Payloads

The rule is always the same:

1. receive protobuf message
2. take the JSON string field
3. run normal JSON decoding
4. render or route by `type`

### Go parsing snippet

```go
var payload map[string]any
if err := json.Unmarshal([]byte(msg.Json), &payload); err != nil {
    return err
}
switch payload["type"] {
case "price_chart_tick":
    // render chart
case "chat.message":
    // render chat line
case "market.update":
    // update orderbook view
}
```

### Python parsing snippet

```python
payload = json.loads(msg.json)
kind = payload.get("type")
if kind == "price_chart_tick":
    handle_chart(payload)
elif kind == "chat.message":
    handle_chat(payload)
```

## 9. Sending Commands

All gameplay commands go through `ExecuteAction`.

### PlaceOrder

```json
{
  "symbol": "TECH",
  "side": "buy",
  "type": "limit",
  "price": 1000,
  "quantity": 10
}
```

Go example:

```go
resp, err := gameClient.ExecuteAction(ctx, &gamev1.ActionRequest{
    RequestId:   "req-001",
    PlayerId:    playerID,
    ActionId:    "exchange.place_order",
    PayloadJson: `{"symbol":"TECH","side":"buy","type":"limit","price":1000,"quantity":10}`,
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(resp.ResponseJson)
```

### CancelOrder

If your server adds it later, it should follow the same pattern:

```json
{
  "order_id": "f67b54ce-2758-45b0-b507-6eb7613152c9"
}
```

### ChatMessage

```go
chatResp, err := chatClient.SendChat(ctx, &gamev1.ChatRequest{
    PlayerId: playerID,
    Json:     `{"body":"hello arena"}`,
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(chatResp.Json)
```

### Transfer money

```json
{
  "to_player_id": "8d06cb7b-7fdb-4f0f-89a4-1c7b0dcd8f52",
  "amount": 500
}
```

Use action id:

```text
player.transfer_money
```

## 10. Ready-to-use Chart Rendering Examples

### Go + asciigraph

Best option for a 5-minute working client.

```bash
go get github.com/guptarohit/asciigraph
```

Basic render:

```go
fmt.Println(asciigraph.Plot(prices, asciigraph.Height(12)))
```

### Go + Bubble Tea or termui

For a richer TUI, use chart ticks as your model update source.

Suggested architecture:

- one goroutine reads `SubscribeToChart`
- send decoded ticks into a channel
- Bubble Tea model stores latest history and orderbook
- render chart, summary stats, and top-of-book side by side

Pseudo-structure:

```go
type model struct {
    tick PriceChartTick
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch v := msg.(type) {
    case PriceChartTick:
        m.tick = v
    }
    return m, nil
}
```

Recommended sections:

- top summary: price, 1m, 5m, 15m, VWAP, volatility
- center chart: latest `history`
- right panel: top 10 bids and asks
- bottom panel: most recent trades and chat

### Python + rich + asciichart-py

The earlier Python example is enough for a working chart.

For a richer layout with `rich` panels:

```python
from rich.panel import Panel
from rich.columns import Columns

summary = Panel(
    f"price={tick['current_price']:.2f}\n"
    f"1m={tick['change_1m_pct']:+.2f}%\n"
    f"5m={tick['change_5m_pct']:+.2f}%\n"
    f"vwap_5m={tick['vwap_5m']:.2f}",
    title=tick['ticker'],
)
chart = Panel(asciichartpy.plot(prices, {'height': 12}), title="Chart")
console.print(Columns([summary, chart]))
```

### Minimal shell example with jq + watch

If you already have a helper that writes chart JSON lines to stdout, you can prototype quickly.

Example assuming a local file `chart.jsonl` is being appended to:

```bash
watch -n 1 "tail -n 1 chart.jsonl | jq '{ticker, current_price, change_1m_pct, volume_1m}'"
```

Plot history values roughly:

```bash
tail -n 1 chart.jsonl | jq -r '.history[].price'
```

If you expose gRPC through `grpcurl` plus a small converter, you can chain the output into `jq` the same way.

## 11. Example JSON Payloads

### price_chart_tick

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

### orderbook snapshot

```json
{
  "type": "market.snapshot",
  "payload": {
    "orderbook": {
      "symbol": "TECH",
      "last_trade_price": 1010,
      "sequence": 44,
      "bids": [
        {"price": 1005, "quantity": 130, "order_count": 4}
      ],
      "asks": [
        {"price": 1010, "quantity": 90, "order_count": 3}
      ],
      "generated_at": "2026-03-20T18:00:00Z"
    },
    "price": {
      "symbol": "TECH",
      "previous_price": 995,
      "current_price": 1000,
      "net_volume": 10,
      "buy_pressure": 10,
      "sell_pressure": 0,
      "whale_volume": 0,
      "whale_multiplier_bps": 10000,
      "move_bps": 50,
      "updated_at": "2026-03-20T18:00:05Z"
    }
  }
}
```

### trade

```json
{
  "id": "req-123-1",
  "symbol": "TECH",
  "price": 1010,
  "quantity": 15,
  "aggressor_side": "buy",
  "buy_order_id": "bid-77",
  "sell_order_id": "ask-12",
  "buyer_id": "player-a",
  "seller_id": "player-b",
  "buyer_role": "Whale",
  "seller_role": "Holder",
  "executed_at": "2026-03-20T18:00:05Z"
}
```

### chat

```json
{
  "type": "chat.message",
  "channel": "global",
  "player_id": "player-a",
  "username": "alice",
  "role": "Whale",
  "body": "TECH to the moon.",
  "sent_at": "2026-03-20T18:01:00Z"
}
```

## 12. Error Handling and Reconnection Tips

### Always expect these failure modes

- SSH disconnects
- gRPC stream reset
- JSON decode error from a malformed or future payload
- server restart
- network timeout

### Best practices

- reconnect gRPC streams with exponential backoff
- keep the last `player_id` and subscriptions locally
- on reconnect, resubscribe to chart and market streams
- ignore unknown JSON fields so new server versions do not break old clients
- log raw JSON when debugging parse errors

### Simple Go reconnection loop

```go
for {
    err := runChartClient()
    log.Printf("chart stream ended: %v", err)
    time.Sleep(2 * time.Second)
}
```

### Sequence handling

If you store `sequence` from `PriceChartTick`, you can detect dropped messages.

If a gap appears:

1. reconnect
2. request a fresh subscription
3. rebuild local state from the next full chart tick

## 13. How to Add New Features on the Client Side

Because the server sends JSON strings, adding client features is easy:

1. add a new JSON payload struct or dictionary parser
2. route by `type`
3. render it in your UI

Examples:

- new whale ability event
- random market event banner
- scoreboard stream
- alliance notifications
- per-symbol heat map

Typical approach in Go:

```go
type Envelope struct {
    Type string `json:"type"`
}

var env Envelope
_ = json.Unmarshal(raw, &env)
switch env.Type {
case "price_chart_tick":
    // chart panel
case "chat.message":
    // chat panel
case "market.update":
    // orderbook panel
case "event.market_crash":
    // event banner
}
```

## 14. Recommended Build Path for a New Client

If you are starting from scratch, do this:

1. SSH in manually once and capture your `player_id`.
2. Build the minimal Go or Python chart subscriber above.
3. Add market snapshot parsing.
4. Add `ExecuteAction` support for orders.
5. Add chat.
6. Add a richer TUI layout.

That path gets you from zero to useful very quickly.

## 15. Final Notes

- The server is intentionally JSON-first for client simplicity.
- Do not hardcode field order; always parse JSON by keys.
- Treat chart ticks as periodic state snapshots, not just deltas.
- Keep your renderer decoupled from your transport layer.
- If you can print the raw JSON and parse it, you can build a client for this system.


