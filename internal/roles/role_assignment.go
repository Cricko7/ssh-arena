package roles

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"
)

type Role string

const (
	RoleBuyer  Role = "Buyer"
	RoleHolder Role = "Holder"
	RoleWhale  Role = "Whale"
)

type Stats struct {
	Buyers  int `json:"buyers"`
	Holders int `json:"holders"`
	Whales  int `json:"whales"`
}

type Profile struct {
	Cash           int64            `json:"cash"`
	BaseHoldings   map[string]int64 `json:"base_holdings"`
	VariancePct    int64            `json:"variance_pct"`
	WhaleInfluence int64            `json:"whale_influence_bps,omitempty"`
}

type Config struct {
	Buyer  Profile `json:"Buyer"`
	Holder Profile `json:"Holder"`
	Whale  Profile `json:"Whale"`
}

type Assignment struct {
	Role     Role             `json:"role"`
	Cash     int64            `json:"cash"`
	Holdings map[string]int64 `json:"holdings"`
}

type Allocator struct {
	config Config
	mu     sync.Mutex
	rng    *rand.Rand
}

func NewAllocator(config Config) *Allocator {
	return NewAllocatorWithSeed(config, time.Now().UnixNano())
}

func NewAllocatorWithSeed(config Config, seed int64) *Allocator {
	return &Allocator{
		config: config,
		rng:    rand.New(rand.NewSource(seed)),
	}
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read role config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, fmt.Errorf("decode role config: %w", err)
	}
	return config, nil
}

func (a *Allocator) Assign(_ string, stats Stats, symbols []string) Assignment {
	a.mu.Lock()
	defer a.mu.Unlock()

	role := a.pickRole(stats)
	profile := a.profileFor(role)
	cash := a.randomizedBudget(profile.Cash, profile.VariancePct)
	holdings := a.randomizedHoldings(profile, symbols)

	return Assignment{Role: role, Cash: cash, Holdings: holdings}
}

func (a *Allocator) pickRole(stats Stats) Role {
	total := stats.Buyers + stats.Holders + stats.Whales
	whaleRatio := 0.0
	if total > 0 {
		whaleRatio = float64(stats.Whales) / float64(total)
	}
	if whaleRatio < 0.10 && a.rng.Float64() < 0.10 {
		return RoleWhale
	}

	diff := stats.Buyers - stats.Holders
	switch {
	case diff >= 2:
		return RoleHolder
	case diff <= -2:
		return RoleBuyer
	case a.rng.Intn(2) == 0:
		return RoleBuyer
	default:
		return RoleHolder
	}
}

func (a *Allocator) profileFor(role Role) Profile {
	switch role {
	case RoleBuyer:
		return a.config.Buyer
	case RoleHolder:
		return a.config.Holder
	default:
		return a.config.Whale
	}
}

func (a *Allocator) randomizedBudget(base, variancePct int64) int64 {
	if base <= 0 {
		return 0
	}
	spread := float64(base) * (float64(variancePct) / 100.0)
	value := float64(base) + ((a.rng.Float64()*2 - 1) * spread)
	if value < 0 {
		value = 0
	}
	return int64(math.Round(value))
}

func (a *Allocator) randomizedHoldings(profile Profile, symbols []string) map[string]int64 {
	result := make(map[string]int64, len(symbols))
	if len(symbols) == 0 {
		return result
	}

	sorted := append([]string(nil), symbols...)
	sort.Strings(sorted)

	baseTotal := int64(0)
	for _, symbol := range sorted {
		baseTotal += holdingBase(profile, symbol)
	}
	if baseTotal <= 0 {
		return result
	}

	targetTotal := a.randomizedBudget(baseTotal, maxInt64(4, profile.VariancePct/2))
	if targetTotal <= 0 {
		targetTotal = baseTotal
	}

	weights := make(map[string]float64, len(sorted))
	weightSum := 0.0
	for _, symbol := range sorted {
		base := float64(maxInt64(1, holdingBase(profile, symbol)))
		jitter := 1.0 + ((a.rng.Float64()*2 - 1) * maxFloat(0.08, float64(profile.VariancePct)/200.0))
		weight := base * jitter
		weights[symbol] = weight
		weightSum += weight
	}
	if weightSum == 0 {
		weightSum = 1
	}

	remaining := targetTotal
	for i, symbol := range sorted {
		if i == len(sorted)-1 {
			result[symbol] = maxInt64(0, remaining)
			break
		}
		share := int64(math.Round((weights[symbol] / weightSum) * float64(targetTotal)))
		if share < 0 {
			share = 0
		}
		result[symbol] = share
		remaining -= share
	}

	return result
}

func holdingBase(profile Profile, symbol string) int64 {
	if value := profile.BaseHoldings[symbol]; value > 0 {
		return value
	}
	return profile.BaseHoldings["*"]
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
