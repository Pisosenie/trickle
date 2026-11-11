package main

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"trickle/internal/bt/dht"
	"trickle/internal/bt/download"
	"trickle/internal/bt/torrentfile"
	"trickle/internal/bt/tracker"
)

const (
	trackerTimeout = 15 * time.Second
	dhtTimeout     = 30 * time.Second
	dhtWant        = 100
	refillEvery    = 2 * time.Minute

	dhtAnnounceEvery = 15 * time.Minute
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

func request(infoHash, peerID [20]byte, left int64, event tracker.Event) tracker.Request {
	return tracker.Request{InfoHash: infoHash, PeerID: peerID, Port: listenPort, Left: left, Event: event}
}

type progressStats struct {
	done       atomic.Int64
	downloaded atomic.Int64
	uploaded   atomic.Int64
}

func (s *progressStats) store(p download.Progress) {
	s.done.Store(int64(p.Done))
	s.downloaded.Store(p.Downloaded)
	s.uploaded.Store(p.Uploaded)
}

func (s *progressStats) request(tf torrentfile.File, peerID [20]byte) tracker.Request {
	req := request(tf.InfoHash, peerID, max(tf.Length-s.done.Load()*tf.PieceLength, 0), tracker.EventNone)
	req.Downloaded = s.downloaded.Load()
	req.Uploaded = s.uploaded.Load()
	return req
}

func announceAll(ctx context.Context, trackers []string, req tracker.Request) ([]netip.AddrPort, int) {
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
			res, err := tracker.Announce(ctx, http.DefaultClient, tr, req)
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

// a fresh context: by the time we say goodbye the download context is usually cancelled
func farewell(trackers []string, req tracker.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	announceAll(ctx, trackers, req)
}

func gatherPeers(ctx context.Context, trackers []string, req tracker.Request) []netip.AddrPort {
	var set peerSet
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		found, _ := announceAll(ctx, trackers, req)
		set.add(found)
	}()
	go func() {
		defer wg.Done()
		if found, err := lookupDHT(ctx, req.InfoHash, dhtWant); err == nil {
			set.add(found)
		}
	}()
	wg.Wait()
	return set.peers
}

func refill(ctx context.Context, trackers []string, current func() tracker.Request) <-chan []netip.AddrPort {
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
			found := gatherPeers(ctx, trackers, current())
			select {
			case more <- found:
			case <-ctx.Done():
				return
			}
		}
	}()
	return more
}

func withDHT[T any](ctx context.Context, fn func(context.Context, *dht.Client, []netip.AddrPort) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, dhtTimeout)
	defer cancel()
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return zero, err
	}
	client, err := dht.NewClient(conn)
	if err != nil {
		conn.Close()
		return zero, err
	}
	defer client.Close()
	return fn(ctx, client, resolve(ctx, bootstrapNodes))
}

func lookupDHT(ctx context.Context, infoHash [20]byte, want int) ([]netip.AddrPort, error) {
	return withDHT(ctx, func(ctx context.Context, c *dht.Client, boot []netip.AddrPort) ([]netip.AddrPort, error) {
		return c.FindPeers(ctx, infoHash, boot, want)
	})
}

func announceDHT(ctx context.Context, infoHash [20]byte) (int, error) {
	return withDHT(ctx, func(ctx context.Context, c *dht.Client, boot []netip.AddrPort) (int, error) {
		return c.Announce(ctx, infoHash, boot, listenPort)
	})
}

func keepAnnouncing(ctx context.Context, infoHash [20]byte, report func(int, error)) {
	for {
		accepted, err := announceDHT(ctx, infoHash)
		if report != nil {
			report(accepted, err)
		}
		select {
		case <-time.After(dhtAnnounceEvery):
		case <-ctx.Done():
			return
		}
	}
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
