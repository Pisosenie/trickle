package download

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trickle/internal/bt/peer"
	"trickle/internal/bt/torrentfile"
)

var seedsAndUnchokes = []*peer.Message{{ID: peer.HaveAll}, {ID: peer.Unchoke}}

// fastFake speaks the fast extension: it sends greet, then hands every request to answer
func fastFake(t *testing.T, fx fixture, greet []*peer.Message, answer func(nc net.Conn, req peer.BlockRequest) bool) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		if _, err := peer.ReadHandshake(nc); err != nil {
			return
		}
		hs := peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'F', 'A'}}
		hs.Reserved[7] |= 0x04
		nc.Write(hs.Bytes())
		for _, m := range greet {
			nc.Write(m.Bytes())
		}
		for {
			m, err := peer.ReadMessage(nc)
			if err != nil {
				return
			}
			if m == nil || m.ID != peer.Request {
				continue
			}
			req, _ := peer.ParseRequest(m)
			if !answer(nc, req) {
				return
			}
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func write(nc net.Conn, m *peer.Message) bool {
	_, err := nc.Write(m.Bytes())
	return err == nil
}

func blockOf(fx fixture, req peer.BlockRequest) *peer.Message {
	off := req.Index*pieceLength + req.Begin
	return peer.NewPiece(req.Index, req.Begin, fx.files["image.iso"][off:off+req.Length])
}

func TestRefusedPieceGoesToAnotherPeer(t *testing.T) {
	fx := newFixture(t, false)
	var asked atomic.Int32
	refused := make(chan struct{})
	var once sync.Once
	stubborn := fastFake(t, fx, seedsAndUnchokes, func(nc net.Conn, req peer.BlockRequest) bool {
		if req.Index != 0 {
			return write(nc, blockOf(fx, req))
		}
		asked.Add(1)
		once.Do(func() { close(refused) })
		return write(nc, peer.NewReject(req.Index, req.Begin, req.Length))
	})
	helper := ourSeeder(t, fx, 0)
	more := make(chan []netip.AddrPort, 1)
	go func() {
		<-refused
		time.Sleep(300 * time.Millisecond)
		more <- []netip.AddrPort{helper}
	}()

	out := t.TempDir()
	if _, err := download(t, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{stubborn}, MorePeers: more, Dir: out}); err != nil {
		t.Fatal(err)
	}
	fx.check(t, out)
	if n := asked.Load(); n > 2*maxBacklog {
		t.Fatalf("piece 0 was asked %d times after the peer kept refusing it", n)
	}
}

// keeper chokes on the first request, keeps it, rejects the rest and unchokes; asking for the kept block again is a duplicate
func TestFastChokeKeepsUnrejectedRequests(t *testing.T) {
	fx := newFixture(t, false)
	var (
		duplicate atomic.Bool
		kept      peer.BlockRequest
		held      []peer.BlockRequest
		waiting   int
		phase     int
	)
	keeper := fastFake(t, fx, seedsAndUnchokes, func(nc net.Conn, req peer.BlockRequest) bool {
		switch phase {
		case 0:
			kept, phase = req, 1
			blocks := int((fx.tf.PieceSize(req.Index) + blockSize - 1) / blockSize)
			waiting = min(maxBacklog, blocks) - 1
			write(nc, &peer.Message{ID: peer.Choke})
		case 1:
			held = append(held, req)
		case 2:
			if req == kept {
				duplicate.Store(true)
				return false
			}
			waiting--
			write(nc, blockOf(fx, req))
		default:
			return write(nc, blockOf(fx, req))
		}
		if phase == 1 && len(held) == waiting {
			for _, r := range held {
				write(nc, peer.NewReject(r.Index, r.Begin, r.Length))
			}
			write(nc, &peer.Message{ID: peer.Unchoke})
			phase = 2
		}
		if phase == 2 && waiting == 0 {
			phase = 3
			return write(nc, blockOf(fx, kept))
		}
		return true
	})

	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Download(ctx, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{keeper}, Dir: out})
	if duplicate.Load() {
		t.Fatal("a request the peer kept through the choke was sent again")
	}
	if err != nil {
		t.Fatal(err)
	}
	fx.check(t, out)
}

