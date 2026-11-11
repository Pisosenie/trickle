package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"trickle/internal/bt/peer"
)

const outboxSize = 128

type link struct {
	c       *peer.Conn
	s       *session
	choking bool
	outbox  chan *peer.Message
}

// register before the bitfield snapshot; queued haves wait until the bitfield is sent
func (s *session) newLink(c *peer.Conn) (*link, error) {
	l := &link{c: c, s: s, choking: true, outbox: make(chan *peer.Message, outboxSize)}
	s.register(l)
	if bf := s.bitfield(); bf.Count() > 0 {
		if err := c.Send(&peer.Message{ID: peer.BitfieldMsg, Payload: bf}); err != nil {
			s.dropLink(l)
			return nil, err
		}
	}
	go l.drain()
	return l, nil
}

func (l *link) drain() {
	for m := range l.outbox {
		if l.c.Send(m) != nil {
			return
		}
	}
}

func (l *link) read(ctx context.Context) (*peer.Message, error) {
	m, err := l.c.Read()
	if err != nil || m == nil {
		return m, err
	}
	switch m.ID {
	case peer.Interested:
		if l.choking {
			l.choking = false
			return m, l.c.Send(&peer.Message{ID: peer.Unchoke})
		}
	case peer.NotInterested:
		if !l.choking {
			l.choking = true
			return m, l.c.Send(&peer.Message{ID: peer.Choke})
		}
	case peer.Request:
		return m, l.serve(ctx, m)
	}
	return m, nil
}

func (l *link) serve(ctx context.Context, m *peer.Message) error {
	if l.choking {
		return nil
	}
	req, err := peer.ParseRequest(m)
	if err != nil {
		return err
	}
	tf := l.s.tf
	if req.Index < 0 || req.Index >= len(tf.Pieces) || !l.s.has(req.Index) ||
		req.Length <= 0 || req.Length > maxRequest || req.Begin < 0 ||
		int64(req.Begin+req.Length) > tf.PieceSize(req.Index) {
		return fmt.Errorf("%w: bad request for %d@%d+%d", peer.ErrProtocol, req.Index, req.Begin, req.Length)
	}
	data := make([]byte, req.Length)
	if err := l.s.store.ReadAt(data, int64(req.Index)*tf.PieceLength+int64(req.Begin)); err != nil {
		return err
	}
	if err := l.s.up.WaitN(ctx, len(data)); err != nil {
		return err
	}
	l.s.uploaded.Add(int64(len(data)))
	return l.c.Send(peer.NewPiece(req.Index, req.Begin, data))
}

func (s *session) worker(ctx context.Context, addr netip.AddrPort, queue chan work, results chan<- result) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	c, err := peer.Dial(dialCtx, addr, s.tf.InfoHash, s.cfg.PeerID)
	cancel()
	if err != nil {
		return
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	l, err := s.newLink(c)
	if err != nil {
		return
	}
	defer s.dropLink(l)
	if c.Send(&peer.Message{ID: peer.Interested}) != nil {
		return
	}
	c.SetDeadline(time.Now().Add(unchokeWait))
	for c.Choked {
		if _, err := l.read(ctx); err != nil {
			return
		}
	}
	c.SetDeadline(time.Time{})

	misses := 0
	for {
		w, ok := s.next(ctx, queue, c.Bitfield)
		if !ok {
			return
		}
		if !c.Bitfield.Has(w.index) {
			queue <- w
			if misses++; misses >= cap(queue) {
				misses = 0
				if !sleep(ctx, idleAfterMiss) {
					return
				}
			}
			continue
		}
		misses = 0

		s.startFetch(w)
		data, err := fetchPiece(ctx, l, w)
		s.endFetch(w.index)
		if errors.Is(err, errAlreadyHave) {
			continue
		}
		if err != nil {
			if !w.duplicate {
				queue <- w
			}
			return
		}
		select {
		case results <- result{index: w.index, data: data}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *session) serveIncoming(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	hsCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	c, err := peer.Accept(hsCtx, nc, s.tf.InfoHash, s.cfg.PeerID)
	cancel()
	if err != nil {
		return
	}
	l, err := s.newLink(c)
	if err != nil {
		return
	}
	defer s.dropLink(l)
	for {
		c.SetDeadline(time.Now().Add(idleTimeout))
		if _, err := l.read(ctx); err != nil {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
