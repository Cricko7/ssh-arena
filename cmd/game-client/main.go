package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"

	gamev1 "github.com/aeza/ssh-arena/gen/game/v1"
	"github.com/aeza/ssh-arena/internal/charting"
	"github.com/aeza/ssh-arena/internal/client"
	"github.com/aeza/ssh-arena/internal/clientlog"
	"github.com/aeza/ssh-arena/internal/grpcjson"
)

type config struct {
	SSHAddr      string
	Username     string
	Password     string
	GRPCAddr     string
	PlayerID     string
	ProfilePath  string
	ResetProfile bool
	Symbols      []string
	Initial      string
	HistoryLimit int
	Depth        int
}

type chartTickMsg struct {
	tick charting.PriceChartTick
}

type errMsg struct{ err error }

type statusMsg struct{ text string }

type actionResponseMsg struct {
	actionID string
	json     string
}

type intelCatalogItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Price       int64  `json:"price"`
}

type tradeRulesSnapshot struct {
	ExchangeFeeBps          int64 `json:"exchange_fee_bps"`
	MarketOrderSurchargeBps int64 `json:"market_order_surcharge_bps"`
	RapidFlipTaxBps         int64 `json:"rapid_flip_tax_bps"`
	RapidFlipWindowSeconds  int64 `json:"rapid_flip_window_seconds"`
}

type portfolioSnapshot struct {
	Type           string             `json:"type"`
	PlayerID       string             `json:"player_id"`
	Username       string             `json:"username"`
	Role           string             `json:"role"`
	Cash           int64              `json:"cash"`
	ReservedCash   int64              `json:"reserved_cash"`
	AvailableCash  int64              `json:"available_cash"`
	Portfolio      map[string]int64   `json:"portfolio"`
	ReservedStocks map[string]int64   `json:"reserved_stocks"`
	AvailableStock map[string]int64   `json:"available_stock"`
	FreshStock     map[string]int64   `json:"fresh_stock"`
	Rules          tradeRulesSnapshot `json:"rules"`
}

type orderTicket struct {
	Active    bool
	Side      string
	OrderType string
	Price     string
	Quantity  string
	Field     int
}

type tickerRect struct {
	Symbol string
	X1     int
	Y1     int
	X2     int
	Y2     int
}

type orderbookRect struct {
	Side     string
	Price    float64
	Quantity int64
	X1       int
	Y1       int
	X2       int
	Y2       int
}

type uiState struct {
	cfg            config
	selected       int
	status         string
	playerLabel    string
	endpointLabel  string
	ticks          map[string]charting.PriceChartTick
	tickerRects    []tickerRect
	orderbookRects []orderbookRect
	portfolio      portfolioSnapshot
	hasPortfolio   bool
	rules          tradeRulesSnapshot
	ticket         orderTicket
	chartZoom      int
	chartOffset    int
	chartPlotWidth int
	timeframe      int
	chatRect       tickerRect
	chatNameRects  []chatNameRect
	chatLines      []chatEntry
	chatInput      string
	chatScroll     int
	chatFocus      bool
	insiderOffer   *intelCatalogItem
	insiderRect    tickerRect
	banner         *eventBanner
}

type palette struct {
	baseBG     tcell.Color
	panelBG    tcell.Color
	panelAltBG tcell.Color
	selectedBG tcell.Color
	statusBG   tcell.Color
	mainFG     tcell.Color
	mutedFG    tcell.Color
	accentFG   tcell.Color
	selectedFG tcell.Color
	positiveFG tcell.Color
	negativeFG tcell.Color
	borderFG   tcell.Color
	gridFG     tcell.Color
	chartFG    tcell.Color

	background   tcell.Style
	header       tcell.Style
	headerAccent tcell.Style
	muted        tcell.Style
	panel        tcell.Style
	panelAlt     tcell.Style
	selectedTile tcell.Style
	selectedText tcell.Style
	positive     tcell.Style
	negative     tcell.Style
	neutral      tcell.Style
	border       tcell.Style
	chartLine    tcell.Style
	grid         tcell.Style
	status       tcell.Style
}

type plotPoint struct {
	X int
	Y int
}

type timeframePreset struct {
	Label    string
	Duration time.Duration
}

var timeframePresets = []timeframePreset{
	{Label: "1m", Duration: time.Minute},
	{Label: "5m", Duration: 5 * time.Minute},
	{Label: "15m", Duration: 15 * time.Minute},
	{Label: "30m", Duration: 30 * time.Minute},
	{Label: "1h", Duration: time.Hour},
	{Label: "all", Duration: 0},
}

type actionClientCache struct {
	mu   sync.Mutex
	addr string
	conn *grpc.ClientConn
	api  gamev1.GameServiceClient
}

var (
	sharedActionClient = actionClientCache{}
	clientLogger       = clientlog.L("game-client")
	actionLogger       = clientlog.L("game-client.action")
	chartLogger        = clientlog.L("game-client.chart")
	marketLogger       = clientlog.L("game-client.market")
)

func newPalette() palette {
	p := palette{
		baseBG:     tcell.NewRGBColor(7, 11, 21),
		panelBG:    tcell.NewRGBColor(12, 19, 34),
		panelAltBG: tcell.NewRGBColor(17, 26, 45),
		selectedBG: tcell.NewRGBColor(24, 58, 92),
		statusBG:   tcell.NewRGBColor(9, 14, 25),
		mainFG:     tcell.NewRGBColor(222, 231, 244),
		mutedFG:    tcell.NewRGBColor(130, 146, 170),
		accentFG:   tcell.NewRGBColor(115, 214, 255),
		selectedFG: tcell.NewRGBColor(240, 249, 255),
		positiveFG: tcell.NewRGBColor(94, 223, 158),
		negativeFG: tcell.NewRGBColor(255, 123, 123),
		borderFG:   tcell.NewRGBColor(72, 89, 115),
		gridFG:     tcell.NewRGBColor(36, 50, 70),
		chartFG:    tcell.NewRGBColor(115, 214, 255),
	}
	p.background = tcell.StyleDefault.Background(p.baseBG).Foreground(p.mainFG)
	p.header = tcell.StyleDefault.Background(p.baseBG).Foreground(p.mainFG).Bold(true)
	p.headerAccent = tcell.StyleDefault.Background(p.baseBG).Foreground(p.accentFG).Bold(true)
	p.muted = tcell.StyleDefault.Background(p.baseBG).Foreground(p.mutedFG)
	p.panel = tcell.StyleDefault.Background(p.panelBG).Foreground(p.mainFG)
	p.panelAlt = tcell.StyleDefault.Background(p.panelAltBG).Foreground(p.mainFG)
	p.selectedTile = tcell.StyleDefault.Background(p.selectedBG).Foreground(p.selectedFG)
	p.selectedText = tcell.StyleDefault.Background(p.selectedBG).Foreground(p.selectedFG).Bold(true)
	p.positive = tcell.StyleDefault.Background(p.panelBG).Foreground(p.positiveFG).Bold(true)
	p.negative = tcell.StyleDefault.Background(p.panelBG).Foreground(p.negativeFG).Bold(true)
	p.neutral = tcell.StyleDefault.Background(p.panelBG).Foreground(p.mainFG)
	p.border = tcell.StyleDefault.Background(p.panelBG).Foreground(p.borderFG)
	p.chartLine = tcell.StyleDefault.Background(p.panelBG).Foreground(p.chartFG).Bold(true)
	p.grid = tcell.StyleDefault.Background(p.panelBG).Foreground(p.gridFG)
	p.status = tcell.StyleDefault.Background(p.statusBG).Foreground(tcell.NewRGBColor(175, 189, 210))
	return p
}

func (p palette) headerOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.mainFG).Bold(true)
}

func (p palette) accentOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.accentFG).Bold(true)
}

func (p palette) mutedOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.mutedFG)
}

func (p palette) neutralOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.mainFG)
}

func (p palette) positiveOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.positiveFG).Bold(true)
}

func (p palette) negativeOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.negativeFG).Bold(true)
}

func (p palette) chartOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.chartFG).Bold(true)
}

func (p palette) gridOn(bg tcell.Color) tcell.Style {
	return tcell.StyleDefault.Background(bg).Foreground(p.gridFG)
}

