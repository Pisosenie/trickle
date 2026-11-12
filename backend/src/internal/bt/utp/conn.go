package utp

import (
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"time"
)

const (
	maxPayload  = 1200
	recvBuffer  = 1 << 20
	maxAhead    = 2048
	maxSack     = 32
	initialRTO  = time.Second
	minRTO      = 500 * time.Millisecond
	maxRTO      = 30 * time.Second
	maxRetries  = 6
	synRetries  = 3
	keepAliveIn = 29 * time.Second
)

var (
	ErrReset   = errors.New("utp: connection reset by peer")
	ErrTimeout = errors.New("utp: connection timed out")
)

type outPacket struct {
	p         packet
	sentAt    time.Time
	resends   int
	overtaken int
	lost      bool
}

// Conn is one uTP connection; it is a net.Conn, so the peer wire protocol runs over it unchanged
type Conn struct {
	s              *Socket
	remote         netip.AddrPort
	recvID, sendID uint16

	mu        sync.Mutex
	cond      *sync.Cond
	connected bool
	closed    bool
	err       error

	seq      uint16
	ack      uint16
	outbox   []*outPacket
	inflight int
	peerWnd  uint32
	recovery uint16
	retries  int
	rtt      time.Duration
	rttVar   time.Duration
	rto      time.Duration
	lastSent time.Time
	cc       ledbat

	inbuf    []byte
	ahead    map[uint16]packet
	aheadLen int
	gotFin   bool
	reply    uint32

	readDeadline, writeDeadline time.Time
}

func newConn(s *Socket, remote netip.AddrPort, recvID, sendID uint16) *Conn {
	c := &Conn{
		s:       s,
		remote:  remote,
		recvID:  recvID,
		sendID:  sendID,
		peerWnd: recvBuffer,
		rto:     initialRTO,
		ahead:   map[uint16]packet{},
		cc:      newLedbat(),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *Conn) connect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq = 1
	c.queue(stSyn, nil)
}

// the answer to a SYN carries the next sequence number we will use, not a used one
func (c *Conn) accepted(syn packet) {
	c.connected = true
	c.seq = uint16(rand.Uint32())
	c.ack = syn.seq
}

func (c *Conn) established() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for !c.connected && c.err == nil {
		c.cond.Wait()
	}
	return c.err
}

func (c *Conn) receive(p packet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	now := time.Now()
	c.reply = micros(now) - p.timestamp
	switch {
	case p.typ == stReset:
		c.failLocked(ErrReset)
		return
	case p.typ == stSyn:
		c.sendState()
		return
	case !c.connected:
		if p.typ != stState && p.typ != stData {
			return
		}
		c.connected = true
		c.ack = p.seq - 1
	}
	c.peerWnd = p.wnd
	c.acked(p, now)
	if p.typ == stData || p.typ == stFin {
		c.arrived(p)
	}
	c.flush(now)
	c.settle()
	c.cond.Broadcast()
}

func (c *Conn) arrived(p packet) {
	switch {
	case p.seq == c.ack+1:
		c.deliver(p)
		for next, ok := c.ahead[c.ack+1]; ok; next, ok = c.ahead[c.ack+1] {
			delete(c.ahead, next.seq)
			c.aheadLen -= len(next.payload)
			c.deliver(next)
		}
	case seqLess(c.ack, p.seq) && p.seq-c.ack < maxAhead:
		if _, dup := c.ahead[p.seq]; !dup {
			c.ahead[p.seq] = p
			c.aheadLen += len(p.payload)
		}
	}
	c.sendState()
}

// after a local close the data is still acknowledged but nobody reads it
func (c *Conn) deliver(p packet) {
	c.ack = p.seq
	if p.typ == stFin {
		c.gotFin = true
		return
	}
	if !c.closed {
		c.inbuf = append(c.inbuf, p.payload...)
	}
}

// a packet is lost once three packets sent after it are acknowledged, first send or resend alike;
// the window halves once per round of losses, not once per lost packet
func (c *Conn) acked(p packet, now time.Time) {
	if seqLess(c.seq-1, p.ack) {
		return
	}
	var done []*outPacket
	c.outbox = slices.DeleteFunc(c.outbox, func(op *outPacket) bool {
		if seqLess(p.ack, op.p.seq) && !p.sacks(op.p.seq) {
			return false
		}
		done = append(done, op)
		return true
	})
	if len(done) == 0 {
		return
	}
	bytes := 0
	for _, a := range done {
		if !a.lost {
			c.inflight -= len(a.p.payload)
		}
		bytes += len(a.p.payload)
		if a.resends == 0 {
			c.measure(now.Sub(a.sentAt))
		}
		for _, op := range c.outbox {
			if op.sentAt.Before(a.sentAt) {
				op.overtaken++
			}
		}
	}
	c.retries = 0
	if bytes > 0 {
		c.cc.onAck(p.tsDiff, bytes, now)
	}
	fresh := false
	for _, op := range c.outbox {
		if op.overtaken >= 3 && !op.lost {
			c.lose(op)
			fresh = fresh || !seqLess(op.p.seq, c.recovery)
		}
	}
	if fresh {
		c.cc.onLoss()
		c.recovery = c.seq
	}
}

func (c *Conn) lose(op *outPacket) {
	if !op.lost {
		op.lost = true
		c.inflight -= len(op.p.payload)
	}
}