func TestSeederRejectsWhileChoking(t *testing.T) {
	fx := newFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := peer.Dial(ctx, ourSeeder(t, fx, 0), fx.tf.InfoHash, ourID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	first, err := c.Read()
	if err != nil || first == nil || first.ID != peer.HaveAll {
		t.Fatalf("a complete seeder should open with have all, got %v, %v", first, err)
	}
	if err := c.Send(peer.NewRequest(1, 0, blockSize)); err != nil {
		t.Fatal(err)
	}
	for {
		m, err := c.Read()
		if err != nil {
			t.Fatal(err)
		}
		if m == nil {
			continue
		}
		switch m.ID {
		case peer.Piece:
			t.Fatal("served a peer it was choking")
		case peer.Reject:
			if req, _ := peer.ParseRequest(m); req != (peer.BlockRequest{Index: 1, Length: blockSize}) {
				t.Fatalf("rejected %+v", req)
			}
			return
		}
	}
}

func TestAnnouncementFollowsTheFastBit(t *testing.T) {
	s := &session{tf: torrentfile.File{Pieces: make([][20]byte, 10)}}
	cases := []struct {
		have peer.Bitfield
		fast bool
		want peer.ID
	}{
		{peer.Bitfield{0, 0}, true, peer.HaveNone},
		{peer.Bitfield{0x80, 0}, true, peer.BitfieldMsg},
		{peer.Full(10), true, peer.HaveAll},
		{peer.Full(10), false, peer.BitfieldMsg},
	}
	for _, tc := range cases {
		s.have = tc.have
		if m := s.announcement(tc.fast); m == nil || m.ID != tc.want {
			t.Errorf("have %08b fast=%v: got %v, want %v", tc.have, tc.fast, m, tc.want)
		}
	}
	s.have = peer.Bitfield{0, 0}
	if m := s.announcement(false); m != nil {
		t.Errorf("a peer without the fast bit should hear nothing when we have nothing, got %v", m.ID)
	}
}

func TestRejectOfACancelKeepsTheRetry(t *testing.T) {
	p := newProgress(work{index: 2, length: 3 * blockSize}, time.Now())
	send := func(*peer.Message) error { return nil }
	p.request(send)
	p.markLost(0)
	p.cancelLost(send)
	p.request(send)
	first := peer.BlockRequest{Index: 2, Length: blockSize}
	if p.rejected(first) || !p.inFlight[0] {
		t.Fatal("the reject answering our cancel freed the retry")
	}
	if !p.rejected(first) || p.inFlight[0] || p.backlog != 2 {
		t.Fatalf("the retry's own reject should free it: in flight %v, backlog %d", p.inFlight, p.backlog)
	}
	if p.rejected(first) || p.rejected(peer.BlockRequest{Index: 3}) {
		t.Fatal("stray rejects must change nothing")
	}
}

func grantingAll(fx fixture) []*peer.Message {
	greet := []*peer.Message{{ID: peer.HaveAll}}
	for i := range fx.tf.Pieces {
		greet = append(greet, peer.NewAllowedFast(i))
	}
	return greet
}

func TestAllowedFastPiecesFlowWhileChoked(t *testing.T) {
	fx := newFixture(t, false)
	never := fastFake(t, fx, grantingAll(fx), func(nc net.Conn, req peer.BlockRequest) bool {
		return write(nc, blockOf(fx, req))
	})
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Download(ctx, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{never}, Dir: out}); err != nil {
		t.Fatalf("every piece was allowed fast, the choke should not matter: %v", err)
	}
	fx.check(t, out)
}

// the peer takes piece 0 back with a reject while choking; we wait for the unchoke instead of asking again and again
func TestWithdrawnAllowedFastWaitsForUnchoke(t *testing.T) {
	fx := newFixture(t, false)
	var (
		mu       sync.Mutex
		unchoked atomic.Bool
		asked    atomic.Int32
		once     sync.Once
	)
	send := func(nc net.Conn, m *peer.Message) bool {
		mu.Lock()
		defer mu.Unlock()
		return write(nc, m)
	}
	peerAddr := fastFake(t, fx, grantingAll(fx), func(nc net.Conn, req peer.BlockRequest) bool {
		if req.Index != 0 || unchoked.Load() {
			return send(nc, blockOf(fx, req))
		}
		asked.Add(1)
		once.Do(func() {
			time.AfterFunc(300*time.Millisecond, func() {
				unchoked.Store(true)
				send(nc, &peer.Message{ID: peer.Unchoke})
			})
		})
		return send(nc, peer.NewReject(req.Index, req.Begin, req.Length))
	})
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Download(ctx, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{peerAddr}, Dir: out}); err != nil {
		t.Fatal(err)
	}
	fx.check(t, out)
	if n := asked.Load(); n > maxBacklog {
		t.Fatalf("piece 0 was asked %d times while the peer was refusing it", n)
	}
}
