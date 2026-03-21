package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/charting"
	"github.com/aeza/ssh-arena/internal/chat"
	"github.com/aeza/ssh-arena/internal/config"
	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/gameplay"
	"github.com/aeza/ssh-arena/internal/grpcapi"
	"github.com/aeza/ssh-arena/internal/grpcjson"
	"github.com/aeza/ssh-arena/internal/intel"
	"github.com/aeza/ssh-arena/internal/logx"
	"github.com/aeza/ssh-arena/internal/marketevents"
	"github.com/aeza/ssh-arena/internal/roles"
	"github.com/aeza/ssh-arena/internal/state"
)

func main() {
	logger := logx.L("cmd.grpc-game")
	addr := envOr("GRPC_LISTEN_ADDR", ":9090")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtimeConfig, err := config.LoadRuntimeConfig("config.yaml")
	if err != nil {
		fatalErr(logger, "load runtime config", err)
	}
	tickers, err := exchange.LoadTickers("events/stocks.json")
	if err != nil {
		fatalErr(logger, "load tickers", err)
	}
	roleConfig, err := roles.LoadConfig("config/roles.json")
	if err != nil {
		fatalErr(logger, "load role config", err)
	}
	eventDefs, err := marketevents.LoadDefinitions(runtimeConfig.RandomEventsPath)
	if err != nil {
		fatalErr(logger, "load random events", err)
	}
	intelDefs, err := intel.LoadDefinitions(runtimeConfig.IntelEventsPath)
	if err != nil {
		fatalErr(logger, "load intel feeds", err)
	}
	playerStore, err := state.LoadPlayerStore(runtimeConfig.PlayerStatePath)
	if err != nil {
		fatalErr(logger, "load player store", err)
	}
	tradeStore, err := state.LoadTradeStore(runtimeConfig.TradeHistoryPath, 100000)
	if err != nil {
		fatalErr(logger, "load trade store", err)
	}
	performanceStore, err := state.LoadPerformanceStore(runtimeConfig.PerformanceHistoryPath, 200000)
	if err != nil {
		fatalErr(logger, "load performance store", err)
	}
	chatStore, err := state.LoadChatStore(runtimeConfig.ChatHistoryPath, 20000)
	if err != nil {
		fatalErr(logger, "load chat store", err)
	}

	chatService := chat.NewService(128, chatStore)
	var cache exchange.Cache
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		client := redis.NewClient(&redis.Options{Addr: redisAddr})
		cache = exchange.NewRedisCache(client)
		logger.Info("redis cache enabled", "addr", redisAddr)
	} else {
		logger.Info("redis cache disabled")
	}
	exchangeService := exchange.NewService(tickers, chatService, cache)
	allocator := roles.NewAllocator(roleConfig)
	chartEngine := charting.NewEngine(charting.Config{
		TickInterval:   time.Duration(runtimeConfig.ChartTickIntervalSeconds) * time.Second,
		HistoryLimit:   runtimeConfig.ChartHistoryPoints,
		OrderbookDepth: runtimeConfig.ChartOrderbookDepth,
	}, exchangeService)
	chartEngine.Start(ctx)

	gameEngine := gameplay.NewEngine(playerStore, tradeStore, performanceStore, chatStore, allocator, exchangeService, chatService, chartEngine)
	intelEngine, err := intel.NewEngine(intel.Config{
		Interval: time.Duration(runtimeConfig.IntelEventIntervalSecs) * time.Second,
	}, intelDefs, exchangeService, gameEngine)
	if err != nil {
		fatalErr(logger, "create intel engine", err)
	}
	gameEngine.SetIntelEngine(intelEngine)

	randomEvents, err := marketevents.NewEngine(marketevents.Config{
		Interval: time.Duration(runtimeConfig.RandomEventIntervalSecs) * time.Second,
	}, eventDefs, exchangeService, intelEngine)
	if err != nil {
		fatalErr(logger, "create random events engine", err)
	}
	randomEvents.Start(ctx)
	intelEngine.Start(ctx)

	api := grpcapi.New(gameEngine)
	grpcjson.Register()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fatalErr(logger, "listen grpc", err)
	}

	server := grpc.NewServer(grpc.ForceServerCodec(grpcjson.Codec{}))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	gamev1.RegisterAccountServiceServer(server, api)
	gamev1.RegisterGameServiceServer(server, api)
	gamev1.RegisterChatServiceServer(server, api)
	reflection.Register(server)

	logger.Info("grpc game service listening", "addr", addr)
	logger.Info("exchange core ready",
		"tickers", len(tickers),
		"chart_interval", fmt.Sprintf("%ds", runtimeConfig.ChartTickIntervalSeconds),
		"chart_history", runtimeConfig.ChartHistoryPoints,
		"chart_depth", runtimeConfig.ChartOrderbookDepth,
		"random_event_interval", fmt.Sprintf("%ds", runtimeConfig.RandomEventIntervalSecs),
		"random_events", len(eventDefs),
		"intel_interval", fmt.Sprintf("%ds", runtimeConfig.IntelEventIntervalSecs),
		"intel_defs", len(intelDefs),
		"cache_enabled", cache != nil,
		"trade_history", runtimeConfig.TradeHistoryPath,
		"performance_history", runtimeConfig.PerformanceHistoryPath,
		"chat_history", runtimeConfig.ChatHistoryPath,
	)
	if err := server.Serve(lis); err != nil {
		fatalErr(logger, "serve grpc", err)
	}
}

func fatalErr(logger interface{ Error(string, ...any) }, message string, err error) {
	logger.Error(message, "error", err)
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
