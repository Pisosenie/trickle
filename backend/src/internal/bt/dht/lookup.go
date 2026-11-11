package dht

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
)

const (
	alpha      = 4
	k          = 8
	maxQueries = 200
	maxKnown   = 64
)

// FindPeers walks the DHT towards infoHash and collects the peers nodes know about
func (c *Client) FindPeers(ctx context.Context, infoHash [20]byte, bootstrap []netip.AddrPort, want int) ([]netip.AddrPort, error) {
	peers, _, err := c.traverse(ctx, infoHash, bootstrap, want)
	if err != nil && len(peers) == 0 {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, errors.New("the DHT knows no peers for this torrent")
	}
	return peers, nil
}

// Announce tells the k nodes closest to infoHash that we serve it on port, with the token each handed out
func (c *Client) Announce(ctx context.Context, infoHash [20]byte, bootstrap []netip.AddrPort, port int) (int, error) {
	_, replies, err := c.traverse(ctx, infoHash, bootstrap, math.MaxInt)
	if err != nil && len(replies) == 0 {
		return 0, err
	}
	var withToken []getPeersReply
	for _, r := range replies {
		if r.token != "" {
			withToken = append(withToken, r)
		}
	}
	slices.SortFunc(withToken, func(a, b getPeersReply) int { return compareDistance(a.from.id, b.from.id, infoHash) })
	withToken = withToken[:min(k, len(withToken))]

	var wg sync.WaitGroup
	var accepted atomic.Int32
	for _, r := range withToken {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.announcePeer(ctx, r.from.addr, infoHash, port, r.token) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() == 0 {
		return 0, errors.New("no DHT node accepted the announce")
	}
	return int(accepted.Load()), nil
}

func (c *Client) traverse(ctx context.Context, infoHash [20]byte, bootstrap []netip.AddrPort, want int) ([]netip.AddrPort, []getPeersReply, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := slices.Clone(bootstrap)
	var known []node
	asked := map[netip.AddrPort]bool{}
	seen := map[netip.AddrPort]bool{}
	var peers []netip.AddrPort
	var replies []getPeersReply

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

	type outcome struct {
		reply getPeersReply
		err   error
	}
	results := make(chan outcome)
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
				r, err := c.getPeers(ctx, addr, infoHash)
				select {
				case results <- outcome{reply: r, err: err}:
				case <-ctx.Done():
				}
			}()
		}
		if inflight == 0 {
			return peers, replies, nil
		}

		select {
		case o := <-results:
			inflight--
			if o.err != nil {
				continue
			}
			replies = append(replies, o.reply)
			for _, p := range o.reply.peers {
				if !seen[p] {
					seen[p] = true
					peers = append(peers, p)
				}
			}
			known = merge(known, o.reply.nodes, asked, infoHash)
		case <-ctx.Done():
			return peers, replies, ctx.Err()
		}
		if len(peers) >= want {
			return peers, replies, nil
		}
	}
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
