package download

import (
	"net"
	"sync"
	"testing"
	"time"

	"trickle/internal/bt/peer"
)

func TestChooseUnchokedRewardsTheFastest(t *testing.T) {
	links := make([]*link, 7)
	for i := range links {
		links[i] = &link{}
	}
	stats := []peerStat{
		{l: links[0], interested: true, rate: 10},
		{l: links[1], interested: true, rate: 500},
		{l: links[2], interested: true, rate: 300},
		{l: links[3], interested: false, rate: 9000},
		{l: links[4], interested: true, rate: 200},
		{l: links[5], interested: true, rate: 100},
		{l: links[6], interested: true, rate: 0},
	}
	first := func(int) int { return 0 }

	chosen, optimistic := chooseUnchoked(stats, 4, nil, false, first)
	for _, want := range []int{1, 2, 4, 5} {
		if !chosen[links[want]] {
			t.Errorf("peer %d is among the four fastest and must be unchoked", want)
		}
	}
	if chosen[links[3]] {
		t.Error("a peer that is not interested gets no slot")
	}
	if optimistic != links[0] || !chosen[links[0]] || len(chosen) != 5 {
		t.Fatalf("one optimistic slot expected, got %v and %d unchoked", optimistic == links[0], len(chosen))
	}

	_, kept := chooseUnchoked(stats, 4, optimistic, false, func(int) int { return 1 })
	if kept != links[0] {
		t.Fatal("the optimistic peer keeps its slot until the next rotation")
	}
	_, rotated := chooseUnchoked(stats, 4, optimistic, true, func(int) int { return 0 })
	if rotated != links[6] {
		t.Fatal("rotation should move the optimistic slot to the other waiting peer")
	}
}

func TestChooseUnchokedWithFewPeers(t *testing.T) {
	a, b := &link{}, &link{}
	chosen, optimistic := chooseUnchoked([]peerStat{{l: a, interested: true}, {l: b}}, 4, nil, true, func(int) int { return 0 })
	if !chosen[a] || chosen[b] || optimistic != nil {
		t.Fatalf("one interested peer just gets a regular slot: %v %v", chosen, optimistic)
	}
}

func TestTiesKeepTheCurrentHolders(t *testing.T) {
	links := make([]*link, 6)
	stats := make([]peerStat, 6)
	for i := range links {
		links[i] = &link{}
		stats[i] = peerStat{l: links[i], interested: true, unchoked: i >= 2}
	}
	chosen, optimistic := chooseUnchoked(stats, 4, nil, false, func(int) int { return 0 })
	for i := 2; i < 6; i++ {
		if !chosen[links[i]] {
			t.Fatalf("with equal rates peer %d should keep its slot", i)
		}
	}
	if optimistic != links[0] {
		t.Fatal("the optimistic slot goes to one of the others")
	}
	_, next := chooseUnchoked(stats, 4, optimistic, true, func(int) int { return 0 })
	if next != links[1] {
		t.Fatal("rotation must hand the optimistic slot to a different peer")
	}
}

func TestSessionKeepsSlotsLimited(t *testing.T) {
	s := &session{links: map[*link]struct{}{}, tf: newFixture(t, false).tf, have: peer.Bitfield{}}
	peers := make([]*link, 6)
	for i := range peers {
		peers[i] = &link{s: s}
		peers[i].peerInterested.Store(true)
		s.links[peers[i]] = struct{}{}
		s.offerSlot(peers[i])
	}
	countOpen := func() int {
		n := 0
		for _, l := range peers {
			if l.wantUnchoke.Load() {
				n++
			}
		}
		return n
	}
	if n := countOpen(); n != uploadSlots+1 {
		t.Fatalf("free slots should fill at once up to %d, got %d", uploadSlots+1, n)
	}

	ever := map[*link]bool{}
	var optimistic *link
	for round := 1; round <= 6; round++ {
		optimistic = s.rechoke(optimistic, round%optimisticEvery == 0)
		if n := countOpen(); n != uploadSlots+1 {
			t.Fatalf("round %d: %d unchoked, want %d", round, n, uploadSlots+1)
		}
		for _, l := range peers {
			if l.wantUnchoke.Load() {
				ever[l] = true
			}
		}
	}
	if len(ever) != len(peers) {
		t.Fatalf("every interested peer should get a turn, %d of %d did", len(ever), len(peers))
	}
}

type slotWatch struct {
	mu   sync.Mutex
	ever map[int]bool
}

func (w *slotWatch) unchoked(i int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ever[i] = true
}

func TestSeederRotatesSlotsOverTheWire(t *testing.T) {
	old := rechokeInterval
	rechokeInterval = 100 * time.Millisecond
	t.Cleanup(func() { rechokeInterval = old })

	fx := newFixture(t, false)
	seeder := ourSeeder(t, fx, 0)
	watch := &slotWatch{ever: map[int]bool{}}
	const leechers = 6
	for i := range leechers {
		go func() {
			nc, err := net.Dial("tcp", seeder.String())
			if err != nil {
				return
			}
			defer nc.Close()
			nc.Write(peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'L', 'C', byte(i)}}.Bytes())
			if _, err := peer.ReadHandshake(nc); err != nil {
				return
			}
			nc.Write((&peer.Message{ID: peer.Interested}).Bytes())
			for {
				m, err := peer.ReadMessage(nc)
				if err != nil {
					return
				}
				if m != nil && m.ID == peer.Unchoke {
					watch.unchoked(i)
				}
			}
		}()
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		watch.mu.Lock()
		got := len(watch.ever)
		watch.mu.Unlock()
		if got == leechers {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("rotation should reach every interested peer, only %d of %d got a turn", len(watch.ever), leechers)
}
