package roles

import "testing"

func TestAllocatorBalancesNonWhales(t *testing.T) {
	allocator := NewAllocatorWithSeed(Config{
		Buyer:  Profile{Cash: 1000, BaseHoldings: map[string]int64{"*": 1}},
		Holder: Profile{Cash: 500, BaseHoldings: map[string]int64{"*": 10}},
		Whale:  Profile{Cash: 10000, BaseHoldings: map[string]int64{"*": 50}},
	}, 1)

	stats := Stats{Buyers: 5, Holders: 7, Whales: 1}
	assignment := allocator.Assign("new-player", stats, []string{"TECH"})
	if assignment.Role != RoleBuyer && assignment.Role != RoleWhale {
		t.Fatalf("expected buyer or whale, got %s", assignment.Role)
	}
}

func TestAllocatorRandomizedHoldingsStayBalanced(t *testing.T) {
	allocator := NewAllocatorWithSeed(Config{
		Buyer: Profile{
			Cash:         1000,
			VariancePct:  10,
			BaseHoldings: map[string]int64{"TECH": 3, "ENERGY": 5, "*": 2},
		},
		Holder: Profile{Cash: 500, BaseHoldings: map[string]int64{"*": 10}},
		Whale:  Profile{Cash: 10000, BaseHoldings: map[string]int64{"*": 50}},
	}, 42)

	assignment := allocator.Assign("player", Stats{Buyers: 0, Holders: 3, Whales: 1}, []string{"TECH", "ENERGY", "FOOD"})
	if assignment.Cash < 900 || assignment.Cash > 1100 {
		t.Fatalf("cash out of expected balanced range: %d", assignment.Cash)
	}

	total := int64(0)
	for _, qty := range assignment.Holdings {
		total += qty
	}
	if total < 8 || total > 14 {
		t.Fatalf("total holdings should stay normalized, got %d", total)
	}
}
