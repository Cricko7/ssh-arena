package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/grpcjson"
)

type chatEntry struct {
	Type              string    `json:"type"`
	Channel           string    `json:"channel"`
	PlayerID          string    `json:"player_id"`
	Username          string    `json:"username"`
	Role              string    `json:"role"`
	Body              string    `json:"body"`
	RecipientID       string    `json:"recipient_id,omitempty"`
	RecipientUsername string    `json:"recipient_username,omitempty"`
	SentAt            time.Time `json:"sent_at"`
	Topic             string    `json:"-"`
}

type chatEntryMsg struct {
	entry chatEntry
}

type chatNameRect struct {
	PlayerID string
	Username string
	X1       int
	Y1       int
	X2       int
	Y2       int
}

type streamPortfolioMsg struct {
	snapshot portfolioSnapshot
}

type insiderPreviewPayload struct {
	Type         string    `json:"type"`
	IntelID      string    `json:"intel_id"`
	Name         string    `json:"name"`
	Message      string    `json:"message"`
	Symbol       string    `json:"symbol"`
	Global       bool      `json:"global"`
	ScheduledFor time.Time `json:"scheduled_for"`
	PreviewedAt  time.Time `json:"previewed_at"`
	LeadTimeSecs int       `json:"lead_time_seconds"`
}

func startMarketStream(ctx context.Context, cfg config, ch chan<- any) {
	grpcjson.Register()
	codec := encoding.GetCodec("json")
	if codec == nil {
		ch <- errMsg{err: fmt.Errorf("json gRPC codec is not registered")}
		return
	}
	marketLogger.Info("market stream dialing", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "symbols", len(cfg.Symbols))
	started := time.Now()
	conn, err := grpc.DialContext(ctx, cfg.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec)),
		grpc.WithBlock(),
	)
	if err != nil {
		marketLogger.Error("market stream dial failed", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "duration", time.Since(started), "error", err)
		ch <- errMsg{err: fmt.Errorf("grpc dial market stream: %w", err)}
		return
	}
	defer conn.Close()
	marketLogger.Info("market stream connected", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "duration", time.Since(started))

	api := gamev1.NewGameServiceClient(conn)
	stream, err := api.GetMarketStream(ctx)
	if err != nil {
		marketLogger.Error("market stream subscribe failed", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "error", err)
		ch <- errMsg{err: fmt.Errorf("subscribe market stream: %w", err)}
		return
	}
	if err := stream.Send(&gamev1.MarketStreamRequest{
		PlayerID:         cfg.PlayerID,
		Symbols:          cfg.Symbols,
		IncludePortfolio: true,
		IncludeChat:      true,
		IncludeTrades:    true,
		IncludeOrderbook: true,
	}); err != nil {
		marketLogger.Error("market stream subscription send failed", "player_id", cfg.PlayerID, "error", err)
		ch <- errMsg{err: fmt.Errorf("send market stream subscription: %w", err)}
		return
	}
	marketLogger.Info("market stream subscribed", "player_id", cfg.PlayerID, "symbols", len(cfg.Symbols))

	for {
		env, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				marketLogger.Info("market stream closed", "player_id", cfg.PlayerID, "reason", ctx.Err())
				return
			}
			marketLogger.Error("market stream receive failed", "player_id", cfg.PlayerID, "error", err)
			ch <- errMsg{err: fmt.Errorf("recv market stream: %w", err)}
			return
		}
		switch env.Topic {
		case "portfolio":
			var snapshot portfolioSnapshot
			if err := json.Unmarshal([]byte(env.JSON), &snapshot); err == nil && snapshot.Type == "portfolio.snapshot" {
				marketLogger.Debug("portfolio stream update", "player_id", cfg.PlayerID, "cash", snapshot.AvailableCash)
				ch <- streamPortfolioMsg{snapshot: snapshot}
			}
		case "market":
			if event, ok := decodeMarketEvent(env.JSON); ok {
				marketLogger.Debug("market event received", "player_id", cfg.PlayerID, "kind", event.Kind, "symbol", event.Symbol)
				ch <- marketEventMsg{event: event}
			}
		case "chat", "private":
			entry, ok := decodeChatEntry(env.JSON, env.Topic)
			if ok {
				marketLogger.Debug("chat entry received", "player_id", cfg.PlayerID, "topic", env.Topic, "type", entry.Type, "from", entry.PlayerID)
				ch <- chatEntryMsg{entry: entry}
			}
		}
	}
}
func decodeChatEntry(raw string, topic string) (chatEntry, bool) {
	var entry chatEntry
	if err := json.Unmarshal([]byte(raw), &entry); err == nil {
		if strings.TrimSpace(entry.Body) != "" {
			entry.Topic = topic
			return entry, true
		}
	}
	if topic != "private" {
		return chatEntry{}, false
	}
	var preview insiderPreviewPayload
	if err := json.Unmarshal([]byte(raw), &preview); err != nil {
		return chatEntry{}, false
	}
	if preview.Type != "intel.insider.preview" || strings.TrimSpace(preview.Message) == "" {
		return chatEntry{}, false
	}
	body := preview.Message
	if !preview.ScheduledFor.IsZero() {
		body = fmt.Sprintf("%s | event at %s", body, preview.ScheduledFor.Local().Format("15:04:05"))
	}
	return chatEntry{
		Type:        preview.Type,
		Channel:     "direct",
		PlayerID:    "system",
		Username:    "insider-feed",
		Role:        "Intel",
		Body:        body,
		SentAt:      choosePreviewTime(preview),
		Topic:       topic,
		RecipientID: "",
	}, true
}

