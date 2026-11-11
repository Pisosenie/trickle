package download

import (
	"cmp"
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

const (
	uploadSlots     = 4
	optimisticEvery = 3
)

var rechokeInterval = 10 * time.Second

type peerStat struct {
	l          *link
	interested bool
	unchoked   bool
	rate       int64
}

// the fastest interested peers get the slots, ties keep the current holder; the optimistic slot moves on rotation
func chooseUnchoked(stats []peerStat, slots int, optimistic *link, rotate bool, pick func(n int) int) (map[*link]bool, *link) {
	var interested []peerStat
	for _, st := range stats {
		if st.interested {
			st.unchoked = st.unchoked && st.l != optimistic
			interested = append(interested, st)
		}
	}
	slices.SortStableFunc(interested, func(a, b peerStat) int {
		if byRate := cmp.Compare(b.rate, a.rate); byRate != 0 {
			return byRate
		}
		return cmp.Compare(boolRank(b.unchoked), boolRank(a.unchoked))
	})

	chosen := map[*link]bool{}
	for _, st := range interested[:min(slots, len(interested))] {
		chosen[st.l] = true
	}
	var rest []*link
	for _, st := range interested {
		if !chosen[st.l] {
			rest = append(rest, st.l)
		}
	}
	if rotate || !slices.Contains(rest, optimistic) {
		others := slices.DeleteFunc(slices.Clone(rest), func(l *link) bool { return l == optimistic })
		if len(others) == 0 {
			others = rest
		}
		optimistic = nil
		if len(others) > 0 {
			optimistic = others[pick(len(others))]
		}
	}
	if optimistic != nil {
		chosen[optimistic] = true
	}
	return chosen, optimistic
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *session) startChoker(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.chokeLoop(ctx)
	}()
}

func (s *session) chokeLoop(ctx context.Context) {
	ticker := time.NewTicker(s.rechokeEvery)
	defer ticker.Stop()
	var optimistic *link
	for round := 1; ; round++ {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		optimistic = s.rechoke(optimistic, round%optimisticEvery == 0)
	}
}

// while downloading, reward peers that give us data; while seeding, those that take it fastest
func (s *session) rechoke(optimistic *link, rotate bool) *link {
	seeding := s.missing() == 0
	s.mu.Lock()
	stats := make([]peerStat, 0, len(s.links))
	for l := range s.links {
		received, sent := l.received.Swap(0), l.sent.Swap(0)
		rate := received
		if seeding {
			rate = sent
		}
		stats = append(stats, peerStat{l: l, interested: l.peerInterested.Load(), unchoked: l.wantUnchoke.Load(), rate: rate})
	}
	s.mu.Unlock()

	chosen, optimistic := chooseUnchoked(stats, uploadSlots, optimistic, rotate, rand.IntN)
	for _, st := range stats {
		st.l.wantUnchoke.Store(chosen[st.l])
	}
	return optimistic
}

// a newly interested peer takes a free slot at once instead of waiting for the next round
func (s *session) offerSlot(l *link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	open := 0
	for other := range s.links {
		if other.wantUnchoke.Load() {
			open++
		}
	}
	if open < uploadSlots+1 {
		l.wantUnchoke.Store(true)
	}
}
