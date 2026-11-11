package download

import (
	"net/netip"
	"time"
)

const retryAfter = 5 * time.Minute

// at most max workers at once; a peer we already tried waits retryAfter before the next attempt
type pool struct {
	max    int
	active int
	tried  map[netip.AddrPort]time.Time
	spare  []netip.AddrPort
}

func newPool(max int) *pool {
	return &pool{max: max, tried: map[netip.AddrPort]time.Time{}}
}

func (p *pool) admit(now time.Time, addrs []netip.AddrPort) []netip.AddrPort {
	for _, a := range addrs {
		if last, seen := p.tried[a]; seen && now.Sub(last) < retryAfter {
			continue
		}
		p.tried[a] = now
		p.spare = append(p.spare, a)
	}
	var start []netip.AddrPort
	for len(p.spare) > 0 && (p.max <= 0 || p.active < p.max) {
		start = append(start, p.spare[0])
		p.spare = p.spare[1:]
		p.active++
	}
	return start
}

func (p *pool) exited() {
	p.active--
}
