package download

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"trickle/internal/bt/peer"
	"trickle/internal/bt/storage"
	"trickle/internal/bt/torrentfile"
)

const (
	dialTimeout = 5 * time.Second
	unchokeWait = 30 * time.Second
	idleTimeout = 2 * time.Minute
	maxRequest  = 128 << 10
	maxFetchers = 2
)

type Config struct {
	Torrent       torrentfile.File
	PeerID        [20]byte
	Peers         []netip.AddrPort
	MorePeers     <-chan []netip.AddrPort
	Dir           string
	MaxPeers      int
	Listener      net.Listener
	DownloadLimit int64
	UploadLimit   int64
	Progress      func(Progress)
}

type Progress struct {
	Done       int
	Total      int
	Downloaded int64
	Uploaded   int64
}

type session struct {
	cfg   Config
	tf    torrentfile.File
	store *storage.Storage
	down  *rate.Limiter
	up    *rate.Limiter

	// set before any link starts; nil while only seeding
	results chan result
	exits   chan bool
	found   chan []netip.AddrPort

	pexEvery time.Duration

	mu       sync.Mutex
	have     peer.Bitfield
	done     int
	pending  map[int]work
	fetching map[int]work
	fetchers map[int]int
	links    map[*link]struct{}

	incoming   atomic.Int32
	downloaded atomic.Int64
	uploaded   atomic.Int64
}

func open(cfg Config) (*session, error) {
	store, err := storage.Open(cfg.Dir, cfg.Torrent)
	if err != nil {
		return nil, err
	}
	verified, err := store.Verify(cfg.Torrent)
	if err != nil {
		store.Close()
		return nil, err
	}
	s := &session{
		cfg:      cfg,
		tf:       cfg.Torrent,
		store:    store,
		down:     limiter(cfg.DownloadLimit),
		up:       limiter(cfg.UploadLimit),
		pexEvery: pexInterval,
		have:     make(peer.Bitfield, (len(verified)+7)/8),
		pending:  map[int]work{},
		fetching: map[int]work{},
		fetchers: map[int]int{},
		links:    map[*link]struct{}{},
	}
	for i, ok := range verified {
		if ok {
			s.have = s.have.With(i)
			s.done++
		} else {
			s.pending[i] = work{index: i, hash: cfg.Torrent.Pieces[i], length: int(cfg.Torrent.PieceSize(i))}
		}
	}
	return s, nil
}

func limiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return rate.NewLimiter(rate.Inf, maxRequest)
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), max(int(bytesPerSec), maxRequest))
}

func (s *session) bitfield() peer.Bitfield {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.have)
}

func (s *session) has(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.have.Has(index)
}

func (s *session) markHave(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have.Has(index) {
		return false
	}
	s.have = s.have.With(index)
	s.done++
	delete(s.pending, index)
	return true
}

func (s *session) missing() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tf.Pieces) - s.done
}

// one connection per peer: a second one to the same peer id is refused
func (s *session) register(l *link) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for other := range s.links {
		if other.c.PeerID == l.c.PeerID {
			return false
		}
	}
	s.links[l] = struct{}{}
	return true
}

func (s *session) dropLink(l *link) {
	s.mu.Lock()
	delete(s.links, l)
	s.mu.Unlock()
	close(l.outbox)
}

// a peer that is too slow to take a have just misses it; it is advisory
func (s *session) announceHave(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for l := range s.links {
		select {
		case l.outbox <- peer.NewHave(index):
		default:
		}
	}
}

func (s *session) progress() {
	if s.cfg.Progress == nil {
		return
	}
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	s.cfg.Progress(Progress{
		Done:       done,
		Total:      len(s.tf.Pieces),
		Downloaded: s.downloaded.Load(),
		Uploaded:   s.uploaded.Load(),
	})
}

func Download(ctx context.Context, cfg Config) error {
	s, err := open(cfg)
	if err != nil {
		return err
	}
	defer s.store.Close()
	s.progress()
	if s.missing() == 0 {
		return s.store.Sync()
	}

	s.results = make(chan result)
	s.exits = make(chan bool)
	s.found = make(chan []netip.AddrPort, 16)
	ctx, cancel := context.WithCancel(ctx)
	var serving, workers sync.WaitGroup
	defer serving.Wait()
	defer workers.Wait()
	defer cancel()
	s.listen(ctx, &serving)

	peers := newPool(cfg.MaxPeers)
	launch := func(addrs []netip.AddrPort) {
		for _, addr := range peers.admit(time.Now(), addrs) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				s.dialPeer(ctx, addr)
			}()
		}
	}
	launch(cfg.Peers)

	more := cfg.MorePeers
	for s.missing() > 0 {
		if peers.active == 0 && s.incoming.Load() == 0 && more == nil {
			return fmt.Errorf("all peers are gone with %d of %d pieces missing", s.missing(), len(s.tf.Pieces))
		}
		select {
		case r := <-s.results:
			if s.has(r.index) {
				continue
			}
			if err := s.store.WriteAt(r.data, int64(r.index)*s.tf.PieceLength); err != nil {
				return fmt.Errorf("write piece %d: %w", r.index, err)
			}
			s.markHave(r.index)
			s.downloaded.Add(int64(len(r.data)))
			s.announceHave(r.index)
			s.progress()
		case addrs := <-s.found:
			launch(addrs)
		case outgoing := <-s.exits:
			if outgoing {
				peers.exited()
				launch(nil)
			}
		case addrs, ok := <-more:
			if !ok {
				more = nil
				continue
			}
			launch(addrs)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.store.Sync()
}

func Seed(ctx context.Context, cfg Config) error {
	if cfg.Listener == nil {
		return fmt.Errorf("seeding needs a listener")
	}
	s, err := open(cfg)
	if err != nil {
		return err
	}
	defer s.store.Close()
	if missing := s.missing(); missing > 0 {
		return fmt.Errorf("%d of %d pieces are missing or damaged, download them first", missing, len(s.tf.Pieces))
	}
	var serving sync.WaitGroup
	s.listen(ctx, &serving)
	<-ctx.Done()
	serving.Wait()
	return nil
}

func (s *session) listen(ctx context.Context, serving *sync.WaitGroup) {
	ln := s.cfg.Listener
	if ln == nil {
		return
	}
	serving.Add(1)
	go func() {
		defer serving.Done()
		stop := context.AfterFunc(ctx, func() { ln.Close() })
		defer stop()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.incoming.Add(1)
			serving.Add(1)
			go func() {
				defer serving.Done()
				s.acceptPeer(ctx, nc)
			}()
		}
	}()
}

func (s *session) exited(ctx context.Context, outgoing bool) {
	if !outgoing {
		s.incoming.Add(-1)
	}
	if s.exits == nil {
		return
	}
	select {
	case s.exits <- outgoing:
	case <-ctx.Done():
	}
}
