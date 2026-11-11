package dht

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"sync"
	"time"
)

type answer struct {
	r   map[string]any
	err error
}

type call struct {
	addr netip.AddrPort
	ch   chan answer
}

type Client struct {
	conn    net.PacketConn
	id      [20]byte
	Timeout time.Duration

	mu      sync.Mutex
	next    uint16
	pending map[string]call
}

func NewClient(conn net.PacketConn) (*Client, error) {
	c := &Client{conn: conn, Timeout: 2 * time.Second, pending: map[string]call{}}
	if _, err := rand.Read(c.id[:]); err != nil {
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) readLoop() {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := c.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		udp, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		tid, r, err := parseReply(buf[:n])
		if err == errNotReply {
			continue
		}
		c.mu.Lock()
		pending, found := c.pending[tid]
		if found && pending.addr == unmapped(udp.AddrPort()) {
			delete(c.pending, tid)
		} else {
			found = false
		}
		c.mu.Unlock()
		if found {
			pending.ch <- answer{r: r, err: err}
		}
	}
}

func unmapped(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

func (c *Client) query(ctx context.Context, addr netip.AddrPort, method string, args map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	ch := make(chan answer, 1)
	c.mu.Lock()
	c.next++
	tid := string(binary.BigEndian.AppendUint16(nil, c.next))
	c.pending[tid] = call{addr: addr, ch: ch}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, tid)
		c.mu.Unlock()
	}()

	withID := maps.Clone(args)
	withID["id"] = string(c.id[:])
	payload, err := encodeQuery(tid, method, withID)
	if err != nil {
		return nil, err
	}
	if _, err := c.conn.WriteTo(payload, net.UDPAddrFromAddrPort(addr)); err != nil {
		return nil, err
	}
	select {
	case a := <-ch:
		return a.r, a.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%s %v: %w", method, addr, ctx.Err())
	}
}

func (c *Client) Ping(ctx context.Context, addr netip.AddrPort) ([20]byte, error) {
	r, err := c.query(ctx, addr, "ping", map[string]any{})
	if err != nil {
		return [20]byte{}, err
	}
	id, ok := nodeID(r)
	if !ok {
		return id, fmt.Errorf("ping %v: reply without a node id", addr)
	}
	return id, nil
}

type getPeersReply struct {
	from  node
	token string
	peers []netip.AddrPort
	nodes []node
}

func (c *Client) getPeers(ctx context.Context, addr netip.AddrPort, infoHash [20]byte) (getPeersReply, error) {
	r, err := c.query(ctx, addr, "get_peers", map[string]any{"info_hash": string(infoHash[:])})
	if err != nil {
		return getPeersReply{}, err
	}
	id, _ := nodeID(r)
	token, _ := r["token"].(string)
	values, _ := r["values"].([]any)
	nodes, _ := r["nodes"].(string)
	return getPeersReply{
		from:  node{id: id, addr: addr},
		token: token,
		peers: compactPeers(values),
		nodes: compactNodes(nodes),
	}, nil
}

func (c *Client) announcePeer(ctx context.Context, addr netip.AddrPort, infoHash [20]byte, port int, token string) error {
	_, err := c.query(ctx, addr, "announce_peer", map[string]any{
		"info_hash":    string(infoHash[:]),
		"port":         port,
		"token":        token,
		"implied_port": 0,
	})
	return err
}
