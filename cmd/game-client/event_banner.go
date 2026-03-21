package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

type marketEventEnvelope struct {
	Type    string             `json:"type"`
	Payload marketEventPayload `json:"payload"`
}

type marketEventPayload struct {
	Kind            string    `json:"kind"`
	Name            string    `json:"name"`
	Message         string    `json:"message"`
	Global          bool      `json:"global"`
	Symbol          string    `json:"symbol"`
	AffectedSymbols []string  `json:"affected_symbols"`
	MultiplierPct   int       `json:"multiplier_pct"`
	DurationSeconds int       `json:"duration_seconds"`
	OccurredAt      time.Time `json:"occurred_at"`
}

type marketEventMsg struct {
	event marketEventPayload
}

type eventBanner struct {
	Text      string
	StartedAt time.Time
	ExpiresAt time.Time
}

func decodeMarketEvent(raw string) (marketEventPayload, bool) {
	var env marketEventEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return marketEventPayload{}, false
	}
	if env.Type != "market.event" {
		return marketEventPayload{}, false
	}
	if strings.TrimSpace(env.Payload.Message) == "" && strings.TrimSpace(env.Payload.Name) == "" {
		return marketEventPayload{}, false
	}
	return env.Payload, true
}

func activateMarketEvent(state *uiState, event marketEventPayload) {
	text := strings.TrimSpace(event.Message)
	if text == "" {
		text = strings.TrimSpace(event.Name)
	}
	if text == "" {
		return
	}
	now := time.Now()
	state.banner = &eventBanner{
		Text:      text,
		StartedAt: now,
		ExpiresAt: now.Add(10 * time.Second),
	}
	state.status = "market event: " + text
}

func bannerOffset(state *uiState, now time.Time) int {
	if !bannerActive(state, now) {
		return 0
	}
	return 1
}

func bannerActive(state *uiState, now time.Time) bool {
	if state.banner == nil {
		return false
	}
	if !state.banner.ExpiresAt.After(now) {
		state.banner = nil
		return false
	}
	return true
}

func renderEventBanner(screen tcell.Screen, colors palette, width int, state *uiState, now time.Time) {
	if !bannerActive(state, now) {
		return
	}
	fillRect(screen, 0, 0, max(0, width-1), 0, colors.selectedTile)
	drawText(screen, 1, 0, colors.selectedText, truncate(marqueeText(state.banner.Text, width, now.Sub(state.banner.StartedAt)), max(1, width-2)))
}

func marqueeText(text string, width int, elapsed time.Duration) string {
	base := strings.TrimSpace(text)
	if base == "" {
		base = "market event"
	}
	content := "  MARKET EVENT  " + base + "   "
	runes := []rune(content)
	if len(runes) == 0 {
		return content
	}
	shift := int(elapsed / (120 * time.Millisecond))
	prefix := shift % len(runes)
	builder := strings.Builder{}
	for builder.Len() < max(width*3, len(content)*4) {
		builder.WriteString(content)
	}
	repeated := []rune(builder.String())
	end := prefix + max(1, width-2)
	if end > len(repeated) {
		end = len(repeated)
	}
	return string(repeated[prefix:end])
}
