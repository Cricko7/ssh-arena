package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type BootstrapInfo struct {
	GRPCEndpoint string
	PlayerID     string           `json:"player_id"`
	Username     string           `json:"username"`
	Role         string           `json:"role"`
	Cash         int64            `json:"cash"`
	Portfolio    map[string]int64 `json:"portfolio"`
}

func BootstrapViaSSH(ctx context.Context, addr string, username string, password string) (BootstrapInfo, error) {
	if strings.TrimSpace(addr) == "" {
		return BootstrapInfo{}, fmt.Errorf("ssh address is required")
	}
	if strings.TrimSpace(username) == "" {
		return BootstrapInfo{}, fmt.Errorf("ssh username is required")
	}
	if password == "" {
		return BootstrapInfo{}, fmt.Errorf("ssh password is required")
	}

	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         8 * time.Second,
	}

	dialer := &ssh.Client{}
	_ = dialer
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return BootstrapInfo{}, fmt.Errorf("ssh dial: %w", err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return BootstrapInfo{}, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return BootstrapInfo{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return BootstrapInfo{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := session.RequestPty("xterm", 24, 120, ssh.TerminalModes{}); err != nil {
		return BootstrapInfo{}, fmt.Errorf("request pty: %w", err)
	}
	if err := session.Shell(); err != nil {
		return BootstrapInfo{}, fmt.Errorf("start shell: %w", err)
	}

	readDone := make(chan struct {
		info BootstrapInfo
		err  error
	}, 1)
	go func() {
		payload, readErr := io.ReadAll(io.MultiReader(stdout, stderr))
		if readErr != nil {
			readDone <- struct {
				info BootstrapInfo
				err  error
			}{err: fmt.Errorf("read bootstrap: %w", readErr)}
			return
		}
		info, parseErr := parseBootstrapOutput(payload)
		readDone <- struct {
			info BootstrapInfo
			err  error
		}{info: info, err: parseErr}
	}()

	waitErr := make(chan error, 1)
	go func() { waitErr <- session.Wait() }()

	select {
	case <-ctx.Done():
		return BootstrapInfo{}, ctx.Err()
	case result := <-readDone:
		_ = session.Close()
		select {
		case <-waitErr:
		case <-time.After(500 * time.Millisecond):
		}
		return result.info, result.err
	}
}

func parseBootstrapOutput(raw []byte) (BootstrapInfo, error) {
	var info BootstrapInfo
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "grpc_endpoint=") {
			info.GRPCEndpoint = strings.TrimSpace(strings.TrimPrefix(line, "grpc_endpoint="))
			continue
		}
		if !json.Valid([]byte(line)) {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue
		}
		if probe.Type != "bootstrap" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			return BootstrapInfo{}, fmt.Errorf("decode bootstrap json: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return BootstrapInfo{}, fmt.Errorf("scan bootstrap output: %w", err)
	}
	if info.PlayerID == "" {
		return BootstrapInfo{}, fmt.Errorf("bootstrap output did not contain player_id")
	}
	return info, nil
}
