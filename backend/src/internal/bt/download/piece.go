package download

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"time"

	"trickle/internal/bt/peer"
)

const (
	blockSize      = 16 << 10
	maxBacklog     = 5
	pieceTimeout   = 30 * time.Second
	overtakenAfter = time.Second
	requestTimeout = 15 * time.Second
	maxRefusals    = 3
)

var errHashMismatch = errors.New("piece failed SHA-1 check")

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

type pieceProgress struct {
	w         work
	buf       []byte
	received  []bool
	inFlight  []bool
	sentAt    []time.Time
	lost      []int
	cancelled []int
	refusals  int
	left      int
	backlog   int
	lastData  time.Time
}

func newProgress(w work, now time.Time) *pieceProgress {
	blocks := (w.length + blockSize - 1) / blockSize
	return &pieceProgress{
		w:         w,
		buf:       make([]byte, w.length),
		received:  make([]bool, blocks),
		inFlight:  make([]bool, blocks),
		sentAt:    make([]time.Time, blocks),
		cancelled: make([]int, blocks),
		left:      blocks,
		lastData:  now,
	}
}

func (p *pieceProgress) blockSize(i int) int {
	return min(blockSize, p.w.length-i*blockSize)
}

func (p *pieceProgress) request(send func(*peer.Message) error) error {
	for i := range p.received {
		if p.backlog >= maxBacklog {
			return nil
		}
		if p.received[i] || p.inFlight[i] {
			continue
		}
		if err := send(peer.NewRequest(p.w.index, i*blockSize, p.blockSize(i))); err != nil {
			return err
		}
		p.inFlight[i] = true
		p.sentAt[i] = time.Now()
		p.backlog++
	}
	return nil
}

// a choking peer drops our pending requests; they are asked again after unchoke
func (p *pieceProgress) choked() {
	clear(p.inFlight)
	p.backlog = 0
}

// blocks of other pieces are leftovers of a cancelled piece and are ignored
func (p *pieceProgress) block(b peer.Block, now time.Time) (bool, error) {
	if b.Index != p.w.index {
		return false, nil
	}
	i := b.Begin / blockSize
	if b.Begin%blockSize != 0 || i >= len(p.received) || len(b.Data) != p.blockSize(i) {
		return false, fmt.Errorf("%w: unexpected block %d@%d", peer.ErrProtocol, b.Index, b.Begin)
	}
	if p.inFlight[i] {
		p.inFlight[i] = false
		p.backlog--
		p.overtaken(p.sentAt[i], now)
	}
	if !p.received[i] {
		copy(p.buf[b.Begin:], b.Data)
		p.received[i] = true
		p.left--
		p.lastData = now
	}
	return p.left == 0, nil
}

// peers answer requests in order, so a block asked for before one that just arrived was probably lost
func (p *pieceProgress) overtaken(arrivedSentAt, now time.Time) {
	for j, pending := range p.inFlight {
		if pending && p.sentAt[j].Before(arrivedSentAt) && now.Sub(p.sentAt[j]) > overtakenAfter {
			p.markLost(j)
		}
	}
}

func (p *pieceProgress) expire(now time.Time) {
	for j, pending := range p.inFlight {
		if pending && now.Sub(p.sentAt[j]) > requestTimeout {
			p.markLost(j)
		}
	}
}

func (p *pieceProgress) markLost(i int) {
	p.inFlight[i] = false
	p.backlog--
	p.lost = append(p.lost, i)
}

// a peer may keep a stuck request and drop a repeat as a duplicate, so cancel it first
func (p *pieceProgress) cancelLost(send func(*peer.Message) error) error {
	for _, i := range p.lost {
		if err := send(peer.NewCancel(p.w.index, i*blockSize, p.blockSize(i))); err != nil {
			return err
		}
		p.cancelled[i]++
	}
	p.lost = nil
	return nil
}

// a fast peer answers our cancel with a reject too; that one must not free the block asked for again
func (p *pieceProgress) rejected(r peer.BlockRequest) bool {
	i := r.Begin / blockSize
	if r.Index != p.w.index || r.Begin%blockSize != 0 || i < 0 || i >= len(p.inFlight) {
		return false
	}
	if p.cancelled[i] > 0 {
		p.cancelled[i]--
		return false
	}
	if !p.inFlight[i] {
		return false
	}
	p.inFlight[i] = false
	p.backlog--
	return true
}

func (p *pieceProgress) cancel(send func(*peer.Message) error) {
	for i, pending := range p.inFlight {
		if pending {
			send(peer.NewCancel(p.w.index, i*blockSize, p.blockSize(i)))
		}
	}
}

func (p *pieceProgress) verify() ([]byte, error) {
	if sha1.Sum(p.buf) != p.w.hash {
		return nil, errHashMismatch
	}
	return p.buf, nil
}
