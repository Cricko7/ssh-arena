package main

import (
	"context"
	"encoding/json"
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
	sshPublicAddr := envOr("SSH_PUBLIC_ADDR", publicListenAddr(addr, "localhost"))

	client, conn, err := newBootstrapClient(grpcTarget)
	if err != nil {
		logger.Error("create bootstrap client", "error", err, "target", grpcTarget)
		os.Exit(1)
	}
	defer conn.Close()

	server := &gossh.Server{
		Addr:             addr,
		Handler:          sessionHandler(logger, client, grpcPublicAddr, sshPublicAddr),
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

	logger.Info("ssh bootstrap gateway listening", "addr", addr, "ssh_public_addr", sshPublicAddr, "grpc_target", grpcTarget, "grpc_public_addr", grpcPublicAddr)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("ssh server stopped", "error", err)
		os.Exit(1)
	}
}

func sessionHandler(logger interface {
	Info(string, ...any)
	Warn(string, ...any)
}, client *bootstrapClient, grpcPublicAddr string, sshPublicAddr string) gossh.Handler {
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

		var bootstrap struct {
			BootstrapToken string `json:"bootstrap_token"`
		}
		_ = json.Unmarshal([]byte(resp.BootstrapJSON), &bootstrap)

		ps1URL := envOr("CLIENT_INSTALL_PS1_URL", "https://raw.githubusercontent.com/Cricko7/ssh-arena/main/scripts/install-client.ps1")
		shURL := envOr("CLIENT_INSTALL_SH_URL", "https://raw.githubusercontent.com/Cricko7/ssh-arena/main/scripts/install-client.sh")
		exeURL := envOr("CLIENT_BINARY_EXE_URL", "https://raw.githubusercontent.com/Cricko7/ssh-arena/main/bin/game-client.exe")
		powershellBinary := fmt.Sprintf(`$dir=Join-Path $HOME '.ssh-arena\bin'; New-Item -ItemType Directory -Force -Path $dir | Out-Null; $exe=Join-Path $dir 'game-client.exe'; irm '%s' -OutFile $exe; & $exe --ssh '%s' --grpc '%s' --user '%s' --bootstrap-token '%s'`, exeURL, sshPublicAddr, grpcPublicAddr, session.User(), bootstrap.BootstrapToken)
		powershellScript := fmt.Sprintf(`$tmp=Join-Path $env:TEMP 'ssh-arena-install.ps1'; irm '%s' -OutFile $tmp; & $tmp -ClientArgs @('--ssh','%s','--grpc','%s','--user','%s','--bootstrap-token','%s')`, ps1URL, sshPublicAddr, grpcPublicAddr, session.User(), bootstrap.BootstrapToken)
		shellCommand := fmt.Sprintf(`tmp="$(mktemp)"; curl -fsSL '%s' -o "$tmp"; sh "$tmp" --ssh '%s' --grpc '%s' --user '%s' --bootstrap-token '%s'`, shURL, sshPublicAddr, grpcPublicAddr, session.User(), bootstrap.BootstrapToken)
		installMessage := map[string]any{
			"type":                "ssh.client.install",
			"message":             "Download the client and launch it in one command. The one-time bootstrap token means the client opens immediately without asking for the SSH password again.",
			"player_id":           resp.PlayerID,
			"ssh_endpoint":        sshPublicAddr,
			"grpc_endpoint":       grpcPublicAddr,
			"bootstrap_token":     bootstrap.BootstrapToken,
			"powershell_binary":   powershellBinary,
			"powershell_script":   powershellScript,
			"shell":               shellCommand,
			"launch":              "game-client",
			"launch_args_example": fmt.Sprintf("--ssh %s --grpc %s --user %s --bootstrap-token %s", sshPublicAddr, grpcPublicAddr, session.User(), bootstrap.BootstrapToken),
		}
		installJSON := marshalJSON(installMessage)

		_, _ = fmt.Fprintf(session, "welcome, %s\r\n", session.User())
		_, _ = fmt.Fprintf(session, "ssh_endpoint=%s\r\n", sshPublicAddr)
		_, _ = fmt.Fprintf(session, "grpc_endpoint=%s\r\n", grpcPublicAddr)
		_, _ = fmt.Fprintln(session, resp.BootstrapJSON)
		_, _ = fmt.Fprintln(session, installJSON)
		_, _ = fmt.Fprintln(session, `{"type":"ssh.bootstrap.complete","message":"Use one of the install-and-run commands above to download the client and immediately open the game. Next launches can use the saved profile with no extra flags."}`)
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

func publicListenAddr(addr string, fallbackHost string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return fallbackHost + ":2222"
	}
	if strings.HasPrefix(trimmed, ":") {
		return fallbackHost + trimmed
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return trimmed
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = fallbackHost
	}
	return net.JoinHostPort(host, port)
}
