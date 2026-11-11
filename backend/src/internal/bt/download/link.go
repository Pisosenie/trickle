package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"trickle/internal/bt/peer"
)

var errDuplicate = errors.New("already connected to this peer")

const (
	outboxSize = 128
	tick       = 250 * time.Millisecond
	keepAlive  = 90 * time.Second
)

type link struct {
	c          *peer.Conn
	s          *session
	outgoing   bool
	addr       netip.AddrPort
	theirPex   byte
	lastPex    time.Time
	shared     map[netip.AddrPort]bool
	choking    bool
	interested bool
	refused    peer.Bitfield
	outbox     chan *peer.Message
	lastSent   atomic.Int64

	peerInterested atomic.Bool
	wantUnchoke    atomic.Bool
	received       atomic.Int64
	sent           atomic.Int64
}

// register before the bitfield snapshot; queued haves wait until the bitfield is sent
func (s *session) newLink(c *peer.Conn, outgoing bool, addr netip.AddrPort) (*link, error) {
	l := &link{
		c:        c,
		s:        s,
		outgoing: outgoing,
		addr:     addr,
		shared:   map[netip.AddrPort]bool{},
		choking:  true,
		outbox:   make(chan *peer.Message, outboxSize),
	}
	l.lastSent.Store(time.Now().UnixNano())
	if !s.register(l) {
		return nil, errDuplicate
	}
	if m := s.announcement(c.Fast); m != nil {
		if err := l.send(m); err != nil {
			s.dropLink(l)
			return nil, err
		}
	}
	if c.Extensions {
		if err := l.send(&peer.Message{ID: peer.Extended, Payload: s.extHandshake()}); err != nil {
			s.dropLink(l)
			return nil, err
		}
	}
	go l.drain()
	return l, nil
}

func (l *link) send(m *peer.Message) error {
	l.lastSent.Store(time.Now().UnixNano())
	return l.c.Send(m)
}

func (l *link) drain() {
	for m := range l.outbox {
		if l.send(m) != nil {
			return
		}
	}
}

func (s *session) dialPeer(ctx context.Context, addr netip.AddrPort) {
	defer s.exited(ctx, true)
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	c, err := peer.Dial(dialCtx, addr, s.tf.InfoHash, s.cfg.PeerID)
	cancel()
	if err != nil {
		return
	}
	s.runLink(ctx, c, true, addr)
}

func (s *session) acceptPeer(ctx context.Context, nc net.Conn) {
	defer s.exited(ctx, false)
	hsCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	c, err := peer.Accept(hsCtx, nc, s.tf.InfoHash, s.cfg.PeerID)
	cancel()
	if err != nil {
		nc.Close()
		return
	}
	s.runLink(ctx, c, false, netip.AddrPort{})
}

// runLink reads the socket on its own goroutine, so the loop can serve, download and keep time at once
func (s *session) runLink(ctx context.Context, c *peer.Conn, outgoing bool, addr netip.AddrPort) {
	defer c.Close()
	if c.PeerID == s.cfg.PeerID {
		return
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	l, err := s.newLink(c, outgoing, addr)
	if err != nil {
		return
	}
	defer s.dropLink(l)

	msgs := make(chan *peer.Message)
	gone := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(gone)
		for {
			m, err := c.Receive()
			if err != nil {
				return
			}
			if m == nil {
				continue
			}
			select {
			case msgs <- m:
			case <-done:
				return
			}
		}
	}()
	l.loop(ctx, msgs, gone)
}

