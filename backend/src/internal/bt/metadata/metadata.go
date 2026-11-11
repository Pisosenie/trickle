package metadata

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"trickle/internal/bt/bencode"
	"trickle/internal/bt/peer"
)

const (
	localID      = 1
	extHandshake = 0
	pieceSize    = 16 << 10
	maxSize      = 10 << 20
	msgRequest   = 0
	msgData      = 1
	msgReject    = 2
	parallel     = 8
	perPeer      = 15 * time.Second
)

var ErrUnsupported = errors.New("peer does not share metadata")

func Fetch(ctx context.Context, addr netip.AddrPort, infoHash, peerID [20]byte) ([]byte, error) {
	c, err := peer.Dial(ctx, addr, infoHash, peerID)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if !c.Extensions {
		return nil, ErrUnsupported
	}
	if deadline, ok := ctx.Deadline(); ok {
		c.SetDeadline(deadline)
	}

	payload, err := bencode.Encode(map[string]any{"m": map[string]any{"ut_metadata": localID}})
	if err != nil {
		return nil, err
	}
	if err := c.Send(&peer.Message{ID: peer.Extended, Payload: append([]byte{extHandshake}, payload...)}); err != nil {
		return nil, err
	}
	theirID, size, err := awaitHandshake(c)
	if err != nil {
		return nil, err
	}
	return collect(c, theirID, size, infoHash)
}

func awaitHandshake(c *peer.Conn) (byte, int, error) {
	for {
		m, err := c.Read()
		if err != nil {
			return 0, 0, err
		}
		if m == nil || m.ID != peer.Extended || len(m.Payload) == 0 || m.Payload[0] != extHandshake {
			continue
		}
		v, err := bencode.Decode(m.Payload[1:])
		if err != nil {
			return 0, 0, fmt.Errorf("%w: extended handshake: %v", peer.ErrProtocol, err)
		}
		dict, _ := v.(map[string]any)
		exts, _ := dict["m"].(map[string]any)
		id, _ := exts["ut_metadata"].(int64)
		size, _ := dict["metadata_size"].(int64)
		if id <= 0 || id > 255 {
			return 0, 0, ErrUnsupported
		}
		if size <= 0 || size > maxSize {
			return 0, 0, fmt.Errorf("%w: metadata size %d", peer.ErrProtocol, size)
		}
		return byte(id), int(size), nil
	}
}

func collect(c *peer.Conn, theirID byte, size int, infoHash [20]byte) ([]byte, error) {
	pieces := (size + pieceSize - 1) / pieceSize
	for i := range pieces {
		req, err := bencode.Encode(map[string]any{"msg_type": msgRequest, "piece": i})
		if err != nil {
			return nil, err
		}
		if err := c.Send(&peer.Message{ID: peer.Extended, Payload: append([]byte{theirID}, req...)}); err != nil {
			return nil, err
		}
	}

	buf := make([]byte, size)
	got := make([]bool, pieces)
	for left := pieces; left > 0; {
		m, err := c.Read()
		if err != nil {
			return nil, err
		}
		if m == nil || m.ID != peer.Extended || len(m.Payload) == 0 || m.Payload[0] != localID {
			continue
		}
		v, n, err := bencode.DecodePrefix(m.Payload[1:])
		if err != nil {
			return nil, fmt.Errorf("%w: metadata message: %v", peer.ErrProtocol, err)
		}
		dict, _ := v.(map[string]any)
		msgType, _ := dict["msg_type"].(int64)
		index, _ := dict["piece"].(int64)
		switch msgType {
		case msgReject:
			return nil, fmt.Errorf("%w: piece %d refused", ErrUnsupported, index)
		case msgData:
			data := m.Payload[1+n:]
			start := int(index) * pieceSize
			if index < 0 || int(index) >= pieces || len(data) != min(pieceSize, size-start) {
				return nil, fmt.Errorf("%w: bad metadata piece %d", peer.ErrProtocol, index)
			}
			if !got[index] {
				copy(buf[start:], data)
				got[index] = true
				left--
			}
		}
	}

	if sha1.Sum(buf) != infoHash {
		return nil, fmt.Errorf("%w: metadata does not match the info hash", peer.ErrProtocol)
	}
	return buf, nil
}

func FetchAny(ctx context.Context, peers []netip.AddrPort, infoHash, peerID [20]byte) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := make(chan []byte, 1)
	slots := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, addr := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			peerCtx, done := context.WithTimeout(ctx, perPeer)
			defer done()
			if data, err := Fetch(peerCtx, addr, infoHash, peerID); err == nil {
				select {
				case found <- data:
				default:
				}
				cancel()
			}
		}()
	}
	go func() {
		wg.Wait()
		close(found)
	}()

	if data, ok := <-found; ok {
		return data, nil
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	return nil, fmt.Errorf("none of %d peers shared the metadata", len(peers))
}
