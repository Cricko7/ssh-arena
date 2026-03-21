package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	gossh "github.com/gliderlabs/ssh"
	xssh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/grpcjson"
	"github.com/aeza/ssh-arena/internal/logx"
)

type bootstrapClient struct {
	account gamev1.AccountServiceClient
}

func newBootstrapClient(target string) (*bootstrapClient, *grpc.ClientConn, error) {
	grpcjson.Register()
	codec := encoding.GetCodec("json")
	conn, err := grpc.Dial(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec)),
	)
	if err != nil {
		return nil, nil, err
	}
	return &bootstrapClient{account: gamev1.NewAccountServiceClient(conn)}, conn, nil
}

func main() {
	logger := logx.L("cmd.ssh-server")
	addr := envOr("SSH_LISTEN_ADDR", ":2222")
	hostSigner := envOr("SSH_HOST_KEY_PATH", "./config/dev/ssh_host_ed25519")
	grpcTarget := envOr("GRPC_GAME_ADDR", envOr("GRPC_LISTEN_ADDR", "127.0.0.1:9090"))
	grpcPublicAddr := envOr("GRPC_PUBLIC_ADDR", grpcTarget)

	client, conn, err := newBootstrapClient(grpcTarget)
	if err != nil {
		logger.Error("create bootstrap client", "error", err, "target", grpcTarget)
		os.Exit(1)
	}
	defer conn.Close()

	server := &gossh.Server{
		Addr:             addr,
		Handler:          sessionHandler(logger, client, grpcPublicAddr),
		PasswordHandler:  passwordHandler,
		PublicKeyHandler: publicKeyHandler,
		IdleTimeout:      2 * time.Minute,
		MaxTimeout:       10 * time.Minute,
	}

	if _, err := os.Stat(hostSigner); err == nil {
		server.SetOption(gossh.HostKeyFile(hostSigner))
		logger.Info("ssh host key loaded", "path", hostSigner)
	} else {
		logger.Warn("ssh host key not found, using in-memory key", "path", hostSigner, "error", err)
	}

	logger.Info("ssh bootstrap gateway listening", "addr", addr, "grpc_target", grpcTarget, "grpc_public_addr", grpcPublicAddr)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("ssh server stopped", "error", err)
		os.Exit(1)
	}
}

func sessionHandler(logger interface {
	Info(string, ...any)
	Warn(string, ...any)
}, client *bootstrapClient, grpcPublicAddr string) gossh.Handler {
	return func(session gossh.Session) {
		ctx, cancel := context.WithTimeout(session.Context(), 5*time.Second)
		defer cancel()

		keyHash := ""
		if pk := session.PublicKey(); pk != nil {
			keyHash = xssh.FingerprintSHA256(pk)
		}

		logger.Info("ssh bootstrap started", "username", session.User(), "remote_addr", remoteIP(session.RemoteAddr()), "has_public_key", keyHash != "")
		resp, err := client.account.EnsurePlayer(ctx, &gamev1.PlayerBootstrapRequest{
			SSHUsername:          session.User(),
			RemoteAddr:           remoteIP(session.RemoteAddr()),
			PublicKeyFingerprint: keyHash,
		})
		if err != nil {
			logger.Warn("ssh bootstrap failed", "username", session.User(), "error", err)
			_, _ = fmt.Fprintf(session, "bootstrap failed: %v\r\n", err)
			return
		}

		ps1URL := envOr("CLIENT_INSTALL_PS1_URL", "https://raw.githubusercontent.com/Cricko7/ssh-arena/main/scripts/install-client.ps1")
		shURL := envOr("CLIENT_INSTALL_SH_URL", "https://raw.githubusercontent.com/Cricko7/ssh-arena/main/scripts/install-client.sh")
		installMessage := map[string]any{
			"type":          "ssh.client.install",
			"message":       "If the local client is missing, install it once. The client will remember your player_id and server settings for later launches.",
			"player_id":     resp.PlayerID,
			"grpc_endpoint": grpcPublicAddr,
			"powershell":    fmt.Sprintf("irm %s | iex", ps1URL),
			"shell":         fmt.Sprintf("curl -fsSL %s | sh", shURL),
			"launch":        "game-client",
		}
		installJSON := marshalJSON(installMessage)

		_, _ = fmt.Fprintf(session, "welcome, %s\r\n", session.User())
		_, _ = fmt.Fprintf(session, "grpc_endpoint=%s\r\n", grpcPublicAddr)
		_, _ = fmt.Fprintln(session, resp.BootstrapJSON)
		_, _ = fmt.Fprintln(session, installJSON)
		_, _ = fmt.Fprintln(session, `{"type":"ssh.bootstrap.complete","message":"Use the local game-client for charts, gameplay, chat and market streams. It will reuse the saved player_id on the next launch."}`)
		_, _ = fmt.Fprintln(session, "disconnecting from SSH bootstrap gateway")
		logger.Info("ssh bootstrap completed", "username", session.User(), "player_id", resp.PlayerID, "role", resp.Role, "created", resp.Created)
	}
}

func marshalJSON(value any) string {
	raw, _ := grpcjson.Codec{}.Marshal(value)
	return string(raw)
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
	if value := os.Getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