func (l *link) loop(ctx context.Context, msgs <-chan *peer.Message, gone <-chan struct{}) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var cur *pieceProgress
	defer func() {
		if cur != nil {
			l.s.release(cur.w, !cur.w.duplicate)
		}
	}()
	downloading := l.s.results != nil
	lastHeard := time.Now()
	var chokedSince time.Time

	for {
		if l.syncChoke() != nil {
			return
		}
		if downloading {
			if l.updateInterest() != nil {
				return
			}
			if cur == nil && l.interested && !l.c.Choked {
				if w, ok := l.s.assign(l.theirs()); ok {
					cur = newProgress(w, time.Now())
				}
			}
			if cur != nil && !l.c.Choked && cur.request(l.send) != nil {
				return
			}
		}

		select {
		case m := <-msgs:
			lastHeard = time.Now()
			if l.c.Apply(m) != nil || l.handle(ctx, m) != nil {
				return
			}
			if cur == nil {
				continue
			}
			switch m.ID {
			case peer.Choke:
				if !l.c.Fast {
					cur.choked()
				}
			case peer.Reject:
				req, err := peer.ParseRequest(m)
				if err != nil {
					return
				}
				if !cur.rejected(req) || l.c.Choked {
					continue
				}
				if cur.refusals++; cur.refusals > maxRefusals {
					cur.cancel(l.send)
					l.refused = l.refused.With(cur.w.index)
					l.s.release(cur.w, !cur.w.duplicate)
					cur = nil
				}
			case peer.Piece:
				block, err := peer.ParsePiece(m)
				if err != nil {
					return
				}
				l.received.Add(int64(len(block.Data)))
				complete, err := cur.block(block, time.Now())
				if err != nil || cur.cancelLost(l.send) != nil || l.s.down.WaitN(ctx, len(block.Data)) != nil {
					return
				}
				if !complete {
					continue
				}
				finished := cur
				cur = nil
				data, err := finished.verify()
				l.s.release(finished.w, err != nil && !finished.w.duplicate)
				if err != nil {
					return
				}
				select {
				case l.s.results <- result{index: finished.w.index, data: data}:
				case <-ctx.Done():
					return
				}
			}

		case now := <-ticker.C:
			if cur != nil {
				cur.expire(now)
				if cur.cancelLost(l.send) != nil {
					return
				}
			}
			if cur != nil && l.s.has(cur.w.index) {
				cur.cancel(l.send)
				l.s.release(cur.w, false)
				cur = nil
			}
			if cur != nil && now.Sub(cur.lastData) > pieceTimeout || now.Sub(lastHeard) > idleTimeout {
				return
			}
			if l.outgoing && l.interested && l.c.Choked {
				if chokedSince.IsZero() {
					chokedSince = now
				} else if now.Sub(chokedSince) > unchokeWait {
					return
				}
			} else {
				chokedSince = time.Time{}
			}
			if l.theirPex != 0 && now.Sub(l.lastPex) >= l.s.pexEvery {
				l.lastPex = now
				if m := l.pexMessage(); m != nil && l.send(m) != nil {
					return
				}
			}
			if now.Sub(time.Unix(0, l.lastSent.Load())) > keepAlive && l.send(nil) != nil {
				return
			}

		case <-gone:
			return
		case <-ctx.Done():
			return
		}
	}
}

// the choker decides, the link tells the peer: only this goroutine writes choke state to the socket
func (l *link) syncChoke() error {
	want := l.wantUnchoke.Load()
	if want != l.choking {
		return nil
	}
	l.choking = !want
	id := peer.Choke
	if want {
		id = peer.Unchoke
	}
	return l.send(&peer.Message{ID: id})
}

func (l *link) theirs() peer.Bitfield {
	bf := l.c.Bitfield
	if l.c.All {
		bf = l.s.full
	}
	if l.refused == nil {
		return bf
	}
	return bf.Minus(l.refused)
}

func (l *link) updateInterest() error {
	want := l.s.wants(l.theirs())
	if want == l.interested {
		return nil
	}
	l.interested = want
	id := peer.NotInterested
	if want {
		id = peer.Interested
	}
	return l.send(&peer.Message{ID: id})
}

func (l *link) handle(ctx context.Context, m *peer.Message) error {
	switch m.ID {
	case peer.Interested:
		l.peerInterested.Store(true)
		l.s.offerSlot(l)
		return l.syncChoke()
	case peer.NotInterested:
		l.peerInterested.Store(false)
		l.wantUnchoke.Store(false)
		return l.syncChoke()
	case peer.Request:
		return l.serve(ctx, m)
	case peer.Extended:
		l.extended(m)
	}
	return nil
}

func (l *link) serve(ctx context.Context, m *peer.Message) error {
	req, err := peer.ParseRequest(m)
	if err != nil {
		return err
	}
	tf := l.s.tf
	if req.Index < 0 || req.Index >= len(tf.Pieces) || req.Length <= 0 || req.Length > maxRequest ||
		req.Begin < 0 || int64(req.Begin+req.Length) > tf.PieceSize(req.Index) {
		return fmt.Errorf("%w: bad request for %d@%d+%d", peer.ErrProtocol, req.Index, req.Begin, req.Length)
	}
	if l.choking || !l.s.has(req.Index) {
		return l.refuse(req)
	}
	data := make([]byte, req.Length)
	if err := l.s.store.ReadAt(data, int64(req.Index)*tf.PieceLength+int64(req.Begin)); err != nil {
		return err
	}
	if err := l.s.up.WaitN(ctx, len(data)); err != nil {
		return err
	}
	l.s.uploaded.Add(int64(len(data)))
	l.sent.Add(int64(len(data)))
	return l.send(peer.NewPiece(req.Index, req.Begin, data))
}

// without the fast extension a choked request is dropped silently and one for a missing piece is a violation
func (l *link) refuse(req peer.BlockRequest) error {
	switch {
	case l.c.Fast:
		return l.send(peer.NewReject(req.Index, req.Begin, req.Length))
	case l.choking:
		return nil
	}
	return fmt.Errorf("%w: request for missing piece %d", peer.ErrProtocol, req.Index)
}
