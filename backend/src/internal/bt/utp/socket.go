package utp

import (
	"context"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

const (
	backlogSize  = 32
	tickInterval = 50 * time.Millisecond
	maxDatagram  = 64 << 10
)

type connKey struct {
	addr netip.AddrPort
	id   uint16
}

// Socket multiplexes uTP connections over one UDP socket and accepts incoming ones like a net.Listener
type Socket struct {
	pc      net.PacketConn
	mu      sync.Mutex
	conns   map[connKey]*Conn
	backlog chan *Conn
	done    chan struct{}
	once    sync.Once
}

func Listen(network, address string) (*Socket, error) {
	pc, err := net.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	return NewSocket(pc), nil
}

func NewSocket(pc net.PacketConn) *Socket {
	s := &Socket{
		pc:      pc,
		conns:   map[connKey]*Conn{},
		backlog: make(chan *Conn, backlogSize),
		done:    make(chan struct{}),
	}
	go s.read()
	go s.tick()
	return s
}

func (s *Socket) Addr() net.Addr {
	return s.pc.LocalAddr()
}

func (s *Socket) Accept() (net.Conn, error) {
	select {
	case c := <-s.backlog:
		return c, nil
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *Socket) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.pc.Close()
		for _, c := range s.snapshot() {
			c.fail(net.ErrClosed)
		}
	})
	return nil
}

func (s *Socket) Dial(ctx context.Context, addr netip.AddrPort) (*Conn, error) {
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	s.mu.Lock()
	id := uint16(rand.Uint32())
	for s.taken(addr, id) {
		id++
	}
	c := newConn(s, addr, id, id+1)
	s.conns[connKey{addr, id}] = c
	s.mu.Unlock()

	c.connect()
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	defer stop()
	if err := c.established(); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Socket) taken(addr netip.AddrPort, id uint16) bool {
	_, ok := s.conns[connKey{addr, id}]
	return ok
}

func (s *Socket) read() {
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := s.pc.ReadFrom(buf)
		if err != nil {
			s.Close()
			return
		}
		p, err := parsePacket(slices.Clone(buf[:n]))
		if err != nil {
			continue
		}
		s.dispatch(addrPort(from), p)
	}
}

func (s *Socket) dispatch(from netip.AddrPort, p packet) {
	if p.typ == stSyn {
		s.incoming(from, p)
		return
	}
	if c := s.lookup(from, p); c != nil {
		c.receive(p)
	} else if p.typ != stReset {
		s.reset(from, p)
	}
}

// a reset may carry either of the two ids of a connection, so it is matched against both
func (s *Socket) lookup(from netip.AddrPort, p packet) *Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.conns[connKey{from, p.connID}]; c != nil || p.typ != stReset {
		return c
	}
	if c := s.conns[connKey{from, p.connID + 1}]; c != nil {
		return c
	}
	return s.conns[connKey{from, p.connID - 1}]
}

// a repeated SYN means our answer was lost; the existing connection answers again
func (s *Socket) incoming(from netip.AddrPort, p packet) {
	key := connKey{from, p.connID + 1}
	s.mu.Lock()
	c, known := s.conns[key]
	if !known {
		c = newConn(s, from, p.connID+1, p.connID)
		c.accepted(p)
		s.conns[key] = c
	}
	s.mu.Unlock()
	if known {
		c.receive(p)
		return
	}
	select {
	case s.backlog <- c:
		c.receive(p)
	default:
		s.remove(c)
		s.reset(from, p)
	}
}

func (s *Socket) reset(to netip.AddrPort, p packet) {
	s.write(packet{typ: stReset, connID: p.connID, seq: uint16(rand.Uint32()), ack: p.seq}.marshal(), to)
}

func (s *Socket) write(b []byte, to netip.AddrPort) {
	s.pc.WriteTo(b, net.UDPAddrFromAddrPort(to))
}

func (s *Socket) remove(c *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := connKey{c.remote, c.recvID}
	if s.conns[key] == c {
		delete(s.conns, key)
	}
}

func (s *Socket) snapshot() []*Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	conns := make([]*Conn, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	return conns
}

func (s *Socket) tick() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			for _, c := range s.snapshot() {
				c.tick(now)
			}
		case <-s.done:
			return
		}
	}
}

func addrPort(a net.Addr) netip.AddrPort {
	if u, ok := a.(*net.UDPAddr); ok {
		ap := u.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	ap, _ := netip.ParseAddrPort(a.String())
	return ap
}
