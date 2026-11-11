package tracker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"
)

const (
	udpProtocolID  = 0x41727101980
	actionConnect  = 0
	actionAnnounce = 1
	actionError    = 3
	udpAttempts    = 4
	connectLen     = 16
	announceHeader = 20
)

var udpFirstTimeout = 2 * time.Second

var udpEvents = map[Event]uint32{EventNone: 0, EventCompleted: 1, EventStarted: 2, EventStopped: 3}

func announceUDP(ctx context.Context, u *url.URL, req Request) (Response, error) {
	if u.Port() == "" {
		return Response{}, fmt.Errorf("%w: UDP tracker %q has no port", ErrTracker, u.Host)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", u.Host)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrTracker, err)
	}
	defer conn.Close()

	connID, err := udpConnect(ctx, conn)
	if err != nil {
		return Response{}, err
	}
	resp, err := roundTrip(ctx, conn, announcePacket(connID, req), actionAnnounce, announceHeader)
	if err != nil {
		return Response{}, err
	}

	addrLen := 4
	if remote, ok := conn.RemoteAddr().(*net.UDPAddr); ok && remote.AddrPort().Addr().Unmap().Is6() {
		addrLen = 16
	}
	peers, err := compactPeers(string(resp[announceHeader:]), addrLen)
	if err != nil {
		return Response{}, err
	}
	interval := time.Duration(binary.BigEndian.Uint32(resp[8:12])) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	return Response{Interval: interval, Peers: peers}, nil
}

func udpConnect(ctx context.Context, conn net.Conn) (uint64, error) {
	packet := make([]byte, 0, connectLen)
	packet = binary.BigEndian.AppendUint64(packet, udpProtocolID)
	packet = binary.BigEndian.AppendUint32(packet, actionConnect)
	packet = binary.BigEndian.AppendUint32(packet, 0)
	resp, err := roundTrip(ctx, conn, packet, actionConnect, connectLen)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(resp[8:16]), nil
}

func announcePacket(connID uint64, req Request) []byte {
	packet := make([]byte, 0, 98)
	packet = binary.BigEndian.AppendUint64(packet, connID)
	packet = binary.BigEndian.AppendUint32(packet, actionAnnounce)
	packet = binary.BigEndian.AppendUint32(packet, 0)
	packet = append(packet, req.InfoHash[:]...)
	packet = append(packet, req.PeerID[:]...)
	packet = binary.BigEndian.AppendUint64(packet, uint64(req.Downloaded))
	packet = binary.BigEndian.AppendUint64(packet, uint64(req.Left))
	packet = binary.BigEndian.AppendUint64(packet, uint64(req.Uploaded))
	packet = binary.BigEndian.AppendUint32(packet, udpEvents[req.Event])
	packet = binary.BigEndian.AppendUint32(packet, 0)
	packet = binary.BigEndian.AppendUint32(packet, randomUint32())
	packet = binary.BigEndian.AppendUint32(packet, 0xffffffff)
	return binary.BigEndian.AppendUint16(packet, req.Port)
}

// roundTrip stamps a fresh transaction id at bytes 12..16 and resends with a doubling timeout,
// because UDP may drop either the request or the answer
func roundTrip(ctx context.Context, conn net.Conn, request []byte, action uint32, minLen int) ([]byte, error) {
	tid := randomUint32()
	packet := append([]byte(nil), request...)
	binary.BigEndian.PutUint32(packet[12:16], tid)

	buf := make([]byte, 64<<10)
	timeout := udpFirstTimeout
	for range udpAttempts {
		if _, err := conn.Write(packet); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTracker, err)
		}
		deadline := time.Now().Add(timeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		conn.SetReadDeadline(deadline)
		resp, err := readReply(conn, buf, tid, action, minLen)
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return resp, err
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrTracker, ctx.Err())
		}
		timeout *= 2
	}
	return nil, fmt.Errorf("%w: no answer from the UDP tracker", ErrTracker)
}

func readReply(conn net.Conn, buf []byte, tid, action uint32, minLen int) ([]byte, error) {
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		resp := buf[:n]
		if n < 8 || binary.BigEndian.Uint32(resp[4:8]) != tid {
			continue
		}
		switch got := binary.BigEndian.Uint32(resp[0:4]); {
		case got == actionError:
			return nil, fmt.Errorf("%w: %s", ErrTracker, resp[8:])
		case got != action || n < minLen:
			return nil, fmt.Errorf("%w: malformed UDP reply", ErrTracker)
		}
		return append([]byte(nil), resp...), nil
	}
}

func randomUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
