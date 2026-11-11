package download

import (
	"testing"
	"time"

	"trickle/internal/bt/peer"
)

func TestLostRequestsAreAskedAgain(t *testing.T) {
	w := work{index: 7, length: 4 * blockSize}
	p := newProgress(w, time.Unix(0, 0))
	var asked []int
	send := func(m *peer.Message) error {
		req, _ := peer.ParseRequest(m)
		asked = append(asked, req.Begin/blockSize)
		return nil
	}
	if err := p.request(send); err != nil || len(asked) != 4 {
		t.Fatalf("asked %v, %v", asked, err)
	}
	t0 := time.Unix(100, 0)
	for i := range p.sentAt {
		p.sentAt[i] = t0.Add(time.Duration(i) * 100 * time.Millisecond)
	}

	block := peer.Block{Index: 7, Begin: 2 * blockSize, Data: make([]byte, blockSize)}
	if done, err := p.block(block, t0.Add(2*time.Second)); done || err != nil {
		t.Fatalf("block: done=%v err=%v", done, err)
	}
	if p.inFlight[0] || p.inFlight[1] || !p.inFlight[3] || p.backlog != 1 {
		t.Fatalf("blocks 0 and 1 were overtaken, 3 was asked later: inFlight=%v backlog=%d", p.inFlight, p.backlog)
	}

	asked = nil
	p.request(send)
	if len(asked) != 2 || asked[0] != 0 || asked[1] != 1 {
		t.Fatalf("expected blocks 0 and 1 again, asked %v", asked)
	}

	p.sentAt[3] = t0
	p.expire(t0.Add(requestTimeout + time.Second))
	if p.inFlight[3] {
		t.Fatal("a request older than requestTimeout must be dropped")
	}
}

func TestStaleBlocksAreIgnored(t *testing.T) {
	p := newProgress(work{index: 1, length: blockSize}, time.Now())
	if done, err := p.block(peer.Block{Index: 9, Begin: 0, Data: []byte("x")}, time.Now()); done || err != nil {
		t.Fatalf("a block of another piece must be ignored: done=%v err=%v", done, err)
	}
	if _, err := p.block(peer.Block{Index: 1, Begin: 3, Data: []byte("x")}, time.Now()); err == nil {
		t.Fatal("a misaligned block of this piece is a protocol error")
	}
}
