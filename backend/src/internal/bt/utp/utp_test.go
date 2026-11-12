package utp

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestPacketRoundTrip(t *testing.T) {
	in := packet{typ: stData, connID: 7, timestamp: 1, tsDiff: 2, wnd: 3, seq: 65535, ack: 9, sack: []byte{0b101, 0, 0, 0x80}, payload: []byte("hi")}
	out, err := parsePacket(in.marshal())
	if err != nil {
		t.Fatal(err)
	}
	if out.typ != in.typ || out.connID != 7 || out.timestamp != 1 || out.tsDiff != 2 || out.wnd != 3 ||
		out.seq != 65535 || out.ack != 9 || !bytes.Equal(out.sack, in.sack) || string(out.payload) != "hi" {
		t.Fatalf("got %+v", out)
	}
	var sacked []uint16
	for seq := range uint16(64) {
		if out.sacks(seq) {
			sacked = append(sacked, seq)
		}
	}
	if !slices.Equal(sacked, []uint16{11, 13, 42}) {
		t.Fatalf("sacked %v", sacked)
	}
}

func TestParseSkipsUnknownExtensions(t *testing.T) {
	b := packet{typ: stSyn, seq: 1}.marshal()
	b[1] = 2
	b = append(b, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8)
	b = append(b, "data"...)
	p, err := parsePacket(b)
	if err != nil || p.sack != nil || string(p.payload) != "data" {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, bad := range [][]byte{nil, b[:19], append([]byte{0x02}, b[1:]...), append(slices.Clone(b[:20]), 9)} {
		if _, err := parsePacket(bad); err == nil {
			t.Errorf("accepted %x", bad)
		}
	}
	if seqLess(65535, 0) == false || seqLess(0, 65535) {
		t.Fatal("sequence numbers must compare across the wrap")
	}
}

func TestLedbatYieldsToQueueing(t *testing.T) {
	now := time.Now()
	l := newLedbat()
	l.onAck(50_000, maxPayload, now)
	start := l.window
	for range 20 {
		l.onAck(50_000+10_000, maxPayload, now)
	}
	if l.window <= start {
		t.Fatalf("10ms of queueing is under target, window should grow: %d -> %d", start, l.window)
	}
	grown := l.window
	for range 20 {
		l.onAck(50_000+400_000, maxPayload, now)
	}
	if l.window >= grown {
		t.Fatalf("400ms of queueing is over target, window should shrink: %d -> %d", grown, l.window)
	}
	l.onTimeout()
	if l.window != maxPayload {
		t.Fatalf("a timeout leaves one packet, got %d", l.window)
	}
}

type lossy struct {
	net.PacketConn
	mu    sync.Mutex
	rng   *mrand.Rand
	drop  float64
	delay float64
}

func (l *lossy) WriteTo(b []byte, addr net.Addr) (int, error) {
	l.mu.Lock()
	r := l.rng.Float64()
	l.mu.Unlock()
	switch {
	case r < l.drop:
		return len(b), nil
	case r < l.drop+l.delay:
		late := slices.Clone(b)
		time.AfterFunc(5*time.Millisecond, func() { l.PacketConn.WriteTo(late, addr) })
		return len(b), nil
	}
	return l.PacketConn.WriteTo(b, addr)
}

func socket(t *testing.T, drop, delay float64) *Socket {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if drop > 0 || delay > 0 {
		pc = &lossy{PacketConn: pc, rng: mrand.New(mrand.NewPCG(1, 2)), drop: drop, delay: delay}
	}
	s := NewSocket(pc)
	t.Cleanup(func() { s.Close() })
	return s
}

func addrOf(t *testing.T, a net.Addr) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func connect(t *testing.T, from, to *Socket) (net.Conn, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := to.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := from.Dial(ctx, addrOf(t, to.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case other := <-accepted:
		return c, other
	case <-ctx.Done():
		t.Fatal("nothing accepted")
	}
	return nil, nil
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// both ends write and read at once, like two peers trading pieces
func exchange(t *testing.T, x, y net.Conn, size int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	x.SetDeadline(deadline)
	y.SetDeadline(deadline)
	fromX, fromY := random(size), random(size)
	gotX, gotY := make([]byte, size), make([]byte, size)
	errs := make(chan error, 4)
	go func() { _, err := x.Write(fromX); errs <- err }()
	go func() { _, err := y.Write(fromY); errs <- err }()
	go func() { _, err := io.ReadFull(y, gotX); errs <- err }()
	go func() { _, err := io.ReadFull(x, gotY); errs <- err }()
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(gotX, fromX) || !bytes.Equal(gotY, fromY) {
		t.Fatal("data arrived mangled")
	}
}

func TestTransferBothWays(t *testing.T) {
	x, y := connect(t, socket(t, 0, 0), socket(t, 0, 0))
	exchange(t, x, y, 4<<20)
}

func TestTransferSurvivesLossAndReordering(t *testing.T) {
	x, y := connect(t, socket(t, 0.1, 0.1), socket(t, 0.1, 0.1))
	exchange(t, x, y, 1<<20)
}

func TestCloseEndsTheStream(t *testing.T) {
	x, y := connect(t, socket(t, 0, 0), socket(t, 0, 0))
	data := random(100 << 10)
	if _, err := x.Write(data); err != nil {
		t.Fatal(err)
	}
	x.Close()
	y.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(y)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d of %d bytes before the end: %v", len(got), len(data), err)
	}
	if _, err := x.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("reading a closed connection: %v", err)
	}
}

func TestReadDeadline(t *testing.T) {
	_, y := connect(t, socket(t, 0, 0), socket(t, 0, 0))
	y.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := y.Read(make([]byte, 1))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected a timeout, got %v", err)
	}
}

func TestDialGivesUpWithItsContext(t *testing.T) {
	gone := socket(t, 0, 0)
	addr := addrOf(t, gone.Addr())
	gone.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := socket(t, 0, 0).Dial(ctx, addr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the dial to end with its context, got %v", err)
	}
}

func TestStrayPacketGetsReset(t *testing.T) {
	s := socket(t, 0, 0)
	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.WriteTo(packet{typ: stData, connID: 777, seq: 5, payload: []byte("?")}.marshal(), s.Addr())
	raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := raw.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := parsePacket(buf[:n]); err != nil || p.typ != stReset || p.connID != 777 || p.ack != 5 {
		t.Fatalf("expected a reset, got %+v, %v", p, err)
	}
}

func TestAckForUnsentDataIsIgnored(t *testing.T) {
	c := newConn(&Socket{pc: discard{}}, netip.MustParseAddrPort("127.0.0.1:1"), 1, 2)
	c.connected, c.seq = true, 100
	c.queue(stData, []byte("x"))
	c.receive(packet{typ: stState, ack: 500})
	if len(c.outbox) != 1 || c.inflight != 1 {
		t.Fatalf("an ack past what we sent must not clear the outbox: %d packets, %d in flight", len(c.outbox), c.inflight)
	}
	c.receive(packet{typ: stState, ack: 100})
	if len(c.outbox) != 0 || c.inflight != 0 {
		t.Fatalf("the real ack should: %d packets, %d in flight", len(c.outbox), c.inflight)
	}
}

type discard struct{ net.PacketConn }

func (discard) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
