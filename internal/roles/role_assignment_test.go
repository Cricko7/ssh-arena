package roles

import "testing"

func TestAllocatorBalancesNonWhales(t *testing.T) {
	allocator := NewAllocator(Config{
		Buyer:  Profile{Cash: 1000, BaseHoldings: map[string]int64{"*": 1}},
		Holder: Profile{Cash: 500, BaseHoldings: map[string]int64{"*": 10}},
		Whale:  Profile{Cash: 10000, BaseHoldings: map[string]int64{"*": 50}},
	})

	stats := Stats{Buyers: 5, Holders: 7, Whales: 1}
	assignment := allocator.Assign("new-player", stats, []string{"TECH"})
	if assignment.Role != RoleBuyer && assignment.Role != RoleWhale {
		t.Fatalf("expected buyer or whale, got %s", assignment.Role)
	}
}
