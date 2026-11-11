package download

import (
	"net/netip"
	"slices"
	"testing"
	"time"
)

func addrs(ports ...uint16) []netip.AddrPort {
	var out []netip.AddrPort
	for _, port := range ports {
		out = append(out, netip.AddrPortFrom(netip.MustParseAddr("10.0.0.1"), port))
	}
	return out
}

func TestPoolCapsAndKeepsSpares(t *testing.T) {
	p := newPool(2)
	now := time.Unix(0, 0)
	if got := p.admit(now, addrs(1, 2, 3)); !slices.Equal(got, addrs(1, 2)) {
		t.Fatalf("first batch %v", got)
	}
	if got := p.admit(now, addrs(2, 4)); len(got) != 0 {
		t.Fatalf("nothing should start while full: %v", got)
	}
	p.exited()
	if got := p.admit(now, nil); !slices.Equal(got, addrs(3)) {
		t.Fatalf("a spare should take the free slot: %v", got)
	}
	p.exited()
	p.exited()
	if got := p.admit(now, addrs(1)); !slices.Equal(got, addrs(4)) {
		t.Fatalf("peer 1 was tried just now, peer 4 waited as a spare: %v", got)
	}
	if got := p.admit(now.Add(retryAfter), addrs(1)); !slices.Equal(got, addrs(1)) {
		t.Fatalf("peer 1 may be retried later: %v", got)
	}
}

func TestPoolWithoutLimit(t *testing.T) {
	if got := newPool(0).admit(time.Now(), addrs(1, 2, 3)); len(got) != 3 {
		t.Fatalf("no limit should start everyone: %v", got)
	}
}