func main() {
	cfg, playerLabel, err := resolveConfig()
	if err != nil {
		clientLogger.Error("resolve client config failed", "error", err)
		log.Fatal(err)
	}
	clientLogger.Info("client starting", "player_id", cfg.PlayerID, "grpc_addr", cfg.GRPCAddr, "ssh_addr", cfg.SSHAddr, "symbols", len(cfg.Symbols), "history_limit", cfg.HistoryLimit, "depth", cfg.Depth, "log_path", clientlog.Path())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan any, 256)
	go startChartStream(ctx, cfg, events)
	go startMarketStream(ctx, cfg, events)

	screen, err := tcell.NewScreen()
	if err != nil {
		clientLogger.Error("create screen failed", "error", err)
		log.Fatal(err)
	}
	if err := screen.Init(); err != nil {
		clientLogger.Error("init screen failed", "error", err)
		log.Fatal(err)
	}
	defer screen.Fini()
	screen.EnableMouse()
	screen.SetStyle(newPalette().background)
	screen.Clear()

	state := uiState{
		cfg:           cfg,
		selected:      indexOfSymbol(cfg.Symbols, cfg.Initial),
		status:        "connecting to chart stream...",
		playerLabel:   playerLabel,
		endpointLabel: cfg.GRPCAddr,
		ticks:         make(map[string]charting.PriceChartTick),
	}
	if state.selected < 0 {
		state.selected = 0
	}

	eventCh := make(chan tcell.Event, 64)
	go func() {
		for {
			ev := screen.PollEvent()
			if ev == nil {
				close(eventCh)
				return
			}
			eventCh <- ev
		}
	}()

	colors := newPalette()
	requestPortfolioAsync(cfg, events)
	requestIntelCatalogAsync(cfg, events)
	portfolioTicker := time.NewTicker(4 * time.Second)
	defer portfolioTicker.Stop()
	uiTicker := time.NewTicker(120 * time.Millisecond)
	defer uiTicker.Stop()
	render(screen, colors, &state)

	running := true
	for running {
		select {
		case raw, ok := <-events:
			if !ok {
				state.status = "chart stream closed"
				render(screen, colors, &state)
				continue
			}
			switch msg := raw.(type) {
			case actionResponseMsg:
				applyActionResponse(&state, msg)
			case chartTickMsg:
				state.ticks[msg.tick.Ticker] = msg.tick
				clampViewport(&state)
				if msg.tick.Ticker == currentSymbol(state.cfg.Symbols, state.selected) {
					state.status = fmt.Sprintf("live tick %s at %s", msg.tick.Ticker, msg.tick.Timestamp.Format("15:04:05"))
				}
			case statusMsg:
				state.status = msg.text
			case marketEventMsg:
				activateMarketEvent(&state, msg.event)
			case chatEntryMsg:
				appendChatEntry(&state, msg.entry)
				if msg.entry.Type == "intel.insider.preview" {
					state.status = "insider preview received"
				}
			case streamPortfolioMsg:
				state.portfolio = msg.snapshot
				state.hasPortfolio = true
			case errMsg:
				state.status = "error: " + msg.err.Error()
			}
			render(screen, colors, &state)
		case ev, ok := <-eventCh:
			if !ok {
				running = false
				continue
			}
			switch e := ev.(type) {
			case *tcell.EventResize:
				screen.Sync()
				render(screen, colors, &state)
			case *tcell.EventKey:
				running = handleKeyEvent(e, &state, cfg, events)
				render(screen, colors, &state)
			case *tcell.EventMouse:
				handleMouseEvent(e, &state, cfg, events)
				render(screen, colors, &state)
			}
		case <-portfolioTicker.C:
			requestPortfolioAsync(cfg, events)
		case <-uiTicker.C:
			if bannerActive(&state, time.Now()) {
				render(screen, colors, &state)
			}
		case <-ctx.Done():
			running = false
		}
	}

	profilePath, err := client.ResolveProfilePath(cfg.ProfilePath)
	if err == nil {
		if saveErr := client.SaveProfile(profilePath, client.Profile{
			SSHAddr:    cfg.SSHAddr,
			GRPCAddr:   cfg.GRPCAddr,
			Username:   cfg.Username,
			PlayerID:   cfg.PlayerID,
			Symbols:    cfg.Symbols,
			LastTicker: currentSymbol(cfg.Symbols, state.selected),
		}); saveErr != nil {
			clientLogger.Warn("save profile on shutdown failed", "path", profilePath, "error", saveErr)
		}
	}
	clientLogger.Info("client shutdown complete", "player_id", cfg.PlayerID)
}