func choosePreviewTime(preview insiderPreviewPayload) time.Time {
	if !preview.PreviewedAt.IsZero() {
		return preview.PreviewedAt
	}
	return time.Now().UTC()
}

func appendChatEntry(state *uiState, entry chatEntry) {
	state.chatLines = append(state.chatLines, entry)
	if len(state.chatLines) > 500 {
		state.chatLines = state.chatLines[len(state.chatLines)-500:]
	}
	if state.chatScroll > 0 {
		state.chatScroll++
	}
}

func handleChatKeyEvent(ev *tcell.EventKey, state *uiState, cfg config, events chan<- any) bool {
	switch ev.Key() {
	case tcell.KeyEscape:
		state.chatFocus = false
		state.status = "chat input closed"
		return true
	case tcell.KeyEnter:
		submitChatAsync(cfg, state, events)
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		runes := []rune(state.chatInput)
		if len(runes) > 0 {
			state.chatInput = string(runes[:len(runes)-1])
		}
		return true
	case tcell.KeyTab:
		state.chatFocus = false
		state.status = "chat input closed"
		return true
	case tcell.KeyPgUp:
		scrollChat(state, 5)
		return true
	case tcell.KeyPgDn:
		scrollChat(state, -5)
		return true
	case tcell.KeyUp:
		scrollChat(state, 1)
		return true
	case tcell.KeyDown:
		scrollChat(state, -1)
		return true
	case tcell.KeyHome:
		state.chatScroll = 999999
		return true
	case tcell.KeyEnd:
		state.chatScroll = 0
		return true
	case tcell.KeyRune:
		r := ev.Rune()
		if r >= 32 {
			state.chatInput += string(r)
		}
		return true
	}
	return true
}

func scrollChat(state *uiState, delta int) {
	state.chatScroll += delta
	if state.chatScroll < 0 {
		state.chatScroll = 0
	}
}

func submitChatAsync(cfg config, state *uiState, events chan<- any) {
	payload, label, err := buildChatPayload(state.chatInput)
	if err != nil {
		marketLogger.Warn("build chat payload failed", "player_id", cfg.PlayerID, "error", err)
		events <- errMsg{err: err}
		return
	}
	_, direct := payload["to"]
	marketLogger.Info("submitting chat message", "player_id", cfg.PlayerID, "direct", direct)
	state.chatInput = ""
	state.status = label
	go executeActionAsync(cfg, "chat.send", payload, events)
}

func buildChatPayload(input string) (map[string]any, string, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return nil, "", fmt.Errorf("message is empty")
	}
	parts := strings.Fields(text)
	if len(parts) >= 3 && (parts[0] == "/w" || parts[0] == "/dm" || parts[0] == "/msg") {
		body := strings.TrimSpace(strings.Join(parts[2:], " "))
		if body == "" {
			return nil, "", fmt.Errorf("direct message body is empty")
		}
		return map[string]any{"to": parts[1], "body": body}, fmt.Sprintf("sending DM to %s...", parts[1]), nil
	}
	return map[string]any{"body": text}, "sending chat message...", nil
}

