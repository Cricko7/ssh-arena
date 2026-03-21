package grpcapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/chat"
	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/gameplay"
	"github.com/aeza/ssh-arena/internal/grpcjson"
	"github.com/aeza/ssh-arena/internal/roles"
	"github.com/aeza/ssh-arena/internal/state"
)

func TestTradeHistoryAndLeaderboardViaGRPC(t *testing.T) {
	ctx := context.Background()
	conn, clients, stores, cleanup := newIntegrationHarness(t)
	defer cleanup()
	defer conn.Close()

	alice := ensurePlayer(t, ctx, clients.accounts, "alice")
	bob := ensurePlayer(t, ctx, clients.accounts, "bob")
	seedPlayer(t, stores.players, alice.PlayerID, 100_000, map[string]int64{"TECH": 20})
	seedPlayer(t, stores.players, bob.PlayerID, 100_000, map[string]int64{"TECH": 0})

	executeAction(t, ctx, clients.game, alice.PlayerID, "sell-1", "place_order", `{"symbol":"TECH","side":"sell","type":"limit","price":1000,"quantity":2}`)
	executeAction(t, ctx, clients.game, bob.PlayerID, "buy-1", "place_order", `{"symbol":"TECH","side":"buy","type":"limit","price":1000,"quantity":2}`)

	historyResp := executeAction(t, ctx, clients.game, alice.PlayerID, "history-1", "trade.history", fmt.Sprintf(`{"player_id":"%s","since_seconds":3600,"limit":5}`, alice.PlayerID))
	var history struct {
		Type   string              `json:"type"`
		Count  int                 `json:"count"`
		Trades []state.TradeRecord `json:"trades"`
	}
	mustDecodeJSON(t, historyResp.ResponseJSON, &history)
	if history.Type != "trade.history" {
		t.Fatalf("expected trade.history, got %q", history.Type)
	}
	if history.Count != 1 || len(history.Trades) != 1 {
		t.Fatalf("expected one trade, got count=%d len=%d payload=%s", history.Count, len(history.Trades), historyResp.ResponseJSON)
	}
	if history.Trades[0].SellerID != alice.PlayerID || history.Trades[0].BuyerID != bob.PlayerID {
		t.Fatalf("unexpected trade participants: %+v", history.Trades[0])
	}

	leaderboardResp := executeAction(t, ctx, clients.game, alice.PlayerID, "leader-1", "stats.leaderboard", `{"window_seconds":3600,"limit":10}`)
	var leaderboard struct {
		Type    string                   `json:"type"`
		Count   int                      `json:"count"`
		Entries []state.LeaderboardEntry `json:"entries"`
	}
	mustDecodeJSON(t, leaderboardResp.ResponseJSON, &leaderboard)
	if leaderboard.Type != "stats.leaderboard" {
		t.Fatalf("expected stats.leaderboard, got %q", leaderboard.Type)
	}
	if leaderboard.Count < 2 || len(leaderboard.Entries) < 2 {
		t.Fatalf("expected at least two leaderboard entries, got %+v", leaderboard)
	}
	if !hasLeaderboardEntry(leaderboard.Entries, alice.PlayerID) || !hasLeaderboardEntry(leaderboard.Entries, bob.PlayerID) {
		t.Fatalf("missing players in leaderboard: %+v", leaderboard.Entries)
	}
	for _, entry := range leaderboard.Entries {
		if entry.PlayerID == alice.PlayerID || entry.PlayerID == bob.PlayerID {
			if entry.TradeCount != 1 || entry.Turnover != 2000 {
				t.Fatalf("unexpected leaderboard stats for %s: %+v", entry.PlayerID, entry)
			}
		}
	}
}

func TestMarketStreamPublishesTradeEnvelope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, clients, stores, cleanup := newIntegrationHarness(t)
	defer cleanup()
	defer conn.Close()

	alice := ensurePlayer(t, ctx, clients.accounts, "alice")
	bob := ensurePlayer(t, ctx, clients.accounts, "bob")
	seedPlayer(t, stores.players, alice.PlayerID, 100_000, map[string]int64{"TECH": 20})
	seedPlayer(t, stores.players, bob.PlayerID, 100_000, map[string]int64{"TECH": 0})

	stream, err := clients.game.GetMarketStream(ctx)
	if err != nil {
		t.Fatalf("open market stream: %v", err)
	}
	if err := stream.Send(&gamev1.MarketStreamRequest{PlayerID: bob.PlayerID, Symbols: []string{"TECH"}, IncludeTrades: true}); err != nil {
		t.Fatalf("subscribe market stream: %v", err)
	}

	executeAction(t, ctx, clients.game, alice.PlayerID, "sell-stream", "place_order", `{"symbol":"TECH","side":"sell","type":"limit","price":1000,"quantity":1}`)
	executeAction(t, ctx, clients.game, bob.PlayerID, "buy-stream", "place_order", `{"symbol":"TECH","side":"buy","type":"limit","price":1000,"quantity":1}`)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv market stream: %v", err)
		}
		if msg.Topic != "market" {
			continue
		}
		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		mustDecodeJSON(t, msg.JSON, &envelope)
		if envelope.Type != "market.trade" {
			continue
		}
		var trade state.TradeRecord
		if err := json.Unmarshal(envelope.Payload, &trade); err != nil {
			t.Fatalf("decode trade envelope: %v", err)
		}
		if trade.Symbol != "TECH" || trade.Quantity != 1 {
			t.Fatalf("unexpected streamed trade: %+v", trade)
		}
		return
	}
	t.Fatal("did not receive market.trade envelope")
}

