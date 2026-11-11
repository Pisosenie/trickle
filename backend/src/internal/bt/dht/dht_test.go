package dht

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	anacrolixdht "github.com/anacrolix/dht/v2"

	"trickle/internal/bt/bencode"
)

func idWith(first byte) [20]byte {
	var id [20]byte
	id[0] = first
	id[19] = 1
	return id
}

func compactNode(n node) string {
	b := append([]byte{}, n.id[:]...)
	ip := n.addr.Addr().As4()
	b = append(b, ip[:]...)
	return string(binary.BigEndian.AppendUint16(b, n.addr.Port()))
}

func compactPeer(p netip.AddrPort) string {
	ip := p.Addr().As4()
	return string(binary.BigEndian.AppendUint16(ip[:], p.Port()))
}

// fakeNode answers DHT queries with whatever handle returns; nil means stay silent
func fakeNode(t *testing.T, id [20]byte, handle func(q map[string]any) map[string]any) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			v, err := bencode.Decode(buf[:n])
			msg, _ := v.(map[string]any)
			if err != nil || msg["y"] != "q" || msg["ro"] != int64(1) {
				continue
			}
			r := handle(msg)
			if r == nil {
				continue
			}
			var reply []byte
			if r["error"] != nil {
				reply, _ = bencode.Encode(map[string]any{"t": msg["t"], "y": "e", "e": []any{201, "nope"}})
			} else {
				r["id"] = string(id[:])
				reply, _ = bencode.Encode(map[string]any{"t": msg["t"], "y": "r", "r": r})
			}
			conn.WriteTo(reply, from)
		}
	}()
	return netip.MustParseAddrPort(conn.LocalAddr().String())
}

func newClient(t *testing.T) *Client {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 300 * time.Millisecond
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFindPeersWalksTowardsTarget(t *testing.T) {
	target := idWith(0x80)
	peer1 := netip.MustParseAddrPort("10.1.1.1:6881")
	peer2 := netip.MustParseAddrPort("10.2.2.2:51413")

	closest := node{id: idWith(0x81)}
	closest.addr = fakeNode(t, closest.id, func(q map[string]any) map[string]any {
		return map[string]any{"token": "x", "values": []any{compactPeer(peer1), compactPeer(peer2)}}
	})
	middle := node{id: idWith(0xc0)}
	middle.addr = fakeNode(t, middle.id, func(map[string]any) map[string]any {
		return map[string]any{"nodes": compactNode(closest)}
	})
	silent := node{id: idWith(0x90)}
	silent.addr = fakeNode(t, silent.id, func(map[string]any) map[string]any { return nil })
	failing := node{id: idWith(0xa0)}
	failing.addr = fakeNode(t, failing.id, func(map[string]any) map[string]any { return map[string]any{"error": true} })
	boot := fakeNode(t, idWith(0x01), func(q map[string]any) map[string]any {
		if q["q"] != "get_peers" {
			return nil
		}
		return map[string]any{"nodes": compactNode(middle) + compactNode(silent) + compactNode(failing)}
	})

	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peers, err := c.FindPeers(ctx, target, []netip.AddrPort{boot}, 10)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(peers, func(a, b netip.AddrPort) int { return a.Compare(b) })
	if !slices.Equal(peers, []netip.AddrPort{peer1, peer2}) {
		t.Fatalf("found %v", peers)
	}
}

func TestFindPeersEmpty(t *testing.T) {
	boot := fakeNode(t, idWith(1), func(map[string]any) map[string]any { return map[string]any{"token": "x"} })
	c := newClient(t)
	if _, err := c.FindPeers(context.Background(), idWith(2), []netip.AddrPort{boot}, 1); err == nil {
		t.Fatal("expected an error when nobody has peers")
	}
}

func TestSpoofedReplyIsIgnored(t *testing.T) {
	spoofer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer spoofer.Close()
	c := newClient(t)
	target := fakeNode(t, idWith(5), func(q map[string]any) map[string]any {
		reply, _ := bencode.Encode(map[string]any{"t": q["t"], "y": "r", "r": map[string]any{"id": string(make([]byte, 20))}})
		spoofer.WriteTo(reply, c.conn.LocalAddr())
		return nil
	})
	if _, err := c.Ping(context.Background(), target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a reply from another address must not count, got %v", err)
	}
}

func TestParsing(t *testing.T) {
	short := compactNode(node{id: idWith(1), addr: netip.MustParseAddrPort("1.2.3.4:5")})
	zeroPort := compactNode(node{id: idWith(2), addr: netip.MustParseAddrPort("1.2.3.4:0")})
	if nodes := compactNodes(short + zeroPort + "junk"); len(nodes) != 1 || nodes[0].addr.String() != "1.2.3.4:5" {
		t.Fatalf("nodes %+v", nodes)
	}
	if peers := compactPeers([]any{"abc", int64(1), compactPeer(netip.MustParseAddrPort("9.9.9.9:9"))}); len(peers) != 1 {
		t.Fatalf("peers %v", peers)
	}
	if _, _, err := parseReply([]byte("d1:t2:aa1:y1:e1:eli201e4:nopeee")); !errors.Is(err, ErrRemote) {
		t.Fatalf("error reply: %v", err)
	}
}

func anacrolixNode(t *testing.T, starting ...net.Addr) *anacrolixdht.Server {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := anacrolixdht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.NoSecurity = true
	cfg.StartingNodes = func() ([]anacrolixdht.Addr, error) {
		var addrs []anacrolixdht.Addr
		for _, a := range starting {
			addrs = append(addrs, anacrolixdht.NewAddr(a))
		}
		return addrs, nil
	}
	s, err := anacrolixdht.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestTalksToAnacrolixNode(t *testing.T) {
	remote := anacrolixNode(t)
	addr := netip.MustParseAddrPort(remote.Addr().String())
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := c.Ping(ctx, addr)
	if err != nil || id != remote.ID() {
		t.Fatalf("ping anacrolix: id %x, %v", id, err)
	}
	answered, _, _, err := c.getPeers(ctx, addr, idWith(0x42))
	if err != nil || answered.id != remote.ID() {
		t.Fatalf("get_peers to anacrolix: %+v, %v", answered, err)
	}
}
