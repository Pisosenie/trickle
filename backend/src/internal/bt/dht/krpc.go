package dht

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"trickle/internal/bt/bencode"
)

var (
	ErrRemote   = errors.New("DHT node returned an error")
	errNotReply = errors.New("not a reply")
)

type node struct {
	id   [20]byte
	addr netip.AddrPort
}

// "ro" marks us as read-only (BEP 43): we ask but never answer, so nobody adds us to routing tables
func encodeQuery(tid, method string, args map[string]any) ([]byte, error) {
	return bencode.Encode(map[string]any{"t": tid, "y": "q", "q": method, "a": args, "ro": 1})
}

func parseReply(data []byte) (string, map[string]any, error) {
	v, err := bencode.Decode(data)
	if err != nil {
		return "", nil, err
	}
	msg, _ := v.(map[string]any)
	tid, _ := msg["t"].(string)
	switch msg["y"] {
	case "r":
		r, ok := msg["r"].(map[string]any)
		if !ok {
			return tid, nil, fmt.Errorf("reply without body")
		}
		return tid, r, nil
	case "e":
		details, _ := msg["e"].([]any)
		return tid, nil, fmt.Errorf("%w: %v", ErrRemote, details)
	default:
		return tid, nil, errNotReply
	}
}

func nodeID(r map[string]any) ([20]byte, bool) {
	var id [20]byte
	raw, _ := r["id"].(string)
	if len(raw) != len(id) {
		return id, false
	}
	copy(id[:], raw)
	return id, true
}

func compactNodes(raw string) []node {
	const size = 26
	var nodes []node
	for i := 0; i+size <= len(raw); i += size {
		var n node
		copy(n.id[:], raw[i:i+20])
		ip, _ := netip.AddrFromSlice([]byte(raw[i+20 : i+24]))
		port := binary.BigEndian.Uint16([]byte(raw[i+24 : i+26]))
		if port == 0 {
			continue
		}
		n.addr = netip.AddrPortFrom(ip, port)
		nodes = append(nodes, n)
	}
	return nodes
}

func compactPeers(values []any) []netip.AddrPort {
	var peers []netip.AddrPort
	for _, v := range values {
		raw, _ := v.(string)
		if len(raw) != 6 {
			continue
		}
		ip, _ := netip.AddrFromSlice([]byte(raw[:4]))
		port := binary.BigEndian.Uint16([]byte(raw[4:]))
		if port != 0 {
			peers = append(peers, netip.AddrPortFrom(ip, port))
		}
	}
	return peers
}
