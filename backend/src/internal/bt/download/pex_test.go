package download

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"trickle/internal/bt/bencode"
	"trickle/internal/bt/peer"
)

func TestPexRoundTrip(t *testing.T) {
	peers := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:6881"),
		netip.MustParseAddrPort("[2001:db8::7]:51413"),
	}
	v4, v6, f4, _ := encodeCompact(peers)
	junk := string([]byte{0, 0, 0, 0, 0x1a, 0xe1}) + string([]byte{10, 0, 0, 2, 0, 0})
	payload, _ := bencode.Encode(map[string]any{"added": v4 + junk, "added6": v6, "added.f": f4})
	got := parsePex(payload)
	if !slices.Equal(got, peers) {
		t.Fatalf("got %v, want %v (unspecified and port-0 entries dropped)", got, peers)
	}
	if f4 != string([]byte{reachable}) {
		t.Fatalf("flags %x", f4)
	}
}

// gossip knows nothing itself, but tells us where a seeder is
func gossip(t *testing.T, fx fixture, tip netip.AddrPort, hello chan<- map[string]any) netip.AddrPort {
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
		hs := peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'G', 'O'}}
		hs.Reserved[5] |= 0x10
		nc.Write(hs.Bytes())
		for {
			m, err := peer.ReadMessage(nc)
			if err != nil {
				return
			}
			if m == nil || m.ID != peer.Extended || m.Payload[0] != extHandshake {
				continue
			}
			v, _ := bencode.Decode(m.Payload[1:])
			if hello != nil {
				hello <- v.(map[string]any)
			}
			ours, _ := v.(map[string]any)["m"].(map[string]any)["ut_pex"].(int64)
			reply, _ := bencode.Encode(map[string]any{"m": map[string]any{"ut_pex": 7}})
			nc.Write((&peer.Message{ID: peer.Extended, Payload: append([]byte{0}, reply...)}).Bytes())
			v4, _, _, _ := encodeCompact([]netip.AddrPort{tip})
			pex, _ := bencode.Encode(map[string]any{"added": v4})
			nc.Write((&peer.Message{ID: peer.Extended, Payload: append([]byte{byte(ours)}, pex...)}).Bytes())
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func TestGossipLeadsToASeeder(t *testing.T) {
	fx := newFixture(t, false)
	hello := make(chan map[string]any, 1)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	out := t.TempDir()
	if _, err := download(t, Config{
		Torrent:  fx.tf,
		PeerID:   ourID,
		Peers:    []netip.AddrPort{gossip(t, fx, seed(t, fx), hello)},
		Listener: ln,
		Dir:      out,
	}); err != nil {
		t.Fatalf("a peer heard through PEX should have been used: %v", err)
	}
	fx.check(t, out)

	ours := <-hello
	if port, _ := ours["p"].(int64); port != int64(ln.Addr().(*net.TCPAddr).Port) {
		t.Fatalf("our extended handshake should carry the listen port, got %v", ours)
	}
}

// listener records the PEX messages we send to it
func listener(t *testing.T, fx fixture, got chan<- []netip.AddrPort) netip.AddrPort {
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
		hs := peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'L', 'S'}}
		hs.Reserved[5] |= 0x10
		nc.Write(hs.Bytes())
		reply, _ := bencode.Encode(map[string]any{"m": map[string]any{"ut_pex": 9}})
		nc.Write((&peer.Message{ID: peer.Extended, Payload: append([]byte{0}, reply...)}).Bytes())
		for {
			m, err := peer.ReadMessage(nc)
			if err != nil {
				return
			}
			if m != nil && m.ID == peer.Extended && m.Payload[0] == 9 {
				got <- parsePex(m.Payload[1:])
			}
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func TestWeShareConnectablePeers(t *testing.T) {
	old := pexInterval
	pexInterval = 50 * time.Millisecond
	defer func() { pexInterval = old }()

	fx := newFixture(t, false)
	got := make(chan []netip.AddrPort, 16)
	me := listener(t, fx, got)
	seeder := ourSeeder(t, fx, 0)
	if _, err := download(t, Config{
		Torrent:       fx.tf,
		PeerID:        ourID,
		Peers:         []netip.AddrPort{me, seeder},
		Dir:           t.TempDir(),
		DownloadLimit: 512 << 10,
	}); err != nil {
		t.Fatal(err)
	}

	for {
		select {
		case peers := <-got:
			if slices.Contains(peers, me) {
				t.Fatalf("a peer must not be told about itself: %v", peers)
			}
			if slices.Contains(peers, seeder) {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("the seeder's address was never shared")
		}
	}
}

func TestSelfConnectionIsDropped(t *testing.T) {
	fx := newFixture(t, false)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		peer.ReadHandshake(nc)
		nc.Write(peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: ourID}.Bytes())
		peer.ReadMessage(nc)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := Download(ctx, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{netip.MustParseAddrPort(ln.Addr().String())}, Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "all peers are gone") {
		t.Fatalf("talking to ourselves must end at once, got %v", err)
	}
}
