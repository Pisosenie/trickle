package download

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"time"

	"trickle/internal/bt/peer"
)

const (
	blockSize    = 16 << 10
	maxBacklog   = 5
	pieceTimeout = 30 * time.Second
)

var (
	errHashMismatch = errors.New("piece failed SHA-1 check")
	errAlreadyHave  = errors.New("piece finished by another peer")
)

type work struct {
	index     int
	hash      [20]byte
	length    int
	duplicate bool
}

type result struct {
	index int
	data  []byte
}

func fetchPiece(ctx context.Context, l *link, w work) ([]byte, error) {
	c := l.c
	c.SetDeadline(time.Now().Add(pieceTimeout))
	defer c.SetDeadline(time.Time{})

	blocks := (w.length + blockSize - 1) / blockSize
	received := make([]bool, blocks)
	inFlight := make([]bool, blocks)
	left, backlog := blocks, 0
	buf := make([]byte, w.length)

	for left > 0 {
		if l.s.has(w.index) {
			cancelInFlight(c, w, inFlight)
			return nil, errAlreadyHave
		}
		if !c.Choked {
			for i := 0; i < blocks && backlog < maxBacklog; i++ {
				if received[i] || inFlight[i] {
					continue
				}
				begin := i * blockSize
				if err := c.Send(peer.NewRequest(w.index, begin, min(blockSize, w.length-begin))); err != nil {
					return nil, err
				}
				inFlight[i] = true
				backlog++
			}
		}

		m, err := l.read(ctx)
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue
		}
		switch m.ID {
		case peer.Choke:
			// a choking peer drops our pending requests; ask again after unchoke
			clear(inFlight)
			backlog = 0
		case peer.Piece:
			block, err := peer.ParsePiece(m)
			if err != nil {
				return nil, err
			}
			if block.Index != w.index {
				continue
			}
			i := block.Begin / blockSize
			if block.Begin%blockSize != 0 || i >= blocks || len(block.Data) != min(blockSize, w.length-block.Begin) {
				return nil, fmt.Errorf("%w: unexpected block %d@%d", peer.ErrProtocol, block.Index, block.Begin)
			}
			if received[i] {
				continue
			}
			copy(buf[block.Begin:], block.Data)
			received[i] = true
			left--
			if inFlight[i] {
				inFlight[i] = false
				backlog--
			}
			if err := l.s.down.WaitN(ctx, len(block.Data)); err != nil {
				return nil, err
			}
		}
	}

	if sha1.Sum(buf) != w.hash {
		return nil, errHashMismatch
	}
	return buf, nil
}

func cancelInFlight(c *peer.Conn, w work, inFlight []bool) {
	for i, pending := range inFlight {
		if pending {
			begin := i * blockSize
			c.Send(peer.NewCancel(w.index, begin, min(blockSize, w.length-begin)))
		}
	}
}