func renderChatPanel(screen tcell.Screen, colors palette, rect tickerRect, state *uiState) {
	state.chatRect = rect
	state.chatNameRects = state.chatNameRects[:0]
	drawRoundedBox(screen, rect.X1, rect.Y1, rect.X2, rect.Y2, colors.panel, colors.border)
	headerStyle := colors.accentOn(colors.panelBG)
	if state.chatFocus {
		headerStyle = colors.headerOn(colors.panelBG)
	}
	drawText(screen, rect.X1+2, rect.Y1, headerStyle, "chat")
	drawText(screen, rect.X1+8, rect.Y1, colors.mutedOn(colors.panelBG), "click name -> copy id | /dm <id> text")

	innerX1 := rect.X1 + 2
	innerX2 := rect.X2 - 2
	inputY := rect.Y2 - 1
	availableLines := max(1, inputY-rect.Y1-2)
	maxStart := max(0, len(state.chatLines)-availableLines)
	if state.chatScroll > maxStart {
		state.chatScroll = maxStart
	}
	start := max(0, len(state.chatLines)-availableLines-state.chatScroll)
	y := rect.Y1 + 2
	for _, entry := range state.chatLines[start:] {
		if y >= inputY {
			break
		}
		tag, name, nameID, style := chatDisplayParts(entry, state.cfg.PlayerID, state.cfg.Username, colors)
		x := innerX1
		if tag != "" {
			drawText(screen, x, y, colors.mutedOn(colors.panelBG), tag+" ")
			x += runeLen(tag) + 1
		}
		if name != "" {
			drawText(screen, x, y, style, name)
			state.chatNameRects = append(state.chatNameRects, chatNameRect{
				PlayerID: nameID,
				Username: name,
				X1:       x,
				Y1:       y,
				X2:       x + runeLen(name) - 1,
				Y2:       y,
			})
			x += runeLen(name)
		}
		bodyPrefix := ": "
		if name == "" {
			bodyPrefix = ""
		}
		body := truncate(bodyPrefix+entry.Body, max(0, innerX2-x+1))
		drawText(screen, x, y, colors.neutralOn(colors.panelBG), body)
		y++
	}

	prompt := "> " + state.chatInput
	inputStyle := colors.mutedOn(colors.panelBG)
	if state.chatFocus {
		inputStyle = colors.headerOn(colors.panelBG)
	}
	drawText(screen, innerX1, inputY, inputStyle, truncate(prompt, max(1, innerX2-innerX1+1)))
}

func chatDisplayParts(entry chatEntry, selfID string, selfUsername string, colors palette) (string, string, string, tcell.Style) {
	switch {
	case entry.Type == "intel.insider.preview":
		return "[intel]", "insider-feed", "", colors.accentOn(colors.panelBG)
	case entry.Type == "chat.direct" && entry.PlayerID == selfID:
		name := entry.RecipientUsername
		if name == "" {
			name = entry.RecipientID
		}
		return "[dm]", name, entry.RecipientID, colors.accentOn(colors.panelBG)
	case entry.Type == "chat.direct":
		name := entry.Username
		if name == "" {
			name = entry.PlayerID
		}
		return "[dm]", name, entry.PlayerID, colors.accentOn(colors.panelBG)
	case entry.Type == "system.event":
		return "", "system", "", colors.mutedOn(colors.panelBG)
	default:
		name := entry.Username
		if name == "" {
			name = entry.PlayerID
		}
		style := colors.neutralOn(colors.panelBG)
		if entry.PlayerID == selfID || name == selfUsername {
			style = colors.positiveOn(colors.panelBG)
		}
		return "", name, entry.PlayerID, style
	}
}

func copyPlayerIDIntoChat(state *uiState, rect chatNameRect) {
	marketLogger.Info("prepared dm target from chat", "player_id", rect.PlayerID, "username", rect.Username)
	state.chatInput = "/dm " + rect.PlayerID + " "
	state.chatFocus = true
	if rect.PlayerID == "" {
		state.status = fmt.Sprintf("prepared chat focus for %s", rect.Username)
		return
	}
	if err := copyToClipboardBestEffort(rect.PlayerID); err != nil {
		state.status = fmt.Sprintf("prepared DM to %s (%s)", rect.Username, rect.PlayerID)
		return
	}
	state.status = fmt.Sprintf("copied %s id and prepared DM", rect.Username)
}

func copyToClipboardBestEffort(value string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(value))
	_, err := fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", encoded)
	return err
}
