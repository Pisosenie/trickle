package tracker

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type udpScript struct {
	dropConnects int
	noise        bool
	silent       bool
	failure      string
	peers        []netip.AddrPort
	got          chan []byte
}

const fakeConnID = 0x0123456789abcdef

func fakeUDPTracker(t *testing.T, s *udpScript) string {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if s.silent || n < 16 {
				continue
			}
			p := buf[:n]
			action := binary.BigEndian.Uint32(p[8:12])
			tid := p[12:16]
			reply := func(action uint32, body []byte) {
				out := binary.BigEndian.AppendUint32(nil, action)
				out = append(out, tid...)
				conn.WriteTo(append(out, body...), from)
			}
			switch {
			case action == actionConnect && binary.BigEndian.Uint64(p[0:8]) == udpProtocolID:
				if s.dropConnects > 0 {
					s.dropConnects--
					continue
				}
				if s.noise {
					conn.WriteTo(append(binary.BigEndian.AppendUint32(nil, actionConnect), 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8), from)
				}
				reply(actionConnect, binary.BigEndian.AppendUint64(nil, fakeConnID))
			case action == actionAnnounce && binary.BigEndian.Uint64(p[0:8]) == fakeConnID && n == 98:
				if s.got != nil {
					s.got <- append([]byte(nil), p...)
				}
				if s.failure != "" {
					reply(actionError, []byte(s.failure))
					continue
				}
				body := binary.BigEndian.AppendUint32(nil, 600)
				body = binary.BigEndian.AppendUint32(body, 1)
				body = binary.BigEndian.AppendUint32(body, uint32(len(s.peers)))
				for _, peer := range s.peers {
					ip := peer.Addr().As4()
					body = binary.BigEndian.AppendUint16(append(body, ip[:]...), peer.Port())
				}
				reply(actionAnnounce, body)
			default:
				reply(actionError, []byte("bad connection id"))
			}
		}
	}()
	return "udp://" + conn.LocalAddr().String() + "/announce"
}

func udpAnnounce(t *testing.T, url string, timeout time.Duration) (Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Announce(ctx, http.DefaultClient, url, testRequest())
}

func TestUDPAnnounce(t *testing.T) {
	want := []netip.AddrPort{netip.MustParseAddrPort("10.0.0.1:6881"), netip.MustParseAddrPort("192.168.0.9:51413")}
	script := &udpScript{peers: want, got: make(chan []byte, 1)}
	res, err := udpAnnounce(t, fakeUDPTracker(t, script), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Interval != 10*time.Minute || len(res.Peers) != 2 || res.Peers[0] != want[0] || res.Peers[1] != want[1] {
		t.Fatalf("got %+v", res)
	}

	sent := <-script.got
	req := testRequest()
	if string(sent[16:36]) != string(req.InfoHash[:]) || !strings.HasPrefix(string(sent[36:56]), clientPrefix) {
		t.Fatal("info hash or peer id not where BEP 15 puts them")
	}
	left := binary.BigEndian.Uint64(sent[64:72])
	event := binary.BigEndian.Uint32(sent[80:84])
	numWant := binary.BigEndian.Uint32(sent[92:96])
	port := binary.BigEndian.Uint16(sent[96:98])
	if left != 1000 || event != 2 || numWant != 0xffffffff || port != 6881 {
		t.Fatalf("left=%d event=%d num_want=%x port=%d", left, event, numWant, port)
	}
}

func TestUDPRetriesAndIgnoresStrangers(t *testing.T) {
	old := udpFirstTimeout
	udpFirstTimeout = 100 * time.Millisecond
	defer func() { udpFirstTimeout = old }()

	url := fakeUDPTracker(t, &udpScript{dropConnects: 2, noise: true, peers: []netip.AddrPort{netip.MustParseAddrPort("1.1.1.1:1")}})
	res, err := udpAnnounce(t, url, 5*time.Second)
	if err != nil || len(res.Peers) != 1 {
		t.Fatalf("lost packets should be retried: %+v, %v", res, err)
	}
}

func TestUDPErrors(t *testing.T) {
	old := udpFirstTimeout
	udpFirstTimeout = 100 * time.Millisecond
	defer func() { udpFirstTimeout = old }()

	_, err := udpAnnounce(t, fakeUDPTracker(t, &udpScript{failure: "unregistered torrent"}), 5*time.Second)
	if !errors.Is(err, ErrTracker) || !strings.Contains(err.Error(), "unregistered torrent") {
		t.Errorf("tracker error: %v", err)
	}

	started := time.Now()
	_, err = udpAnnounce(t, fakeUDPTracker(t, &udpScript{silent: true}), 400*time.Millisecond)
	if !errors.Is(err, ErrTracker) || time.Since(started) > 2*time.Second {
		t.Errorf("silent tracker: %v after %v", err, time.Since(started))
	}

	if _, err := udpAnnounce(t, "udp://tracker.example/announce", time.Second); !errors.Is(err, ErrTracker) {
		t.Errorf("missing port: %v", err)
	}
}
