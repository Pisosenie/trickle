package download

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"trickle/internal/bt/peer"
	"trickle/internal/bt/storage"
	"trickle/internal/bt/torrentfile"
)

const (
	dialTimeout   = 5 * time.Second
	unchokeWait   = 30 * time.Second
	idleTimeout   = 2 * time.Minute
	idleAfterMiss = time.Second
	maxRequest    = 128 << 10
)

type Config struct {
	Torrent       torrentfile.File
	PeerID        [20]byte
	Peers         []netip.AddrPort
	Dir           string
	MaxPeers      int
	Listener      net.Listener
	DownloadLimit int64
	UploadLimit   int64
	Progress      func(done, total int)
}

type session struct {
	cfg   Config
	tf    torrentfile.File
	store *storage.Storage
	down  *rate.Limiter
	up    *rate.Limiter

	mu   sync.Mutex
	have peer.Bitfield
	done int
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
		cfg:   cfg,
		tf:    cfg.Torrent,
		store: store,
		down:  limiter(cfg.DownloadLimit),
		up:    limiter(cfg.UploadLimit),
		have:  make(peer.Bitfield, (len(verified)+7)/8),
	}
	for i, ok := range verified {
		if ok {
			s.have = s.have.With(i)
			s.done++
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

func (s *session) markHave(index int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have.Has(index) {
		s.have = s.have.With(index)
		s.done++
	}
	return s.done
}

func (s *session) progress() {
	if s.cfg.Progress != nil {
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		s.cfg.Progress(done, len(s.tf.Pieces))
	}
}

func Download(ctx context.Context, cfg Config) error {
	s, err := open(cfg)
	if err != nil {
		return err
	}
	defer s.store.Close()
	s.progress()

	missing := s.missing()
	if len(missing) == 0 {
		return s.store.Sync()
	}

	ctx, cancel := context.WithCancel(ctx)
	var serving sync.WaitGroup
	defer serving.Wait()
	defer cancel()
	s.listen(ctx, &serving)

	queue := make(chan work, len(missing))
	for _, w := range missing {
		queue <- w
	}
	results := make(chan result)
	allGone := s.startWorkers(ctx, queue, results)

	for left := len(missing); left > 0; {
		select {
		case r := <-results:
			if err := s.store.WriteAt(r.data, int64(r.index)*s.tf.PieceLength); err != nil {
				return fmt.Errorf("write piece %d: %w", r.index, err)
			}
			s.markHave(r.index)
			s.progress()
			left--
		case <-allGone:
			return fmt.Errorf("all peers are gone with %d of %d pieces missing", left, len(s.tf.Pieces))
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
	if missing := len(s.missing()); missing > 0 {
		return fmt.Errorf("%d of %d pieces are missing or damaged, download them first", missing, len(s.tf.Pieces))
	}
	var serving sync.WaitGroup
	s.listen(ctx, &serving)
	<-ctx.Done()
	serving.Wait()
	return nil
}

func (s *session) missing() []work {
	var todo []work
	for i, hash := range s.tf.Pieces {
		if !s.has(i) {
			todo = append(todo, work{index: i, hash: hash, length: int(s.tf.PieceSize(i))})
		}
	}
	return todo
}

func (s *session) startWorkers(ctx context.Context, queue chan work, results chan<- result) <-chan struct{} {
	peers := s.cfg.Peers
	if s.cfg.MaxPeers > 0 {
		peers = peers[:min(len(peers), s.cfg.MaxPeers)]
	}
	var wg sync.WaitGroup
	for _, addr := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx, addr, queue, results)
		}()
	}
	allGone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allGone)
	}()
	return allGone
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
			serving.Add(1)
			go func() {
				defer serving.Done()
				s.serveIncoming(ctx, nc)
			}()
		}
	}()
}
