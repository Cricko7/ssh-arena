package main

import (
	"context"
	"log"
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
	"github.com/aeza/ssh-arena/internal/marketevents"
	"github.com/aeza/ssh-arena/internal/roles"
	"github.com/aeza/ssh-arena/internal/state"
)

func main() {
	addr := envOr("GRPC_LISTEN_ADDR", ":9090")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtimeConfig, err := config.LoadRuntimeConfig("config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	tickers, err := exchange.LoadTickers("events/stocks.json")
	if err != nil {
		log.Fatal(err)
	}
	roleConfig, err := roles.LoadConfig("config/roles.json")
	if err != nil {
		log.Fatal(err)
	}
	eventDefs, err := marketevents.LoadDefinitions(runtimeConfig.RandomEventsPath)
	if err != nil {
		log.Fatal(err)
	}
	playerStore, err := state.LoadPlayerStore(runtimeConfig.PlayerStatePath)
	if err != nil {
		log.Fatal(err)
	}

	chatService := chat.NewService(128)
	var cache exchange.Cache
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		client := redis.NewClient(&redis.Options{Addr: redisAddr})
		cache = exchange.NewRedisCache(client)
	}
	exchangeService := exchange.NewService(tickers, chatService, cache)
	allocator := roles.NewAllocator(roleConfig)
	chartEngine := charting.NewEngine(charting.Config{
		TickInterval:   time.Duration(runtimeConfig.ChartTickIntervalSeconds) * time.Second,
		HistoryLimit:   runtimeConfig.ChartHistoryPoints,
		OrderbookDepth: runtimeConfig.ChartOrderbookDepth,
	}, exchangeService)
	chartEngine.Start(ctx)

	randomEvents, err := marketevents.NewEngine(marketevents.Config{
		Interval: time.Duration(runtimeConfig.RandomEventIntervalSecs) * time.Second,
	}, eventDefs, exchangeService)
	if err != nil {
		log.Fatal(err)
	}
	randomEvents.Start(ctx)

	gameEngine := gameplay.NewEngine(playerStore, allocator, exchangeService, chatService, chartEngine)
	api := grpcapi.New(gameEngine)
	grpcjson.Register()

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}

	server := grpc.NewServer(grpc.ForceServerCodec(grpcjson.Codec{}))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	gamev1.RegisterAccountServiceServer(server, api)
	gamev1.RegisterGameServiceServer(server, api)
	gamev1.RegisterChatServiceServer(server, api)
	reflection.Register(server)

	log.Printf("grpc game service listening on %s", addr)
	log.Printf("exchange core ready: tickers=%d chart_interval=%ds chart_history=%d chart_depth=%d random_event_interval=%ds random_events=%d cache_enabled=%t", len(tickers), runtimeConfig.ChartTickIntervalSeconds, runtimeConfig.ChartHistoryPoints, runtimeConfig.ChartOrderbookDepth, runtimeConfig.RandomEventIntervalSecs, len(eventDefs), cache != nil)
	if err := server.Serve(lis); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
