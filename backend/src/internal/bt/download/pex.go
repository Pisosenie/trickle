package download

import (
	"encoding/binary"
	"net"
	"net/netip"
	"time"

	"trickle/internal/bt/bencode"
	"trickle/internal/bt/peer"
)

const (
	extHandshake = 0
	pexID        = 1
	maxPexPeers  = 50
	maxPexAccept = 200
	reachable    = 0x10
)

var pexInterval = time.Minute

func (s *session) extHandshake() []byte {
	hello := map[string]any{"m": map[string]any{"ut_pex": pexID}, "v": "Trickle"}
	if tcp, ok := addrOf(s.cfg.Listener).(*net.TCPAddr); ok && tcp.Port > 0 {
		hello["p"] = tcp.Port
	}
	payload, _ := bencode.Encode(hello)
	return append([]byte{extHandshake}, payload...)
}

func addrOf(ln net.Listener) net.Addr {
	if ln == nil {
		return nil
	}
	return ln.Addr()
}

// malformed extension messages are ignored, they are optional
func (l *link) extended(m *peer.Message) {
	if len(m.Payload) == 0 {
		return
	}
	switch m.Payload[0] {
	case extHandshake:
		v, err := bencode.Decode(m.Payload[1:])
		if err != nil {
			return
		}
		dict, _ := v.(map[string]any)
		exts, _ := dict["m"].(map[string]any)
		if id, ok := exts["ut_pex"].(int64); ok && id > 0 && id < 256 {
			l.theirPex = byte(id)
		}
		if port, ok := dict["p"].(int64); ok && !l.outgoing && port > 0 && port < 1<<16 {
			l.s.setAddr(l, netip.AddrPortFrom(l.c.RemoteAddr().Addr(), uint16(port)))
		}
	case pexID:
		l.s.discover(parsePex(m.Payload[1:]))
	}
}

func parsePex(payload []byte) []netip.AddrPort {
	v, err := bencode.Decode(payload)
	if err != nil {
		return nil
	}
	dict, _ := v.(map[string]any)
	added, _ := dict["added"].(string)
	added6, _ := dict["added6"].(string)
	peers := append(decodeCompact(added, 4), decodeCompact(added6, 16)...)
	return peers[:min(len(peers), maxPexAccept)]
}

func decodeCompact(raw string, addrLen int) []netip.AddrPort {
	size := addrLen + 2
	var peers []netip.AddrPort
	for i := 0; i+size <= len(raw); i += size {
		ip, _ := netip.AddrFromSlice([]byte(raw[i : i+addrLen]))
		ap := netip.AddrPortFrom(ip.Unmap(), binary.BigEndian.Uint16([]byte(raw[i+addrLen:i+size])))
		if usable(ap) {
			peers = append(peers, ap)
		}
	}
	return peers
}

func usable(ap netip.AddrPort) bool {
	a := ap.Addr()
	return ap.Port() != 0 && a.IsValid() && !a.IsUnspecified() && !a.IsMulticast()
}

func encodeCompact(peers []netip.AddrPort) (v4, v6 string, flags4, flags6 string) {
	var b4, b6, f4, f6 []byte
	for _, p := range peers {
		ip := p.Addr()
		if ip.Is4() {
			b4 = binary.BigEndian.AppendUint16(append(b4, ip.AsSlice()...), p.Port())
			f4 = append(f4, reachable)
		} else {
			b6 = binary.BigEndian.AppendUint16(append(b6, ip.AsSlice()...), p.Port())
			f6 = append(f6, reachable)
		}
	}
	return string(b4), string(b6), string(f4), string(f6)
}

// only the difference since the last message goes out, and only addresses we know accept connections
func (l *link) pexMessage() *peer.Message {
	current := l.s.connectable(l)
	var added, dropped []netip.AddrPort
	for _, a := range current {
		if !l.shared[a] && len(added) < maxPexPeers {
			added = append(added, a)
		}
	}
	live := map[netip.AddrPort]bool{}
	for _, a := range current {
		live[a] = true
	}
	for a := range l.shared {
		if !live[a] && len(dropped) < maxPexPeers {
			dropped = append(dropped, a)
		}
	}
	if len(added) == 0 && len(dropped) == 0 {
		return nil
	}
	for _, a := range added {
		l.shared[a] = true
	}
	for _, a := range dropped {
		delete(l.shared, a)
	}

	add4, add6, f4, f6 := encodeCompact(added)
	drop4, drop6, _, _ := encodeCompact(dropped)
	payload, _ := bencode.Encode(map[string]any{
		"added": add4, "added.f": f4, "added6": add6, "added6.f": f6,
		"dropped": drop4, "dropped6": drop6,
	})
	return &peer.Message{ID: peer.Extended, Payload: append([]byte{l.theirPex}, payload...)}
}

func (s *session) setAddr(l *link, addr netip.AddrPort) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.addr = addr
}

func (s *session) connectable(except *link) []netip.AddrPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	var addrs []netip.AddrPort
	for l := range s.links {
		if l != except && l.addr.IsValid() && l.addr != except.addr {
			addrs = append(addrs, l.addr)
		}
	}
	return addrs
}

// a full queue just loses this batch; the peer will gossip again
func (s *session) discover(addrs []netip.AddrPort) {
	if s.found == nil {
		return
	}
	fresh := s.notOurs(addrs)
	if len(fresh) == 0 {
		return
	}
	select {
	case s.found <- fresh:
	default:
	}
}

// peers happily gossip our own address back to us
func (s *session) notOurs(addrs []netip.AddrPort) []netip.AddrPort {
	ours := map[netip.AddrPort]bool{}
	if tcp, ok := addrOf(s.cfg.Listener).(*net.TCPAddr); ok {
		ap := tcp.AddrPort()
		ours[netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())] = true
	}
	s.mu.Lock()
	for l := range s.links {
		ours[l.c.LocalAddr()] = true
	}
	s.mu.Unlock()

	var fresh []netip.AddrPort
	for _, a := range addrs {
		if !ours[a] {
			fresh = append(fresh, a)
		}
	}
	return fresh
}
