package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	gossh "github.com/gliderlabs/ssh"
	xssh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/grpcjson"
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
	addr := envOr("SSH_LISTEN_ADDR", ":2222")
	hostSigner := envOr("SSH_HOST_KEY_PATH", "./config/dev/ssh_host_ed25519")
	grpcTarget := envOr("GRPC_GAME_ADDR", envOr("GRPC_LISTEN_ADDR", "127.0.0.1:9090"))
	grpcPublicAddr := envOr("GRPC_PUBLIC_ADDR", grpcTarget)

	client, conn, err := newBootstrapClient(grpcTarget)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	server := &gossh.Server{
		Addr:             addr,
		Handler:          sessionHandler(client, grpcPublicAddr),
		PasswordHandler:  passwordHandler,
		PublicKeyHandler: publicKeyHandler,
		IdleTimeout:      2 * time.Minute,
		MaxTimeout:       10 * time.Minute,
	}

	if _, err := os.Stat(hostSigner); err == nil {
		server.SetOption(gossh.HostKeyFile(hostSigner))
	}

	log.Printf("ssh bootstrap gateway listening on %s, forwarding to %s, advertising %s", addr, grpcTarget, grpcPublicAddr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func sessionHandler(client *bootstrapClient, grpcPublicAddr string) gossh.Handler {
	return func(session gossh.Session) {
		ctx, cancel := context.WithTimeout(session.Context(), 5*time.Second)
		defer cancel()

		keyHash := ""
		if pk := session.PublicKey(); pk != nil {
			keyHash = xssh.FingerprintSHA256(pk)
		}

		resp, err := client.account.EnsurePlayer(ctx, &gamev1.PlayerBootstrapRequest{
			SSHUsername:          session.User(),
			RemoteAddr:           remoteIP(session.RemoteAddr()),
			PublicKeyFingerprint: keyHash,
		})
		if err != nil {
			_, _ = fmt.Fprintf(session, "bootstrap failed: %v\r\n", err)
			return
		}

		_, _ = fmt.Fprintf(session, "welcome, %s\r\n", session.User())
		_, _ = fmt.Fprintf(session, "grpc_endpoint=%s\r\n", grpcPublicAddr)
		_, _ = fmt.Fprintln(session, resp.BootstrapJSON)
		_, _ = fmt.Fprintln(session, `{"type":"ssh.bootstrap.complete","message":"Use gRPC for gameplay commands, chat, charts and market streams."}`)
		_, _ = fmt.Fprintln(session, "disconnecting from SSH bootstrap gateway")
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