func (c *Conn) measure(sample time.Duration) {
	if c.rtt == 0 {
		c.rtt, c.rttVar = sample, sample/2
	} else {
		delta := c.rtt - sample
		c.rttVar += (max(delta, -delta) - c.rttVar) / 4
		c.rtt += (sample - c.rtt) / 8
	}
	c.rto = min(max(c.rtt+4*c.rttVar, minRTO), maxRTO)
}

// a timeout means everything in flight is gone: the window shrinks and lost packets go again in order
func (c *Conn) tick(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	if c.overdue(now) {
		limit := maxRetries
		if !c.connected {
			limit = synRetries
		}
		if c.retries++; c.retries > limit {
			c.failLocked(ErrTimeout)
			return
		}
		c.rto = min(2*c.rto, maxRTO)
		for _, op := range c.outbox {
			c.lose(op)
		}
		c.cc.onTimeout()
		c.flush(now)
	}
	if c.connected && now.Sub(c.lastSent) > keepAliveIn {
		c.sendState()
	}
}

func (c *Conn) overdue(now time.Time) bool {
	for _, op := range c.outbox {
		if !op.lost && now.Sub(op.sentAt) > c.rto {
			return true
		}
	}
	return false
}

func (c *Conn) flush(now time.Time) {
	for _, op := range c.outbox {
		if !op.lost {
			continue
		}
		size := len(op.p.payload)
		if c.inflight > 0 && c.inflight+size > c.window() {
			return
		}
		op.lost = false
		op.overtaken = 0
		op.resends++
		op.sentAt = now
		c.inflight += size
		c.transmit(op.p)
	}
}

func (c *Conn) window() int {
	return min(c.cc.window, int(c.peerWnd))
}

func (c *Conn) queue(typ packetType, payload []byte) {
	p := packet{typ: typ, seq: c.seq, payload: slices.Clone(payload)}
	c.seq++
	c.outbox = append(c.outbox, &outPacket{p: p, sentAt: time.Now()})
	c.inflight += len(payload)
	c.transmit(p)
}

func (c *Conn) sendState() {
	c.transmit(packet{typ: stState, seq: c.seq, sack: c.sackMask()})
}

func (c *Conn) transmit(p packet) {
	now := time.Now()
	p.connID = c.sendID
	if p.typ == stSyn {
		p.connID = c.recvID
	}
	p.timestamp = micros(now)
	p.tsDiff = c.reply
	p.wnd = uint32(max(recvBuffer-len(c.inbuf)-c.aheadLen, 0))
	p.ack = c.ack
	c.lastSent = now
	c.s.write(p.marshal(), c.remote)
}

func (c *Conn) sackMask() []byte {
	var mask []byte
	for seq := range c.ahead {
		i := int(seq - c.ack - 2)
		if i >= maxSack*8 {
			continue
		}
		for len(mask) <= i/8 {
			mask = append(mask, 0, 0, 0, 0)
		}
		mask[i/8] |= 1 << (i % 8)
	}
	return mask
}

// a closed connection lingers until its FIN is acknowledged, then leaves the socket
func (c *Conn) settle() {
	if c.closed && len(c.outbox) == 0 && c.err == nil {
		c.err = net.ErrClosed
		c.s.remove(c)
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failLocked(err)
}

func (c *Conn) failLocked(err error) {
	if c.err == nil {
		c.err = err
		c.s.remove(c)
		c.cond.Broadcast()
	}
}

func (c *Conn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.inbuf) == 0 {
		switch {
		case c.closed:
			return 0, net.ErrClosed
		case c.gotFin:
			return 0, io.EOF
		case c.err != nil:
			return 0, c.err
		}
		if err := c.wait(c.readDeadline); err != nil {
			return 0, err
		}
	}
	starved := recvBuffer-len(c.inbuf)-c.aheadLen < maxPayload
	n := copy(b, c.inbuf)
	c.inbuf = c.inbuf[n:]
	if len(c.inbuf) == 0 {
		c.inbuf = nil
	}
	if starved {
		c.sendState()
	}
	return n, nil
}

// a full window still lets one packet out when nothing is in flight, which also probes a zero window
func (c *Conn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := 0
	for written < len(b) {
		switch {
		case c.closed:
			return written, net.ErrClosed
		case c.err != nil:
			return written, c.err
		}
		size := min(len(b)-written, maxPayload)
		if c.inflight > 0 && c.inflight+size > c.window() {
			if err := c.wait(c.writeDeadline); err != nil {
				return written, err
			}
			continue
		}
		c.queue(stData, b[written:written+size])
		written += size
	}
	return written, nil
}

func (c *Conn) wait(deadline time.Time) error {
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.AfterFunc(d, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.cond.Broadcast()
		})
		defer t.Stop()
	}
	c.cond.Wait()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	return nil
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.inbuf = nil
	if c.connected && c.err == nil {
		c.queue(stFin, nil)
	} else {
		c.failLocked(net.ErrClosed)
	}
	c.cond.Broadcast()
	return nil
}

func (c *Conn) LocalAddr() net.Addr  { return c.s.Addr() }
func (c *Conn) RemoteAddr() net.Addr { return net.UDPAddrFromAddrPort(c.remote) }

func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline, c.writeDeadline = t, t
	c.cond.Broadcast()
	return nil
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.cond.Broadcast()
	return nil
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	c.cond.Broadcast()
	return nil
}

func micros(t time.Time) uint32 {
	return uint32(t.UnixMicro())
}