func resolveConfig() (config, string, error) {
	cfg := config{}
	symbolsFlag := flag.String("symbols", "", "comma-separated tickers to subscribe")
	flag.StringVar(&cfg.SSHAddr, "ssh", "localhost:2222", "SSH bootstrap address")
	flag.StringVar(&cfg.Username, "user", "", "SSH username for bootstrap")
	flag.StringVar(&cfg.Password, "password", "", "SSH password for bootstrap")
	flag.StringVar(&cfg.GRPCAddr, "grpc", "", "gRPC address override")
	flag.StringVar(&cfg.PlayerID, "player-id", "", "existing player ID; skip SSH bootstrap when set")
	flag.StringVar(&cfg.ProfilePath, "profile", "", "path to local client profile")
	flag.BoolVar(&cfg.ResetProfile, "reset-profile", false, "remove saved client profile before startup")
	flag.StringVar(&cfg.Initial, "ticker", "", "initial selected ticker")
	flag.IntVar(&cfg.HistoryLimit, "history", 2400, "chart history points per ticker")
	flag.IntVar(&cfg.Depth, "depth", 10, "orderbook depth for chart ticks")
	flag.Parse()

	profilePath, err := client.ResolveProfilePath(cfg.ProfilePath)
	if err != nil {
		return config{}, "", err
	}
	clientLogger.Info("resolved client profile path", "path", profilePath)
	if cfg.ResetProfile {
		if removeErr := os.Remove(profilePath); removeErr == nil {
			clientLogger.Info("client profile removed", "path", profilePath)
		} else if !errors.Is(removeErr, os.ErrNotExist) {
			clientLogger.Warn("client profile removal failed", "path", profilePath, "error", removeErr)
		}
	}

	profile, profileErr := client.LoadProfile(profilePath)
	hasProfile := profileErr == nil
	if profileErr != nil {
		if errors.Is(profileErr, os.ErrNotExist) {
			clientLogger.Info("client profile not found", "path", profilePath)
		} else {
			return config{}, "", fmt.Errorf("load client profile: %w", profileErr)
		}
	}

	if cfg.PlayerID == "" && hasProfile {
		cfg.PlayerID = profile.PlayerID
	}
	if cfg.GRPCAddr == "" && hasProfile {
		cfg.GRPCAddr = profile.GRPCAddr
	}
	if cfg.Username == "" && hasProfile {
		cfg.Username = profile.Username
	}
	if cfg.SSHAddr == "localhost:2222" && hasProfile && profile.SSHAddr != "" {
		cfg.SSHAddr = profile.SSHAddr
	}
	if cfg.Initial == "" && hasProfile {
		cfg.Initial = profile.LastTicker
	}

	playerLabel := "guest"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if cfg.PlayerID == "" {
		if cfg.Username == "" || cfg.Password == "" {
			return config{}, "", fmt.Errorf("no saved profile found; run once with --user and --password to bootstrap over SSH")
		}
		clientLogger.Info("starting bootstrap flow", "ssh_addr", cfg.SSHAddr, "username", cfg.Username)
		bootstrap, bootstrapErr := client.BootstrapViaSSH(ctx, cfg.SSHAddr, cfg.Username, cfg.Password)
		if bootstrapErr != nil {
			clientLogger.Error("bootstrap flow failed", "ssh_addr", cfg.SSHAddr, "username", cfg.Username, "error", bootstrapErr)
			return config{}, "", bootstrapErr
		}
		cfg.PlayerID = bootstrap.PlayerID
		if cfg.GRPCAddr == "" {
			cfg.GRPCAddr = bootstrap.GRPCEndpoint
		}
		if cfg.Username == "" {
			cfg.Username = bootstrap.Username
		}
		playerLabel = fmt.Sprintf("%s (%s) %s", bootstrap.Username, bootstrap.Role, bootstrap.PlayerID)
		portfolioSymbols := make([]string, 0, len(bootstrap.Portfolio))
		for symbol := range bootstrap.Portfolio {
			portfolioSymbols = append(portfolioSymbols, symbol)
		}
		serverSymbols, fetchErr := fetchMarketSymbols(config{GRPCAddr: cfg.GRPCAddr, PlayerID: cfg.PlayerID})
		if fetchErr != nil {
			clientLogger.Warn("fetch market catalog after bootstrap failed", "error", fetchErr)
		} else {
			clientLogger.Info("market catalog fetched after bootstrap", "symbols", len(serverSymbols))
		}
		cfg.Symbols = normalizeSymbols(*symbolsFlag, mergeFallback(mergeFallback(profile.Symbols, portfolioSymbols), serverSymbols))
	} else {
		if cfg.GRPCAddr == "" {
			cfg.GRPCAddr = "localhost:9090"
		}
		serverSymbols, fetchErr := fetchMarketSymbols(config{GRPCAddr: cfg.GRPCAddr, PlayerID: cfg.PlayerID})
		if fetchErr != nil {
			clientLogger.Warn("fetch market catalog from saved profile failed", "error", fetchErr)
		} else {
			clientLogger.Info("market catalog fetched from saved profile", "symbols", len(serverSymbols))
		}
		cfg.Symbols = normalizeSymbols(*symbolsFlag, mergeFallback(profile.Symbols, serverSymbols))
		if cfg.Username != "" {
			playerLabel = fmt.Sprintf("%s %s", cfg.Username, cfg.PlayerID)
		} else {
			playerLabel = cfg.PlayerID
		}
	}

	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = "localhost:9090"
	}
	if len(cfg.Symbols) == 0 {
		return config{}, "", fmt.Errorf("server returned no market symbols")
	}
	if cfg.Initial == "" || indexOfSymbol(cfg.Symbols, cfg.Initial) < 0 {
		cfg.Initial = cfg.Symbols[0]
	}

	if saveErr := client.SaveProfile(profilePath, client.Profile{
		SSHAddr:    cfg.SSHAddr,
		GRPCAddr:   cfg.GRPCAddr,
		Username:   cfg.Username,
		PlayerID:   cfg.PlayerID,
		Symbols:    cfg.Symbols,
		LastTicker: cfg.Initial,
	}); saveErr != nil {
		clientLogger.Warn("save client profile failed", "path", profilePath, "error", saveErr)
	} else {
		clientLogger.Info("client config resolved", "player_id", cfg.PlayerID, "grpc_addr", cfg.GRPCAddr, "symbols", len(cfg.Symbols), "initial", cfg.Initial)
	}

	return cfg, playerLabel, nil
}
func handleKeyEvent(ev *tcell.EventKey, state *uiState, cfg config, events chan<- any) bool {
	if state.ticket.Active {
		return handleTicketKeyEvent(ev, state, cfg, events)
	}
	if state.chatFocus {
		return handleChatKeyEvent(ev, state, cfg, events)
	}
	switch ev.Key() {
	case tcell.KeyCtrlC, tcell.KeyEscape:
		return false
	case tcell.KeyLeft:
		moveSelection(state, -1, "selected")
	case tcell.KeyRight:
		moveSelection(state, 1, "selected")
	case tcell.KeyUp:
		zoomChart(state, 1)
	case tcell.KeyDown:
		zoomChart(state, -1)
	case tcell.KeyHome:
		resetViewport(state)
	case tcell.KeyPgUp:
		panChart(state, panStep(state))
	case tcell.KeyPgDn:
		panChart(state, -panStep(state))
	case tcell.KeyRune:
		r := ev.Rune()
		switch {
		case r == 'q' || r == 'Q':
			return false
		case r == 'h' || r == 'H':
			moveSelection(state, -1, "selected")
		case r == 'l' || r == 'L':
			moveSelection(state, 1, "selected")
		case r >= '1' && r <= '8':
			idx := int(r - '1')
			if idx >= 0 && idx < len(state.cfg.Symbols) {
				state.selected = idx
				state.chartOffset = 0
				clampViewport(state)
				state.status = fmt.Sprintf("selected %s", currentSymbol(state.cfg.Symbols, state.selected))
			}
		case r == '+' || r == '=':
			zoomChart(state, 1)
		case r == '-' || r == '_':
			zoomChart(state, -1)
		case r == 'a' || r == 'A':
			panChart(state, panStep(state))
		case r == 'd' || r == 'D':
			panChart(state, -panStep(state))
		case r == '0':
			resetViewport(state)
		case r == '[' || r == '{' || r == 'w' || r == 'W':
			shiftTimeframe(state, -1)
		case r == ']' || r == '}' || r == 't' || r == 'T':
			shiftTimeframe(state, 1)
		case r == 'b' || r == 'B':
			openTicket(state, "buy")
		case r == 's' || r == 'S':
			openTicket(state, "sell")
		case r == 'r' || r == 'R':
			requestPortfolioAsync(cfg, events)
		case r == 'i' || r == 'I':
			buyInsiderAsync(cfg, state, events)
		}
	}
	return true
}

func handleTicketKeyEvent(ev *tcell.EventKey, state *uiState, cfg config, events chan<- any) bool {
	switch ev.Key() {
	case tcell.KeyEscape:
		state.ticket = orderTicket{}
		state.status = "ticket closed"
		return true
	case tcell.KeyTab:
		state.ticket.Field = (state.ticket.Field + 1) % 3
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		switch state.ticket.Field {
		case 1:
			if len(state.ticket.Price) > 0 {
				state.ticket.Price = state.ticket.Price[:len(state.ticket.Price)-1]
			}
		case 2:
			if len(state.ticket.Quantity) > 0 {
				state.ticket.Quantity = state.ticket.Quantity[:len(state.ticket.Quantity)-1]
			}
		}
		return true
	case tcell.KeyEnter:
		submitOrderAsync(cfg, state, events)
		return true
	case tcell.KeyRune:
		r := ev.Rune()
		switch {
		case r == 'q' || r == 'Q':
			state.ticket = orderTicket{}
			state.status = "ticket closed"
			return true
		case state.ticket.Field == 0 && (r == 'm' || r == 'M'):
			state.ticket.OrderType = "market"
			return true
		case state.ticket.Field == 0 && (r == 'l' || r == 'L'):
			state.ticket.OrderType = "limit"
			return true
		case state.ticket.Field == 1 && ((r >= '0' && r <= '9') || r == '.'):
			state.ticket.Price += string(r)
			return true
		case state.ticket.Field == 2 && r >= '0' && r <= '9':
			state.ticket.Quantity += string(r)
			return true
		}
	}
	return true
}

func openTicket(state *uiState, side string) {
	current := currentSymbol(state.cfg.Symbols, state.selected)
	price := "0.00"
	if tick, ok := state.ticks[current]; ok && tick.CurrentPrice > 0 {
		price = fmt.Sprintf("%.2f", tick.CurrentPrice)
	}
	state.ticket = orderTicket{
		Active:    true,
		Side:      side,
		OrderType: "limit",
		Price:     price,
		Quantity:  "1",
		Field:     1,
	}
	state.status = fmt.Sprintf("%s ticket ready", strings.ToUpper(side))
}

func openBookTicket(state *uiState, side string, price float64, qty int64) {
	if qty <= 0 {
		qty = 1
	}
	state.ticket = orderTicket{
		Active:    true,
		Side:      side,
		OrderType: "limit",
		Price:     fmt.Sprintf("%.2f", price),
		Quantity:  fmt.Sprintf("%d", qty),
		Field:     2,
	}
	state.status = fmt.Sprintf("%s %d @ %.2f ready", strings.ToUpper(side), qty, price)
}
func submitOrderAsync(cfg config, state *uiState, events chan<- any) {
	priceRaw, qty, err := buildOrderPayload(state)
	if err != nil {
		events <- errMsg{err: err}
		return
	}
	payload := map[string]any{
		"symbol":   currentSymbol(state.cfg.Symbols, state.selected),
		"side":     state.ticket.Side,
		"type":     state.ticket.OrderType,
		"price":    priceRaw,
		"quantity": qty,
	}
	state.status = fmt.Sprintf("submitting %s order...", state.ticket.Side)
	state.ticket = orderTicket{}
	go executeActionAsync(cfg, "place_order", payload, events)
}

func buildOrderPayload(state *uiState) (int64, int64, error) {
	qty, err := parseWholeUnits(state.ticket.Quantity)
	if err != nil || qty <= 0 {
		return 0, 0, fmt.Errorf("quantity must be a positive integer")
	}
	if state.ticket.OrderType == "market" {
		return 0, qty, nil
	}
	price, err := parseMoneyToCents(state.ticket.Price)
	if err != nil || price <= 0 {
		return 0, 0, fmt.Errorf("price must be a positive number")
	}
	return price, qty, nil
}

func requestPortfolioAsync(cfg config, events chan<- any) {
	go executeActionAsync(cfg, "portfolio.get", map[string]any{}, events)
}

func requestIntelCatalogAsync(cfg config, events chan<- any) {
	go executeActionAsync(cfg, "intel.catalog", map[string]any{}, events)
}

