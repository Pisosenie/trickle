package peer

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"
)

type Conn struct {
	nc         net.Conn
	PeerID     [20]byte
	Bitfield   Bitfield
	Choked     bool
	Extensions bool
}

func Dial(ctx context.Context, addr netip.AddrPort, infoHash, peerID [20]byte) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return nil, err
	}
	c, err := handshake(ctx, nc, infoHash, peerID)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

func Accept(ctx context.Context, nc net.Conn, infoHash, peerID [20]byte) (*Conn, error) {
	return exchange(ctx, nc, infoHash, peerID, false)
}

func handshake(ctx context.Context, nc net.Conn, infoHash, peerID [20]byte) (*Conn, error) {
	return exchange(ctx, nc, infoHash, peerID, true)
}

// the side that dialed speaks first; the side that accepted checks the info hash before answering
func exchange(ctx context.Context, nc net.Conn, infoHash, peerID [20]byte, dialed bool) (*Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		nc.SetDeadline(deadline)
		defer nc.SetDeadline(time.Time{})
	}
	hs := Handshake{InfoHash: infoHash, PeerID: peerID}
	hs.Reserved[extensionByte] |= extensionBit
	ours := hs.Bytes()
	if dialed {
		if _, err := nc.Write(ours); err != nil {
			return nil, fmt.Errorf("send handshake: %w", err)
		}
	}
	theirs, err := ReadHandshake(nc)
	if err != nil {
		return nil, err
	}
	if theirs.InfoHash != infoHash {
		return nil, fmt.Errorf("%w: peer wants a different torrent", ErrProtocol)
	}
	if !dialed {
		if _, err := nc.Write(ours); err != nil {
			return nil, fmt.Errorf("send handshake: %w", err)
		}
	}
	return &Conn{
		nc:         nc,
		PeerID:     theirs.PeerID,
		Choked:     true,
		Extensions: theirs.Reserved[extensionByte]&extensionBit != 0,
	}, nil
}

func (c *Conn) Send(m *Message) error {
	_, err := c.nc.Write(m.Bytes())
	return err
}

// Read is Receive followed by Apply
func (c *Conn) Read() (*Message, error) {
	m, err := c.Receive()
	if err != nil || m == nil {
		return m, err
	}
	return m, c.Apply(m)
}

// Receive reads one message without touching the connection state, so it can run on its own goroutine
func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.nc)
}

func (c *Conn) Apply(m *Message) error {
	switch m.ID {
	case Choke:
		c.Choked = true
	case Unchoke:
		c.Choked = false
	case BitfieldMsg:
		c.Bitfield = Bitfield(m.Payload)
	case Have:
		index, err := ParseHave(m)
		if err != nil {
			return err
		}
		c.Bitfield = c.Bitfield.With(index)
	}
	return nil
}

func (c *Conn) RemoteAddr() netip.AddrPort {
	tcp, ok := c.nc.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}
	ap := tcp.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func (c *Conn) LocalAddr() netip.AddrPort {
	tcp, ok := c.nc.LocalAddr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}
	ap := tcp.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.nc.SetDeadline(t)
}

func (c *Conn) Close() error {
	return c.nc.Close()
}