type integrationStores struct {
	players     *state.PlayerStore
	tradeStore  *state.TradeStore
	performance *state.PerformanceStore
}

type integrationClients struct {
	accounts gamev1.AccountServiceClient
	game     gamev1.GameServiceClient
}

func newIntegrationHarness(t *testing.T) (*grpc.ClientConn, integrationClients, integrationStores, func()) {
	t.Helper()
	grpcjson.Register()

	baseDir := t.TempDir()
	players, err := state.LoadPlayerStore(filepath.Join(baseDir, "players.json"))
	if err != nil {
		t.Fatalf("load player store: %v", err)
	}
	trades, err := state.LoadTradeStore(filepath.Join(baseDir, "trades.json"), 1000)
	if err != nil {
		t.Fatalf("load trade store: %v", err)
	}
	performance, err := state.LoadPerformanceStore(filepath.Join(baseDir, "performance.json"), 2000)
	if err != nil {
		t.Fatalf("load performance store: %v", err)
	}

	allocator := roles.NewAllocatorWithSeed(roles.Config{
		Buyer:  roles.Profile{Cash: 100_000, BaseHoldings: map[string]int64{"*": 5}, VariancePct: 0},
		Holder: roles.Profile{Cash: 100_000, BaseHoldings: map[string]int64{"*": 5}, VariancePct: 0},
		Whale:  roles.Profile{Cash: 100_000, BaseHoldings: map[string]int64{"*": 5}, VariancePct: 0},
	}, 1)
	market := exchange.NewService([]exchange.Ticker{{
		Symbol:         "TECH",
		Name:           "Tech",
		Sector:         "technology",
		InitialPrice:   1000,
		TickSize:       5,
		LiquidityUnits: 10000,
		WhaleThreshold: 500,
	}}, chat.NewService(32), nil)
	engine := gameplay.NewEngine(players, trades, performance, allocator, market, chat.NewService(32), nil)
	api := New(engine)

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.ForceServerCodec(grpcjson.Codec{}))
	gamev1.RegisterAccountServiceServer(server, api)
	gamev1.RegisterGameServiceServer(server, api)
	go func() {
		_ = server.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(grpcjson.Codec{})),
		grpc.WithBlock(),
	)
	cancel()
	if err != nil {
		listener.Close()
		server.Stop()
		t.Fatalf("dial bufconn: %v", err)
	}

	cleanup := func() {
		conn.Close()
		server.Stop()
		listener.Close()
	}
	return conn, integrationClients{
		accounts: gamev1.NewAccountServiceClient(conn),
		game:     gamev1.NewGameServiceClient(conn),
	}, integrationStores{players: players, tradeStore: trades, performance: performance}, cleanup
}

func ensurePlayer(t *testing.T, ctx context.Context, client gamev1.AccountServiceClient, username string) *gamev1.PlayerBootstrapResponse {
	t.Helper()
	resp, err := client.EnsurePlayer(ctx, &gamev1.PlayerBootstrapRequest{SSHUsername: username, RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatalf("ensure player %s: %v", username, err)
	}
	return resp
}

func seedPlayer(t *testing.T, store *state.PlayerStore, playerID string, cash int64, portfolio map[string]int64) {
	t.Helper()
	_, err := store.UpdateByPlayerID(playerID, func(player *state.Player) error {
		player.Cash = cash
		player.ReservedCash = 0
		player.Portfolio = make(map[string]int64, len(portfolio))
		for symbol, qty := range portfolio {
			player.Portfolio[symbol] = qty
		}
		player.ReservedStocks = make(map[string]int64)
		return nil
	})
	if err != nil {
		t.Fatalf("seed player %s: %v", playerID, err)
	}
}

func executeAction(t *testing.T, ctx context.Context, client gamev1.GameServiceClient, playerID string, requestID string, actionID string, payload string) *gamev1.ActionResponse {
	t.Helper()
	resp, err := client.ExecuteAction(ctx, &gamev1.ActionRequest{
		RequestID:   requestID,
		PlayerID:    playerID,
		ActionID:    actionID,
		PayloadJSON: payload,
	})
	if err != nil {
		t.Fatalf("execute %s: %v", actionID, err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected ok for %s, got status=%s payload=%s", actionID, resp.Status, resp.ResponseJSON)
	}
	return resp
}

func mustDecodeJSON(t *testing.T, raw string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		t.Fatalf("decode json %s: %v", raw, err)
	}
}

func hasLeaderboardEntry(entries []state.LeaderboardEntry, playerID string) bool {
	for _, entry := range entries {
		if entry.PlayerID == playerID {
			return true
		}
	}
	return false
}