func buyInsiderAsync(cfg config, state *uiState, events chan<- any) {
	if state.insiderOffer == nil {
		state.status = "loading insider catalog..."
		requestIntelCatalogAsync(cfg, events)
		return
	}
	state.status = fmt.Sprintf("buying insider preview for %.2f...", moneyValue(state.insiderOffer.Price))
	go executeActionAsync(cfg, "intel.buy", map[string]any{"intel_id": state.insiderOffer.ID}, events)
}

func executeActionAsync(cfg config, actionID string, payload any, events chan<- any) {
	response, err := executeActionJSON(cfg, actionID, payload)
	if err != nil {
		actionLogger.Error("action failed", "action_id", actionID, "player_id", cfg.PlayerID, "error", err)
		events <- errMsg{err: err}
		return
	}
	actionLogger.Info("action completed", "action_id", actionID, "player_id", cfg.PlayerID, "response_bytes", len(response))
	events <- actionResponseMsg{actionID: actionID, json: response}
}

func executeActionJSON(cfg config, actionID string, payload any) (string, error) {
	grpcjson.Register()
	codec := encoding.GetCodec("json")
	if codec == nil {
		return "", fmt.Errorf("json gRPC codec is not registered")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		started := time.Now()
		actionLogger.Info("action request started", "action_id", actionID, "player_id", cfg.PlayerID, "attempt", attempt+1, "grpc_addr", cfg.GRPCAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		api, err := sharedActionClient.client(ctx, cfg.GRPCAddr, codec)
		if err == nil {
			resp, callErr := api.ExecuteAction(ctx, &gamev1.ActionRequest{
				RequestID:   fmt.Sprintf("ui-%d", time.Now().UnixNano()),
				PlayerID:    cfg.PlayerID,
				ActionID:    actionID,
				PayloadJSON: string(raw),
			})
			cancel()
			if callErr == nil {
				if resp.Status != "ok" {
					err = fmt.Errorf("%s", extractErrorMessage(resp.ResponseJSON))
					actionLogger.Warn("action returned application error", "action_id", actionID, "player_id", cfg.PlayerID, "attempt", attempt+1, "duration", time.Since(started), "error", err)
					return "", err
				}
				actionLogger.Info("action request succeeded", "action_id", actionID, "player_id", cfg.PlayerID, "attempt", attempt+1, "duration", time.Since(started))
				return resp.ResponseJSON, nil
			}
			lastErr = callErr
		} else {
			cancel()
			lastErr = err
		}
		actionLogger.Warn("action request failed", "action_id", actionID, "player_id", cfg.PlayerID, "attempt", attempt+1, "duration", time.Since(started), "error", lastErr)
		sharedActionClient.reset(cfg.GRPCAddr)
		if !shouldRetryAction(lastErr) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 350 * time.Millisecond)
	}
	return "", lastErr
}

func shouldRetryAction(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Unavailable, codes.Canceled:
		return true
	default:
		return false
	}
}

func (c *actionClientCache) client(ctx context.Context, addr string, codec encoding.Codec) (gamev1.GameServiceClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && c.addr == addr && c.api != nil {
		actionLogger.Debug("reusing action grpc connection", "grpc_addr", addr)
		return c.api, nil
	}
	if c.conn != nil {
		actionLogger.Info("closing stale action grpc connection", "grpc_addr", c.addr)
		_ = c.conn.Close()
		c.conn = nil
		c.api = nil
		c.addr = ""
	}
	started := time.Now()
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec)),
		grpc.WithBlock(),
	)
	if err != nil {
		actionLogger.Error("grpc dial failed", "grpc_addr", addr, "duration", time.Since(started), "error", err)
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	c.conn = conn
	c.api = gamev1.NewGameServiceClient(conn)
	c.addr = addr
	actionLogger.Info("grpc connection established", "grpc_addr", addr, "duration", time.Since(started))
	return c.api, nil
}

