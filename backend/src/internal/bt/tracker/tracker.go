package tracker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"trickle/internal/bt/bencode"
)

const (
	maxResponse     = 1 << 20
	defaultInterval = 30 * time.Minute
	clientPrefix    = "-TK0001-"
)

var ErrTracker = errors.New("tracker error")

type Event string

const (
	EventNone      Event = ""
	EventStarted   Event = "started"
	EventStopped   Event = "stopped"
	EventCompleted Event = "completed"
)

type Request struct {
	InfoHash   [20]byte
	PeerID     [20]byte
	Port       uint16
	Uploaded   int64
	Downloaded int64
	Left       int64
	Event      Event
}

type Response struct {
	Interval time.Duration
	Peers    []netip.AddrPort
}

func NewPeerID() ([20]byte, error) {
	var id [20]byte
	copy(id[:], clientPrefix)
	if _, err := rand.Read(id[len(clientPrefix):]); err != nil {
		return id, fmt.Errorf("generate peer id: %w", err)
	}
	return id, nil
}

func Announce(ctx context.Context, client *http.Client, announce string, req Request) (Response, error) {
	u, err := url.Parse(announce)
	if err != nil {
		return Response{}, fmt.Errorf("%w: bad announce URL %q", ErrTracker, announce)
	}
	switch u.Scheme {
	case "http", "https":
		return announceHTTP(ctx, client, u, req)
	case "udp":
		return announceUDP(ctx, u, req)
	default:
		return Response{}, fmt.Errorf("%w: unsupported announce URL %q", ErrTracker, announce)
	}
}

func announceHTTP(ctx context.Context, client *http.Client, u *url.URL, req Request) (Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, announceURL(u, req), nil)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrTracker, err)
	}
	res, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrTracker, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("%w: HTTP %d", ErrTracker, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return Response{}, fmt.Errorf("%w: read response: %v", ErrTracker, err)
	}
	if len(body) > maxResponse {
		return Response{}, fmt.Errorf("%w: response larger than %d bytes", ErrTracker, maxResponse)
	}
	return parseResponse(body)
}

func announceURL(announce *url.URL, req Request) string {
	u := *announce
	params := []string{
		"info_hash=" + escapeBytes(req.InfoHash[:]),
		"peer_id=" + escapeBytes(req.PeerID[:]),
		"port=" + strconv.Itoa(int(req.Port)),
		"uploaded=" + strconv.FormatInt(req.Uploaded, 10),
		"downloaded=" + strconv.FormatInt(req.Downloaded, 10),
		"left=" + strconv.FormatInt(req.Left, 10),
		"compact=1",
	}
	if req.Event != EventNone {
		params = append(params, "event="+string(req.Event))
	}
	if u.RawQuery != "" {
		params = append([]string{u.RawQuery}, params...)
	}
	u.RawQuery = strings.Join(params, "&")
	return u.String()
}

// url.QueryEscape turns a 0x20 byte into "+", which some trackers don't decode
func escapeBytes(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if isUnreserved(c) {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func parseResponse(body []byte) (Response, error) {
	v, err := bencode.Decode(body)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrTracker, err)
	}
	dict, ok := v.(map[string]any)
	if !ok {
		return Response{}, fmt.Errorf("%w: response is not a dictionary", ErrTracker)
	}
	if reason, ok := dict["failure reason"].(string); ok {
		return Response{}, fmt.Errorf("%w: %s", ErrTracker, reason)
	}

	interval := defaultInterval
	if seconds, ok := dict["interval"].(int64); ok && seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}

	var peers []netip.AddrPort
	switch raw := dict["peers"].(type) {
	case string:
		peers, err = compactPeers(raw, 4)
	case []any:
		peers = dictPeers(raw)
	case nil:
	default:
		err = fmt.Errorf("%w: unexpected peers type %T", ErrTracker, raw)
	}
	if err != nil {
		return Response{}, err
	}
	if raw, ok := dict["peers6"].(string); ok {
		v6, err := compactPeers(raw, 16)
		if err != nil {
			return Response{}, err
		}
		peers = append(peers, v6...)
	}
	return Response{Interval: interval, Peers: peers}, nil
}

func compactPeers(raw string, addrLen int) ([]netip.AddrPort, error) {
	size := addrLen + 2
	if len(raw)%size != 0 {
		return nil, fmt.Errorf("%w: compact peer list of %d bytes is not a multiple of %d", ErrTracker, len(raw), size)
	}
	peers := make([]netip.AddrPort, 0, len(raw)/size)
	for i := 0; i < len(raw); i += size {
		addr, _ := netip.AddrFromSlice([]byte(raw[i : i+addrLen]))
		port := binary.BigEndian.Uint16([]byte(raw[i+addrLen : i+size]))
		peers = append(peers, netip.AddrPortFrom(addr, port))
	}
	return peers, nil
}

func dictPeers(list []any) []netip.AddrPort {
	var peers []netip.AddrPort
	for _, item := range list {
		entry, _ := item.(map[string]any)
		ip, _ := entry["ip"].(string)
		port, _ := entry["port"].(int64)
		addr, err := netip.ParseAddr(ip)
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		peers = append(peers, netip.AddrPortFrom(addr, uint16(port)))
	}
	return peers
}
