package roles

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
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
}

func NewAllocator(config Config) *Allocator {
	return &Allocator{config: config}
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

func (a *Allocator) Assign(identity string, stats Stats, symbols []string) Assignment {
	role := a.pickRole(identity, stats)
	profile := a.profileFor(role)
	cash := applyVariance(profile.Cash, profile.VariancePct, identity+":cash")
	holdings := make(map[string]int64, len(symbols))
	for _, symbol := range symbols {
		base := profile.BaseHoldings[symbol]
		if base == 0 {
			base = profile.BaseHoldings["*"]
		}
		holdings[symbol] = applyVariance(base, profile.VariancePct, identity+":"+symbol)
	}

	return Assignment{Role: role, Cash: cash, Holdings: holdings}
}

func (a *Allocator) pickRole(identity string, stats Stats) Role {
	total := stats.Buyers + stats.Holders + stats.Whales
	roll := stableRoll(identity)

	whaleQuotaOpen := (stats.Whales+1)*10 <= total+1
	if whaleQuotaOpen && roll < 10 {
		return RoleWhale
	}
	if stats.Buyers <= stats.Holders {
		return RoleBuyer
	}
	return RoleHolder
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

func stableRoll(identity string) int {
	return int(stableHash(identity) % 100)
}

func applyVariance(base, variancePct int64, seed string) int64 {
	if base == 0 || variancePct == 0 {
		return base
	}
	spread := (base * variancePct) / 100
	offset := int64(stableHash(seed)%uint64((spread*2)+1)) - spread
	value := base + offset
	if value < 0 {
		return 0
	}
	return value
}

func stableHash(value string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return hash.Sum64()
}