func (c *actionClientCache) reset(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if addr != "" && c.addr != "" && c.addr != addr {
		return
	}
	if c.conn != nil {
		actionLogger.Info("resetting action grpc connection", "grpc_addr", c.addr)
		_ = c.conn.Close()
	}
	c.conn = nil
	c.api = nil
	c.addr = ""
}
func applyActionResponse(state *uiState, msg actionResponseMsg) {
	if msg.actionID == "market.rules" {
		var payload struct {
			Type  string             `json:"type"`
			Rules tradeRulesSnapshot `json:"rules"`
		}
		if err := json.Unmarshal([]byte(msg.json), &payload); err == nil && payload.Type == "market.rules" {
			state.rules = payload.Rules
			state.status = "market rules loaded"
			return
		}
	}
	if msg.actionID == "portfolio.get" {
		var snapshot portfolioSnapshot
		if err := json.Unmarshal([]byte(msg.json), &snapshot); err == nil && snapshot.Type == "portfolio.snapshot" {
			state.portfolio = snapshot
			state.hasPortfolio = true
			state.rules = snapshot.Rules
			state.status = fmt.Sprintf("portfolio refreshed: cash %.2f", moneyValue(snapshot.AvailableCash))
			return
		}
	}
	if msg.actionID == "intel.catalog" {
		var payload struct {
			Type  string             `json:"type"`
			Items []intelCatalogItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(msg.json), &payload); err == nil {
			state.insiderOffer = nil
			for idx := range payload.Items {
				if payload.Items[idx].Kind == "insider" {
					item := payload.Items[idx]
					state.insiderOffer = &item
					state.status = fmt.Sprintf("insider preview ready: %.2f", moneyValue(item.Price))
					return
				}
			}
			state.status = "intel catalog loaded"
			return
		}
	}
	if msg.actionID == "place_order" {
		var result struct {
			Type      string            `json:"type"`
			Portfolio portfolioSnapshot `json:"portfolio"`
		}
		if err := json.Unmarshal([]byte(msg.json), &result); err == nil {
			if result.Portfolio.Type == "portfolio.snapshot" {
				state.portfolio = result.Portfolio
				state.hasPortfolio = true
				state.rules = result.Portfolio.Rules
			}
			state.status = "order accepted"
			return
		}
	}
	if msg.actionID == "intel.buy" {
		var result struct {
			Type      string            `json:"type"`
			Cost      int64             `json:"cost"`
			Portfolio portfolioSnapshot `json:"portfolio"`
			Payload   struct {
				Type         string    `json:"type"`
				Message      string    `json:"message"`
				ScheduledFor time.Time `json:"scheduled_for"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(msg.json), &result); err == nil {
			if result.Portfolio.Type == "portfolio.snapshot" {
				state.portfolio = result.Portfolio
				state.hasPortfolio = true
				state.rules = result.Portfolio.Rules
			}
			switch result.Payload.Type {
			case "intel.purchase.armed":
				if !result.Payload.ScheduledFor.IsZero() {
					state.status = fmt.Sprintf("insider armed: preview before %s", result.Payload.ScheduledFor.Local().Format("15:04:05"))
				} else {
					state.status = fmt.Sprintf("insider armed for next event (%.2f)", moneyValue(result.Cost))
				}
			case "intel.insider.preview":
				state.status = "insider preview delivered"
			default:
				state.status = fmt.Sprintf("intel purchased for %.2f", moneyValue(result.Cost))
			}
			return
		}
	}
	state.status = "action completed"
}
func parseWholeUnits(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("empty")
	}
	var out int64
	_, err := fmt.Sscanf(value, "%d", &out)
	return out, err
}

func parseMoneyToCents(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	if value == "" {
		return 0, fmt.Errorf("empty")
	}
	var amount float64
	_, err := fmt.Sscanf(value, "%f", &amount)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(amount * 100)), nil
}

func extractErrorMessage(raw string) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.Message != "" {
		return payload.Message
	}
	return raw
}
func moveSelection(state *uiState, delta int, prefix string) {
	if len(state.cfg.Symbols) == 0 {
		return
	}
	state.selected = (state.selected + delta + len(state.cfg.Symbols)) % len(state.cfg.Symbols)
	state.chartOffset = 0
	clampViewport(state)
	state.status = fmt.Sprintf("%s %s", prefix, currentSymbol(state.cfg.Symbols, state.selected))
}

func zoomChart(state *uiState, delta int) {
	base := timeframeHistory(state.ticks[currentSymbol(state.cfg.Symbols, state.selected)].History, timeframePresets[state.timeframe].Duration)
	if len(base) == 0 {
		state.chartZoom = 0
		state.chartOffset = 0
		return
	}
	plotWidth := effectiveChartPlotWidth(state)
	oldVisible := computeVisiblePoints(len(base), plotWidth, state.chartZoom)
	oldOffset := clampInt(state.chartOffset, 0, max(0, len(base)-oldVisible))
	oldEnd := len(base) - oldOffset
	oldStart := max(0, oldEnd-oldVisible)
	oldCenter := oldStart + oldVisible/2

	state.chartZoom += delta
	if state.chartZoom < -6 {
		state.chartZoom = -6
	}
	if state.chartZoom > 12 {
		state.chartZoom = 12
	}

	newVisible := computeVisiblePoints(len(base), plotWidth, state.chartZoom)
	newStart := clampInt(oldCenter-newVisible/2, 0, max(0, len(base)-newVisible))
	state.chartOffset = len(base) - (newStart + newVisible)
	clampViewport(state)
}

func panChart(state *uiState, delta int) {
	if delta == 0 {
		return
	}
	state.chartOffset += delta
	clampViewport(state)
}

func shiftTimeframe(state *uiState, delta int) {
	state.timeframe = (state.timeframe + delta + len(timeframePresets)) % len(timeframePresets)
	state.chartOffset = 0
	clampViewport(state)
	state.status = fmt.Sprintf("timeframe %s", timeframePresets[state.timeframe].Label)
}

func resetViewport(state *uiState) {
	state.chartZoom = 0
	state.chartOffset = 0
	state.status = "chart viewport reset"
}

func clampViewport(state *uiState) {
	tick := state.ticks[currentSymbol(state.cfg.Symbols, state.selected)]
	historyLen := len(tick.History)
	if historyLen == 0 {
		state.chartOffset = 0
		return
	}
	base := timeframeHistory(tick.History, timeframePresets[state.timeframe].Duration)
	visible := computeVisiblePoints(len(base), effectiveChartPlotWidth(state), state.chartZoom)
	maxOffset := max(0, len(base)-visible)
	if state.chartOffset < 0 {
		state.chartOffset = 0
	}
	if state.chartOffset > maxOffset {
		state.chartOffset = maxOffset
	}
}

func panStep(state *uiState) int {
	tick := state.ticks[currentSymbol(state.cfg.Symbols, state.selected)]
	base := timeframeHistory(tick.History, timeframePresets[state.timeframe].Duration)
	visible := computeVisiblePoints(len(base), effectiveChartPlotWidth(state), state.chartZoom)
	return max(1, visible/8)
}

func handleMouseEvent(ev *tcell.EventMouse, state *uiState, cfg config, events chan<- any) {
	x, y := ev.Position()
	buttons := ev.Buttons()
	insideChat := x >= state.chatRect.X1 && x <= state.chatRect.X2 && y >= state.chatRect.Y1 && y <= state.chatRect.Y2
	switch {
	case buttons&tcell.WheelUp != 0:
		if insideChat {
			scrollChat(state, 3)
		} else {
			zoomChart(state, 1)
		}
		return
	case buttons&tcell.WheelDown != 0:
		if insideChat {
			scrollChat(state, -3)
		} else {
			zoomChart(state, -1)
		}
		return
	case buttons&tcell.Button1 == 0:
		return
	}

	if insideChat {
		for _, rect := range state.chatNameRects {
			if x >= rect.X1 && x <= rect.X2 && y >= rect.Y1 && y <= rect.Y2 {
				copyPlayerIDIntoChat(state, rect)
				return
			}
		}
		state.chatFocus = true
		state.status = "chat input focused"
		return
	}
	if state.insiderRect.X2 > state.insiderRect.X1 && x >= state.insiderRect.X1 && x <= state.insiderRect.X2 && y >= state.insiderRect.Y1 && y <= state.insiderRect.Y2 {
		buyInsiderAsync(cfg, state, events)
		return
	}
	for _, rect := range state.orderbookRects {
		if x >= rect.X1 && x <= rect.X2 && y >= rect.Y1 && y <= rect.Y2 {
			if rect.Side == "ask" {
				openBookTicket(state, "buy", rect.Price, rect.Quantity)
			} else {
				openBookTicket(state, "sell", rect.Price, rect.Quantity)
			}
			return
		}
	}
	for idx, rect := range state.tickerRects {
		if x >= rect.X1 && x <= rect.X2 && y >= rect.Y1 && y <= rect.Y2 {
			state.selected = idx
			state.chartOffset = 0
			clampViewport(state)
			state.status = fmt.Sprintf("selected %s via mouse", rect.Symbol)
			return
		}
	}
}

func render(screen tcell.Screen, colors palette, state *uiState) {
	w, h := screen.Size()
	state.tickerRects = state.tickerRects[:0]
	state.orderbookRects = state.orderbookRects[:0]
	state.insiderRect = tickerRect{}
	screen.SetStyle(colors.background)
	screen.Clear()

	now := time.Now()
	offset := bannerOffset(state, now)
	if offset > 0 {
		renderEventBanner(screen, colors, w, state, now)
	}

	drawText(screen, 2, 0+offset, colors.headerAccent, "SSH ARENA")
	drawText(screen, 13, 0+offset, colors.header, "market terminal")
	drawText(screen, 2, 1+offset, colors.muted, truncate(state.playerLabel, max(1, w-4)))
	endpointX := max(2, w-7-runeLen(state.endpointLabel))
	drawText(screen, endpointX, 1+offset, colors.muted, "grpc "+state.endpointLabel)
	drawText(screen, 2, 2+offset, colors.muted, truncate("click ticker/level | enter chat | b buy | s sell | r refresh | wheel zoom | a/d pan | [/] timeframe | q quit", max(1, w-4)))

	tileY := 4 + offset
	tileWidth := 22
	tileHeight := 5
	gap := 1
	cols := max(1, w/(tileWidth+gap))
	for idx, symbol := range state.cfg.Symbols {
		col := idx % cols
		row := idx / cols
		x := 1 + col*(tileWidth+gap)
		y := tileY + row*(tileHeight+1)
		if x+tileWidth >= w || y+tileHeight >= h-3 {
			continue
		}
		rect := tickerRect{Symbol: symbol, X1: x, Y1: y, X2: x + tileWidth - 1, Y2: y + tileHeight - 1}
		state.tickerRects = append(state.tickerRects, rect)
		renderTickerTile(screen, colors, rect, idx == state.selected, symbol, state.ticks[symbol])
	}

	tilesBottom := tileY
	if len(state.tickerRects) > 0 {
		tilesBottom = state.tickerRects[len(state.tickerRects)-1].Y2
	}
	mainY := tilesBottom + 2
	chatHeight := min(11, max(8, h/4))
	chatRect := tickerRect{X1: 1, Y1: max(mainY+8, h-chatHeight-2), X2: w - 2, Y2: h - 2}
	mainHeight := max(10, chatRect.Y1-mainY-2)
	chartWidth := int(float64(w) * 0.68)
	chartWidth = min(chartWidth, w-2)
	if chartWidth < 46 {
		chartWidth = w - 2
	}
	chartRect := tickerRect{X1: 1, Y1: mainY, X2: min(w-2, chartWidth), Y2: min(chatRect.Y1-1, mainY+mainHeight)}
	sidebarRect := tickerRect{X1: chartRect.X2 + 1, Y1: mainY, X2: w - 2, Y2: chartRect.Y2}

	current := currentSymbol(state.cfg.Symbols, state.selected)

	renderChartPanel(screen, colors, chartRect, current, state.ticks[current], state)
	if sidebarRect.X2-sidebarRect.X1 >= 22 {
		renderSidebar(screen, colors, sidebarRect, current, state.ticks[current], state)
	}

	renderChatPanel(screen, colors, chatRect, state)
	statusY := h - 1
	fillRect(screen, 0, statusY, w-1, statusY, colors.status)
	drawText(screen, 1, statusY, colors.status, truncate("status: "+state.status, max(1, w-2)))
	screen.Show()
}

func renderTickerTile(screen tcell.Screen, colors palette, rect tickerRect, selected bool, symbol string, tick charting.PriceChartTick) {
	boxStyle := colors.panelAlt
	textStyle := colors.neutralOn(colors.panelAltBG)
	borderStyle := colors.border
	tileBG := colors.panelAltBG
	if selected {
		boxStyle = colors.selectedTile
		textStyle = colors.neutralOn(colors.selectedBG)
		borderStyle = colors.selectedText
		tileBG = colors.selectedBG
	}
	drawRoundedBox(screen, rect.X1, rect.Y1, rect.X2, rect.Y2, boxStyle, borderStyle)
	drawText(screen, rect.X1+2, rect.Y1+1, styleWithBackground(colors.headerAccent, tileBG), symbol)
	if len(tick.History) == 0 {
		drawText(screen, rect.X1+2, rect.Y1+2, styleWithBackground(colors.muted, tileBG), "waiting...")
		return
	}
	changeStyle := styleForChange(colors, tick.Change5mPct, tileBG)
	drawText(screen, rect.X1+2, rect.Y1+2, textStyle, fmt.Sprintf("%.2f", tick.CurrentPrice))
	drawText(screen, rect.X1+2, rect.Y1+3, changeStyle, fmt.Sprintf("5m %+.2f%%", tick.Change5mPct))
}

func renderChartPanel(screen tcell.Screen, colors palette, rect tickerRect, symbol string, tick charting.PriceChartTick, state *uiState) {
	drawRoundedBox(screen, rect.X1, rect.Y1, rect.X2, rect.Y2, colors.panel, colors.border)
	drawText(screen, rect.X1+2, rect.Y1, colors.accentOn(colors.panelBG), "chart")
	drawText(screen, rect.X1+9, rect.Y1, colors.headerOn(colors.panelBG), symbol)

	innerX1 := rect.X1 + 2
	innerY1 := rect.Y1 + 2
	innerX2 := rect.X2 - 2
	innerY2 := rect.Y2 - 2
	if innerX2 <= innerX1 || innerY2 <= innerY1 {
		return
	}

	if len(tick.History) == 0 {
		drawText(screen, innerX1, innerY1, colors.mutedOn(colors.panelBG), "waiting for chart data from server...")
		return
	}

	drawText(screen, innerX1, innerY1, colors.neutralOn(colors.panelBG), fmt.Sprintf("price %.2f", tick.CurrentPrice))
	drawText(screen, innerX1+18, innerY1, styleForChange(colors, tick.Change1mPct, colors.panelBG), fmt.Sprintf("1m %+.2f%%", tick.Change1mPct))
	drawText(screen, innerX1+32, innerY1, styleForChange(colors, tick.Change5mPct, colors.panelBG), fmt.Sprintf("5m %+.2f%%", tick.Change5mPct))
	drawText(screen, innerX1+47, innerY1, styleForChange(colors, tick.Change15mPct, colors.panelBG), fmt.Sprintf("15m %+.2f%%", tick.Change15mPct))

	plotX1 := innerX1
	plotY1 := innerY1 + 2
	plotX2 := innerX2
	plotY2 := innerY2
	if plotY2-plotY1 < 6 {
		plotY1 = innerY1 + 1
	}
	state.chartPlotWidth = max(1, plotX2-plotX1+1)
	visible, start, end := chartViewport(tick.History, plotX2-plotX1, state.chartZoom, state.chartOffset, timeframePresets[state.timeframe].Duration)
	viewportLabel := fmt.Sprintf("tf %s  view %d:%d  zoom x%.2f", timeframePresets[state.timeframe].Label, start+1, end, chartZoomFactor(state.chartZoom))
	drawText(screen, max(plotX1, plotX2-runeLen(viewportLabel)), innerY1, colors.mutedOn(colors.panelBG), viewportLabel)
	renderChartPlot(screen, colors, plotX1, plotY1, plotX2, plotY2, visible)
}

func renderSidebar(screen tcell.Screen, colors palette, rect tickerRect, symbol string, tick charting.PriceChartTick, state *uiState) {
	drawRoundedBox(screen, rect.X1, rect.Y1, rect.X2, rect.Y2, colors.panelAlt, colors.border)
	drawText(screen, rect.X1+2, rect.Y1, colors.accentOn(colors.panelAltBG), "market")
	drawText(screen, rect.X1+10, rect.Y1, colors.headerOn(colors.panelAltBG), symbol)
	if len(tick.History) == 0 {
		drawText(screen, rect.X1+2, rect.Y1+2, colors.mutedOn(colors.panelAltBG), "awaiting tick...")
		return
	}

	y := rect.Y1 + 2
	lines := []struct {
		label string
		value string
		style tcell.Style
	}{
		{"current", fmt.Sprintf("%.2f", tick.CurrentPrice), colors.neutral},
		{"1m", fmt.Sprintf("%+.2f%%", tick.Change1mPct), styleForChange(colors, tick.Change1mPct, colors.panelAltBG)},
		{"5m", fmt.Sprintf("%+.2f%%", tick.Change5mPct), styleForChange(colors, tick.Change5mPct, colors.panelAltBG)},
		{"15m", fmt.Sprintf("%+.2f%%", tick.Change15mPct), styleForChange(colors, tick.Change15mPct, colors.panelAltBG)},
		{"timeframe", timeframePresets[state.timeframe].Label, colors.neutral},
		{"zoom", fmt.Sprintf("x%.2f", chartZoomFactor(state.chartZoom)), colors.neutral},
		{"fee", fmt.Sprintf("%.2fbp", float64(state.rules.ExchangeFeeBps)/100.0), colors.neutral},
		{"mkt+", fmt.Sprintf("%.2fbp", float64(state.rules.MarketOrderSurchargeBps)/100.0), colors.neutral},
	}
	for _, line := range lines {
		if y >= rect.Y2-2 {
			break
		}
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), line.label)
		drawText(screen, rect.X1+14, y, styleWithBackground(line.style, colors.panelAltBG), line.value)
		y++
	}

	y++
	drawText(screen, rect.X1+2, y, colors.headerOn(colors.panelAltBG), "wallet")
	y++
	if state.hasPortfolio {
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "cash")
		drawText(screen, rect.X1+14, y, colors.neutralOn(colors.panelAltBG), fmt.Sprintf("%.2f", moneyValue(state.portfolio.AvailableCash)))
		y++
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "reserved")
		drawText(screen, rect.X1+14, y, colors.neutralOn(colors.panelAltBG), fmt.Sprintf("%.2f", moneyValue(state.portfolio.ReservedCash)))
		y++
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "shares")
		drawText(screen, rect.X1+14, y, colors.neutralOn(colors.panelAltBG), fmt.Sprintf("%d", state.portfolio.AvailableStock[symbol]))
		y++
	} else {
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "loading portfolio...")
		y++
	}

	y++
	drawText(screen, rect.X1+2, y, colors.headerOn(colors.panelAltBG), "intel")
	y++
	if state.insiderOffer != nil {
		label := fmt.Sprintf("i buy preview %.2f", moneyValue(state.insiderOffer.Price))
		state.insiderRect = tickerRect{X1: rect.X1 + 2, Y1: y, X2: min(rect.X2-2, rect.X1+2+runeLen(label)-1), Y2: y}
		drawText(screen, rect.X1+2, y, colors.accentOn(colors.panelAltBG), truncate(label, max(1, rect.X2-rect.X1-4)))
		y++
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "next random event 30s early")
		y++
	} else {
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "loading insider catalog...")
		y++
	}

	y++
	drawText(screen, rect.X1+2, y, colors.headerOn(colors.panelAltBG), "ticket")
	y++
	if state.ticket.Active {
		drawTicketField(screen, rect.X1+2, y, colors, colors.panelAltBG, "side", strings.ToUpper(state.ticket.Side), state.ticket.Field == 0)
		y++
		drawTicketField(screen, rect.X1+2, y, colors, colors.panelAltBG, "type", strings.ToUpper(state.ticket.OrderType), state.ticket.Field == 0)
		y++
		if state.ticket.OrderType == "limit" {
			drawTicketField(screen, rect.X1+2, y, colors, colors.panelAltBG, "price", state.ticket.Price, state.ticket.Field == 1)
			y++
		}
		drawTicketField(screen, rect.X1+2, y, colors, colors.panelAltBG, "qty", state.ticket.Quantity, state.ticket.Field == 2)
		y++
		est := estimateTicketCost(state, symbol, tick)
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), fmt.Sprintf("fee %.2f", moneyValue(est.ExchangeFee)))
		y++
		if est.MarketOrderSurcharge > 0 {
			drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), fmt.Sprintf("impact %.2f", moneyValue(est.MarketOrderSurcharge)))
			y++
		}
		if est.RapidFlipTax > 0 {
			drawText(screen, rect.X1+2, y, colors.negativeOn(colors.panelAltBG), fmt.Sprintf("flip tax %.2f", moneyValue(est.RapidFlipTax)))
			y++
		}
		label := "total debit"
		if state.ticket.Side == "sell" {
			label = "net credit"
		}
		drawText(screen, rect.X1+2, y, colors.headerOn(colors.panelAltBG), fmt.Sprintf("%s %.2f", label, moneyValue(est.Total)))
		y++
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "Tab switch | Enter send | Esc close | click book level")
		y++
	} else {
		drawText(screen, rect.X1+2, y, colors.mutedOn(colors.panelAltBG), "b buy | s sell | i insider | r refresh")
		y++
	}

	y++
	if y < rect.Y2-2 {
		drawText(screen, rect.X1+2, y, colors.headerOn(colors.panelAltBG), "order book")
		y += 2
	}
	if y < rect.Y2-2 {
		drawText(screen, rect.X1+2, y, colors.positiveOn(colors.panelAltBG), "bids")
		y++
	}
	for idx, bid := range tick.OrderBook.Bids {
		if idx >= 3 || y >= rect.Y2-3 {
			break
		}
		state.orderbookRects = append(state.orderbookRects, orderbookRect{Side: "bid", Price: bid.Price, Quantity: bid.Qty, X1: rect.X1 + 2, Y1: y, X2: rect.X2 - 2, Y2: y})
		drawText(screen, rect.X1+2, y, colors.positiveOn(colors.panelAltBG), fmt.Sprintf("%.2f", bid.Price))
		drawText(screen, rect.X1+14, y, colors.neutralOn(colors.panelAltBG), fmt.Sprintf("%d", bid.Qty))
		y++
	}
	if y < rect.Y2-2 {
		y++
		drawText(screen, rect.X1+2, y, colors.negativeOn(colors.panelAltBG), "asks")
		y++
	}
	for idx, ask := range tick.OrderBook.Asks {
		if idx >= 3 || y >= rect.Y2-1 {
			break
		}
		state.orderbookRects = append(state.orderbookRects, orderbookRect{Side: "ask", Price: ask.Price, Quantity: ask.Qty, X1: rect.X1 + 2, Y1: y, X2: rect.X2 - 2, Y2: y})
		drawText(screen, rect.X1+2, y, colors.negativeOn(colors.panelAltBG), fmt.Sprintf("%.2f", ask.Price))
		drawText(screen, rect.X1+14, y, colors.neutralOn(colors.panelAltBG), fmt.Sprintf("%d", ask.Qty))
		y++
	}
}

func drawTicketField(screen tcell.Screen, x int, y int, colors palette, bg tcell.Color, label string, value string, selected bool) {
	labelStyle := colors.mutedOn(bg)
	valueStyle := colors.neutralOn(bg)
	if selected {
		labelStyle = colors.accentOn(bg)
		valueStyle = colors.headerOn(bg)
	}
	drawText(screen, x, y, labelStyle, label)
	drawText(screen, x+10, y, valueStyle, value)
}

type ticketCostEstimate struct {
	Notional             int64
	ExchangeFee          int64
	MarketOrderSurcharge int64
	RapidFlipTax         int64
	Total                int64
}

func estimateTicketCost(state *uiState, symbol string, tick charting.PriceChartTick) ticketCostEstimate {
	qty, err := parseWholeUnits(state.ticket.Quantity)
	if err != nil || qty <= 0 {
		return ticketCostEstimate{}
	}
	price := int64(math.Round(tick.CurrentPrice * 100))
	if state.ticket.OrderType == "limit" {
		if parsed, parseErr := parseMoneyToCents(state.ticket.Price); parseErr == nil && parsed > 0 {
			price = parsed
		}
	}
	if price <= 0 {
		return ticketCostEstimate{}
	}
	notional := price * qty
	est := ticketCostEstimate{
		Notional:    notional,
		ExchangeFee: chargeBps(notional, state.rules.ExchangeFeeBps),
	}
	if state.ticket.OrderType == "market" {
		est.MarketOrderSurcharge = chargeBps(notional, state.rules.MarketOrderSurchargeBps)
	}
	if state.ticket.Side == "sell" {
		fresh := int64(0)
		if state.portfolio.FreshStock != nil {
			fresh = state.portfolio.FreshStock[symbol]
		}
		taxableQty := min64(qty, fresh)
		est.RapidFlipTax = chargeBps(price*taxableQty, state.rules.RapidFlipTaxBps)
		est.Total = notional - est.ExchangeFee - est.MarketOrderSurcharge - est.RapidFlipTax
		return est
	}
	est.Total = notional + est.ExchangeFee + est.MarketOrderSurcharge
	return est
}

func chargeBps(amount int64, bps int64) int64 {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return (amount*bps + 9999) / 10000
}

func min64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func moneyValue(raw int64) float64 {
	return math.Round((float64(raw)/100.0)*100) / 100
}
func renderChartPlot(screen tcell.Screen, colors palette, x1 int, y1 int, x2 int, y2 int, history []charting.HistoryPoint) {
	if x2 <= x1 || y2 <= y1 || len(history) == 0 {
		return
	}
	height := y2 - y1 + 1
	minPrice := history[0].Price
	maxPrice := history[0].Price
	for _, point := range history {
		if point.Price < minPrice {
			minPrice = point.Price
		}
		if point.Price > maxPrice {
			maxPrice = point.Price
		}
	}
	if math.Abs(maxPrice-minPrice) < 0.0001 {
		maxPrice = minPrice + 1
	}

	for step := 0; step < 4; step++ {
		y := y1 + step*(height-1)/3
		for x := x1; x <= x2; x++ {
			screen.SetContent(x, y, '\u2508', nil, colors.gridOn(colors.panelBG))
		}
		labelValue := maxPrice - (float64(step)/3.0)*(maxPrice-minPrice)
		drawText(screen, x1+1, y, colors.mutedOn(colors.panelBG), fmt.Sprintf("%.2f", labelValue))
	}

	points := make([]plotPoint, 0, len(history))
	drawWidth := max(1, x2-(x1+1))
	for idx, point := range history {
		x := x1 + 1
		if len(history) > 1 {
			x += int(math.Round(float64(idx) * float64(drawWidth) / float64(len(history)-1)))
		}
		if x > x2 {
			x = x2
		}
		y := mapValueToRow(point.Price, minPrice, maxPrice, y1, y2)
		points = append(points, plotPoint{X: x, Y: y})
	}
	for idx := 1; idx < len(points); idx++ {
		style := segmentStyle(colors, history[idx-1].Price, history[idx].Price, colors.panelBG)
		drawSegment(screen, points[idx-1], points[idx], style)
	}
	for idx, point := range points {
		style := colors.chartOn(colors.panelBG)
		if idx > 0 {
			style = segmentStyle(colors, history[idx-1].Price, history[idx].Price, colors.panelBG)
		}
		screen.SetContent(point.X, point.Y, '\u2022', nil, style)
	}
}

func segmentStyle(colors palette, from float64, to float64, bg tcell.Color) tcell.Style {
	switch {
	case to > from:
		return colors.positiveOn(bg)
	case to < from:
		return colors.negativeOn(bg)
	default:
		return colors.chartOn(bg)
	}
}

func drawSegment(screen tcell.Screen, from plotPoint, to plotPoint, style tcell.Style) {
	dx := to.X - from.X
	dy := to.Y - from.Y
	steps := max(abs(dx), abs(dy))
	if steps == 0 {
		screen.SetContent(from.X, from.Y, '\u2022', nil, style)
		return
	}
	for i := 0; i <= steps; i++ {
		x := from.X + dx*i/steps
		y := from.Y + dy*i/steps
		r := '\u2022'
		switch {
		case dy == 0:
			r = '\u2500'
		case dx == 0:
			r = '\u2502'
		case (dx > 0 && dy < 0) || (dx < 0 && dy > 0):
			r = '\u2571'
		default:
			r = '\u2572'
		}
		screen.SetContent(x, y, r, nil, style)
	}
}

func chartViewport(history []charting.HistoryPoint, plotWidth int, zoom int, offset int, duration time.Duration) ([]charting.HistoryPoint, int, int) {
	if len(history) == 0 {
		return nil, 0, 0
	}
	base, baseStart := timeframeHistoryWithStart(history, duration)
	if len(base) == 0 {
		return history, 0, len(history)
	}
	visible := computeVisiblePoints(len(base), plotWidth+1, zoom)
	maxOffset := max(0, len(base)-visible)
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := len(base) - offset
	start := max(0, end-visible)
	return base[start:end], baseStart + start, baseStart + end
}

func timeframeHistory(history []charting.HistoryPoint, duration time.Duration) []charting.HistoryPoint {
	base, _ := timeframeHistoryWithStart(history, duration)
	return base
}

func timeframeHistoryWithStart(history []charting.HistoryPoint, duration time.Duration) ([]charting.HistoryPoint, int) {
	if len(history) == 0 || duration <= 0 {
		return history, 0
	}
	latest := time.UnixMilli(history[len(history)-1].TS)
	cutoff := latest.Add(-duration)
	start := 0
	for idx, point := range history {
		if !time.UnixMilli(point.TS).Before(cutoff) {
			start = idx
			break
		}
	}
	return history[start:], start
}

func computeVisiblePoints(total int, plotWidth int, zoom int) int {
	if total <= 0 {
		return 0
	}
	baseVisible := clampInt(max(12, plotWidth-2), 8, total)
	scale := math.Pow(1.6, float64(zoom))
	if scale <= 0 {
		scale = 1
	}
	count := int(math.Round(float64(baseVisible) / scale))
	if count < 8 {
		count = 8
	}
	if count < 2 {
		count = 2
	}
	if count > total {
		count = total
	}
	return count
}

func effectiveChartPlotWidth(state *uiState) int {
	if state.chartPlotWidth > 0 {
		return state.chartPlotWidth
	}
	return 96
}

func clampInt(value int, low int, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func chartZoomFactor(zoom int) float64 {
	return math.Pow(1.6, float64(zoom))
}

func mapValueToRow(value float64, minValue float64, maxValue float64, top int, bottom int) int {
	rangeSize := maxValue - minValue
	if rangeSize <= 0 {
		return (top + bottom) / 2
	}
	normalized := (value - minValue) / rangeSize
	row := bottom - int(math.Round(normalized*float64(bottom-top)))
	if row < top {
		return top
	}
	if row > bottom {
		return bottom
	}
	return row
}

func drawRoundedBox(screen tcell.Screen, x1 int, y1 int, x2 int, y2 int, fill tcell.Style, border tcell.Style) {
	if x2 <= x1 || y2 <= y1 {
		return
	}
	fillRect(screen, x1, y1, x2, y2, fill)
	screen.SetContent(x1, y1, '\u256d', nil, border)
	screen.SetContent(x2, y1, '\u256e', nil, border)
	screen.SetContent(x1, y2, '\u2570', nil, border)
	screen.SetContent(x2, y2, '\u256f', nil, border)
	for x := x1 + 1; x < x2; x++ {
		screen.SetContent(x, y1, '\u2500', nil, border)
		screen.SetContent(x, y2, '\u2500', nil, border)
	}
	for y := y1 + 1; y < y2; y++ {
		screen.SetContent(x1, y, '\u2502', nil, border)
		screen.SetContent(x2, y, '\u2502', nil, border)
	}
}

func fillRect(screen tcell.Screen, x1 int, y1 int, x2 int, y2 int, style tcell.Style) {
	for y := y1; y <= y2; y++ {
		for x := x1; x <= x2; x++ {
			screen.SetContent(x, y, ' ', nil, style)
		}
	}
}

func drawText(screen tcell.Screen, x int, y int, style tcell.Style, text string) {
	for _, r := range []rune(text) {
		screen.SetContent(x, y, r, nil, style)
		x++
	}
}

func truncate(text string, width int) string {
	runes := []rune(text)
	if width <= 0 {
		return ""
	}
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func runeLen(text string) int {
	return len([]rune(text))
}

func styleForChange(colors palette, value float64, bg tcell.Color) tcell.Style {
	switch {
	case value > 0:
		return colors.positiveOn(bg)
	case value < 0:
		return colors.negativeOn(bg)
	default:
		return colors.neutralOn(bg)
	}
}

func styleWithBackground(style tcell.Style, bg tcell.Color) tcell.Style {
	fg, _, attrs := style.Decompose()
	return tcell.StyleDefault.Background(bg).Foreground(fg).Attributes(attrs)
}

func normalizeSymbols(raw string, fallback []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	if len(out) == 0 && len(fallback) > 0 {
		for _, symbol := range fallback {
			symbol = strings.ToUpper(strings.TrimSpace(symbol))
			if symbol == "" {
				continue
			}
			if _, ok := seen[symbol]; ok {
				continue
			}
			seen[symbol] = struct{}{}
			out = append(out, symbol)
		}
		sort.Strings(out)
	}
	return out
}

func mergeFallback(primary []string, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	out = append(out, primary...)
	out = append(out, secondary...)
	return out
}

func fetchMarketSymbols(cfg config) ([]string, error) {
	response, err := executeActionJSON(cfg, "market.catalog", map[string]any{})
	if err != nil {
		clientLogger.Warn("market catalog request failed", "player_id", cfg.PlayerID, "grpc_addr", cfg.GRPCAddr, "error", err)
		return nil, err
	}
	var payload struct {
		Type    string   `json:"type"`
		Symbols []string `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		return nil, fmt.Errorf("decode market catalog: %w", err)
	}
	if payload.Type != "market.catalog" {
		return nil, fmt.Errorf("unexpected market catalog payload %q", payload.Type)
	}
	clientLogger.Info("market catalog loaded", "player_id", cfg.PlayerID, "symbols", len(payload.Symbols))
	return payload.Symbols, nil
}

func startChartStream(ctx context.Context, cfg config, ch chan<- any) {
	grpcjson.Register()
	codec := encoding.GetCodec("json")
	if codec == nil {
		ch <- errMsg{err: fmt.Errorf("json gRPC codec is not registered")}
		return
	}
	chartLogger.Info("chart stream dialing", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "symbols", len(cfg.Symbols))
	started := time.Now()
	conn, err := grpc.DialContext(ctx, cfg.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec)),
		grpc.WithBlock(),
	)
	if err != nil {
		chartLogger.Error("chart stream dial failed", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "duration", time.Since(started), "error", err)
		ch <- errMsg{err: fmt.Errorf("grpc dial: %w", err)}
		return
	}
	defer conn.Close()
	chartLogger.Info("chart stream connected", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "duration", time.Since(started))

	api := gamev1.NewGameServiceClient(conn)
	stream, err := api.SubscribeToChart(ctx)
	if err != nil {
		chartLogger.Error("chart stream subscribe failed", "grpc_addr", cfg.GRPCAddr, "player_id", cfg.PlayerID, "error", err)
		ch <- errMsg{err: fmt.Errorf("subscribe chart: %w", err)}
		return
	}
	for _, symbol := range cfg.Symbols {
		if err := stream.Send(&gamev1.ChartSubscriptionRequest{
			PlayerID:       cfg.PlayerID,
			Ticker:         symbol,
			HistoryLimit:   uint32(cfg.HistoryLimit),
			OrderbookDepth: uint32(cfg.Depth),
		}); err != nil {
			chartLogger.Error("chart stream subscription send failed", "player_id", cfg.PlayerID, "ticker", symbol, "error", err)
			ch <- errMsg{err: fmt.Errorf("subscribe %s: %w", symbol, err)}
			return
		}
		chartLogger.Info("chart stream subscribed ticker", "player_id", cfg.PlayerID, "ticker", symbol, "history_limit", cfg.HistoryLimit, "depth", cfg.Depth)
	}
	ch <- statusMsg{text: fmt.Sprintf("subscribed to %d tickers", len(cfg.Symbols))}

	for {
		msg, err := stream.Recv()
		if err != nil {
			select {
			case <-ctx.Done():
				chartLogger.Info("chart stream closed", "player_id", cfg.PlayerID, "reason", ctx.Err())
				return
			default:
				chartLogger.Error("chart stream receive failed", "player_id", cfg.PlayerID, "error", err)
				ch <- errMsg{err: fmt.Errorf("chart recv: %w", err)}
				return
			}
		}
		var tick charting.PriceChartTick
		if err := json.Unmarshal([]byte(msg.JSON), &tick); err != nil {
			chartLogger.Warn("chart tick decode failed", "player_id", cfg.PlayerID, "error", err)
			ch <- errMsg{err: fmt.Errorf("decode chart tick: %w", err)}
			continue
		}
		chartLogger.Debug("chart tick received", "player_id", cfg.PlayerID, "ticker", tick.Ticker, "history_points", len(tick.History), "current_price", tick.CurrentPrice)
		ch <- chartTickMsg{tick: tick}
	}
}
func currentSymbol(symbols []string, selected int) string {
	if len(symbols) == 0 {
		return ""
	}
	if selected < 0 || selected >= len(symbols) {
		return symbols[0]
	}
	return symbols[selected]
}

func indexOfSymbol(symbols []string, target string) int {
	for idx, symbol := range symbols {
		if symbol == target {
			return idx
		}
	}
	return -1
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func sampleHistory(history []charting.HistoryPoint, target int) []charting.HistoryPoint {
	if target <= 0 || len(history) <= target {
		return history
	}
	out := make([]charting.HistoryPoint, 0, target)
	for i := 0; i < target; i++ {
		idx := i * (len(history) - 1) / max(1, target-1)
		out = append(out, history[idx])
	}
	return out
}




