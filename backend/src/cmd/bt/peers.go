package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"trickle/internal/bt/dht"
	"trickle/internal/bt/torrentfile"
	"trickle/internal/bt/tracker"
)

const (
	trackerTimeout = 15 * time.Second
	dhtTimeout     = 30 * time.Second
	dhtWant        = 100
	refillEvery    = 2 * time.Minute
)

var bootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
}

type peerSet struct {
	mu    sync.Mutex
	seen  map[netip.AddrPort]bool
	peers []netip.AddrPort
}

func (s *peerSet) add(found []netip.AddrPort) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[netip.AddrPort]bool{}
	}
	for _, p := range found {
		if !s.seen[p] {
			s.seen[p] = true
			s.peers = append(s.peers, p)
		}
	}
}

func announceAll(ctx context.Context, infoHash [20]byte, trackers []string, peerID [20]byte, left int64) ([]netip.AddrPort, int) {
	var (
		set      peerSet
		wg       sync.WaitGroup
		mu       sync.Mutex
		answered int
	)
	for _, tr := range trackers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, trackerTimeout)
			defer cancel()
			res, err := tracker.Announce(ctx, http.DefaultClient, tr, tracker.Request{
				InfoHash: infoHash, PeerID: peerID, Port: listenPort, Left: left, Event: tracker.EventStarted,
			})
			if err != nil {
				return
			}
			set.add(res.Peers)
			mu.Lock()
			answered++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return set.peers, answered
}

func gatherPeers(ctx context.Context, infoHash [20]byte, trackers []string, peerID [20]byte, left int64) []netip.AddrPort {
	var set peerSet
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		found, _ := announceAll(ctx, infoHash, trackers, peerID, left)
		set.add(found)
	}()
	go func() {
		defer wg.Done()
		if found, err := lookupDHT(ctx, infoHash, dhtWant); err == nil {
			set.add(found)
		}
	}()
	wg.Wait()
	return set.peers
}

func refill(ctx context.Context, tf torrentfile.File, peerID [20]byte) <-chan []netip.AddrPort {
	more := make(chan []netip.AddrPort)
	go func() {
		defer close(more)
		ticker := time.NewTicker(refillEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			found := gatherPeers(ctx, tf.InfoHash, tf.Trackers, peerID, tf.Length)
			select {
			case more <- found:
			case <-ctx.Done():
				return
			}
		}
	}()
	return more
}

func lookupDHT(ctx context.Context, infoHash [20]byte, want int) ([]netip.AddrPort, error) {
	ctx, cancel := context.WithTimeout(ctx, dhtTimeout)
	defer cancel()
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	client, err := dht.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer client.Close()
	return client.FindPeers(ctx, infoHash, resolve(ctx, bootstrapNodes), want)
}

func resolve(ctx context.Context, hosts []string) []netip.AddrPort {
	var addrs []netip.AddrPort
	for _, hostport := range hosts {
		host, portText, err := net.SplitHostPort(hostport)
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			continue
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			addrs = append(addrs, netip.AddrPortFrom(ip, uint16(port)))
		}
	}
	return addrs
}
