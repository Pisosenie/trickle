package dht

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"slices"
)

const (
	alpha      = 4
	k          = 8
	maxQueries = 200
	maxKnown   = 64
)

type lookupResult struct {
	peers []netip.AddrPort
	nodes []node
}

// FindPeers walks the DHT towards infoHash: ask the closest nodes we know,
// learn closer ones from their answers, and collect peers on the way
func (c *Client) FindPeers(ctx context.Context, infoHash [20]byte, bootstrap []netip.AddrPort, want int) ([]netip.AddrPort, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := slices.Clone(bootstrap)
	var known []node
	asked := map[netip.AddrPort]bool{}
	seen := map[netip.AddrPort]bool{}
	var peers []netip.AddrPort

	pick := func() (netip.AddrPort, bool) {
		for len(queue) > 0 {
			addr := queue[0]
			queue = queue[1:]
			if !asked[addr] {
				return addr, true
			}
		}
		for _, n := range known[:min(k, len(known))] {
			if !asked[n.addr] {
				return n.addr, true
			}
		}
		return netip.AddrPort{}, false
	}

	results := make(chan lookupResult)
	inflight, queries := 0, 0
	for {
		for inflight < alpha && queries < maxQueries {
			addr, ok := pick()
			if !ok {
				break
			}
			asked[addr] = true
			inflight++
			queries++
			go func() {
				_, found, nodes, _ := c.getPeers(ctx, addr, infoHash)
				select {
				case results <- lookupResult{peers: found, nodes: nodes}:
				case <-ctx.Done():
				}
			}()
		}
		if inflight == 0 {
			break
		}

		select {
		case r := <-results:
			inflight--
			for _, p := range r.peers {
				if !seen[p] {
					seen[p] = true
					peers = append(peers, p)
				}
			}
			known = merge(known, r.nodes, asked, infoHash)
		case <-ctx.Done():
			return peers, ctx.Err()
		}
		if len(peers) >= want {
			break
		}
	}
	if len(peers) == 0 {
		return nil, errors.New("the DHT knows no peers for this torrent")
	}
	return peers, nil
}

func merge(known, found []node, asked map[netip.AddrPort]bool, target [20]byte) []node {
	next := slices.Clone(known)
	for _, n := range found {
		if !asked[n.addr] && !slices.ContainsFunc(next, func(o node) bool { return o.addr == n.addr }) {
			next = append(next, n)
		}
	}
	slices.SortFunc(next, func(a, b node) int { return compareDistance(a.id, b.id, target) })
	return next[:min(len(next), maxKnown)]
}

func compareDistance(a, b, target [20]byte) int {
	da, db := xor(a, target), xor(b, target)
	return bytes.Compare(da[:], db[:])
}

func xor(a, b [20]byte) [20]byte {
	var out [20]byte
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}
