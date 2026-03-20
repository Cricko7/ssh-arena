package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	gossh "github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	xssh "golang.org/x/crypto/ssh"

	"github.com/aeza/ssh-arena/internal/exchange"
	"github.com/aeza/ssh-arena/internal/roles"
)

type BootstrapRequest struct {
	Username        string
	RemoteAddr      string
	PublicKeySHA256 string
}

type BootstrapResponse struct {
	PlayerID      string
	Role          string
	BootstrapJSON string
}

type ActionRequest struct {
	RequestID string
	PlayerID  string
	ActionID  string
	JSON      string
}

type GameplayGateway interface {
	EnsurePlayer(ctx context.Context, req BootstrapRequest) (BootstrapResponse, error)
	ExecuteAction(ctx context.Context, req ActionRequest) error
}

type memoryGateway struct {
	mu        sync.Mutex
	allocator *roles.Allocator
	symbols   []string
	stats     roles.Stats
	players   map[string]BootstrapResponse
}

func newMemoryGateway() (*memoryGateway, error) {
	roleConfig, err := roles.LoadConfig("config/roles.json")
	if err != nil {
		return nil, err
	}
	tickers, err := exchange.LoadTickers("events/stocks.json")
	if err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(tickers))
	for _, ticker := range tickers {
		symbols = append(symbols, ticker.Symbol)
	}

	return &memoryGateway{
		allocator: roles.NewAllocator(roleConfig),
		symbols:   symbols,
		players:   make(map[string]BootstrapResponse),
	}, nil
}

func (g *memoryGateway) EnsurePlayer(_ context.Context, req BootstrapRequest) (BootstrapResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if player, ok := g.players[req.Username]; ok {
		return player, nil
	}

	assignment := g.allocator.Assign(req.Username, g.stats, g.symbols)
	player := BootstrapResponse{
		PlayerID: uuid.NewString(),
		Role:     string(assignment.Role),
	}
	payload, err := json.Marshal(map[string]any{
		"type":      "bootstrap",
		"player_id": player.PlayerID,
		"username":  req.Username,
		"role":      player.Role,
		"cash":      assignment.Cash,
		"portfolio": assignment.Holdings,
	})
	if err != nil {
		return BootstrapResponse{}, err
	}
	player.BootstrapJSON = string(payload)
	g.players[req.Username] = player

	switch assignment.Role {
	case roles.RoleBuyer:
		g.stats.Buyers++
	case roles.RoleHolder:
		g.stats.Holders++
	case roles.RoleWhale:
		g.stats.Whales++
	}

	return player, nil
}

func (g *memoryGateway) ExecuteAction(_ context.Context, req ActionRequest) error {
	log.Printf("forward action player=%s action=%s payload=%s", req.PlayerID, req.ActionID, req.JSON)
	return nil
}

func main() {
	addr := envOr("SSH_LISTEN_ADDR", ":2222")
	hostSigner := envOr("SSH_HOST_KEY_PATH", "./config/dev/ssh_host_ed25519")
	gateway, err := newMemoryGateway()
	if err != nil {
		log.Fatal(err)
	}

	server := &gossh.Server{
		Addr:             addr,
		Handler:          sessionHandler(gateway),
		PasswordHandler:  passwordHandler,
		PublicKeyHandler: publicKeyHandler,
		IdleTimeout:      10 * time.Minute,
		MaxTimeout:       24 * time.Hour,
	}

	if _, err := os.Stat(hostSigner); err == nil {
		server.SetOption(gossh.HostKeyFile(hostSigner))
	}

	log.Printf("ssh gateway listening on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func sessionHandler(gateway GameplayGateway) gossh.Handler {
	return func(session gossh.Session) {
		ctx, cancel := context.WithTimeout(session.Context(), 5*time.Second)
		defer cancel()

		keyHash := ""
		if pk := session.PublicKey(); pk != nil {
			keyHash = xssh.FingerprintSHA256(pk)
		}

		player, err := gateway.EnsurePlayer(ctx, BootstrapRequest{
			Username:        session.User(),
			RemoteAddr:      remoteIP(session.RemoteAddr()),
			PublicKeySHA256: keyHash,
		})
		if err != nil {
			_, _ = fmt.Fprintf(session, "bootstrap failed: %v\r\n", err)
			return
		}

		_, _ = fmt.Fprintf(session, "welcome, %s\r\n", session.User())
		_, _ = fmt.Fprintf(session, "player_id=%s role=%s\r\n", player.PlayerID, player.Role)
		_, _ = fmt.Fprintln(session, player.BootstrapJSON)
		_, _ = fmt.Fprintln(session, "type: help")

		scanner := bufio.NewScanner(session)
		for {
			_, _ = fmt.Fprint(session, "arena> ")
			if !scanner.Scan() {
				return
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if line == "quit" || line == "exit" {
				_, _ = fmt.Fprintln(session, "bye")
				return
			}
			if line == "help" {
				_, _ = fmt.Fprintln(session, "place_order {\"symbol\":\"TECH\",\"side\":\"buy\",\"type\":\"limit\",\"price\":1000,\"quantity\":10}")
				_, _ = fmt.Fprintln(session, "chat.send {\"body\":\"hello arena\"}")
				_, _ = fmt.Fprintln(session, "market.subscribe {\"symbols\":[\"TECH\",\"CRYPTO\"]}")
				_, _ = fmt.Fprintln(session, "portfolio.get {}")
				continue
			}

			parts := strings.SplitN(line, " ", 2)
			actionID := parts[0]
			payload := "{}"
			if len(parts) == 2 {
				payload = parts[1]
			}

			reqCtx, reqCancel := context.WithTimeout(session.Context(), 3*time.Second)
			err := gateway.ExecuteAction(reqCtx, ActionRequest{
				RequestID: uuid.NewString(),
				PlayerID:  player.PlayerID,
				ActionID:  actionID,
				JSON:      payload,
			})
			reqCancel()
			if err != nil {
				_, _ = fmt.Fprintf(session, "error: %v\r\n", err)
				continue
			}

			_, _ = fmt.Fprintln(session, "accepted")
		}
	}
}

func passwordHandler(_ gossh.Context, password string) bool {
	return len(password) >= 12
}

func publicKeyHandler(_ gossh.Context, _ gossh.PublicKey) bool {
	return true
}

func remoteIP(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
