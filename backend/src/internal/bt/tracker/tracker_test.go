package tracker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testRequest() Request {
	var hash [20]byte
	for i := range hash {
		hash[i] = byte(i * 13)
	}
	id, _ := NewPeerID()
	return Request{InfoHash: hash, PeerID: id, Port: 6881, Left: 1000, Event: EventStarted}
}

func fakeTracker(t *testing.T, body string, check func(q url.Values)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r.URL.Query())
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func announce(t *testing.T, srv *httptest.Server, path string) (Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Announce(ctx, srv.Client(), srv.URL+path, testRequest())
}

func TestAnnounceCompactPeers(t *testing.T) {
	req := testRequest()
	body := "d8:intervali900e5:peers12:" + "\x0a\x00\x00\x01\x1a\xe1" + "\xc0\xa8\x01\x02\x00\x50" +
		"6:peers618:" + "\x20\x01\x0d\xb8" + strings.Repeat("\x00", 11) + "\x01" + "\x1a\xe1" + "e"
	srv := fakeTracker(t, body, func(q url.Values) {
		if q.Get("info_hash") != string(req.InfoHash[:]) {
			t.Errorf("info_hash arrived as %x", q.Get("info_hash"))
		}
		if q.Get("compact") != "1" || q.Get("event") != "started" || q.Get("left") != "1000" || q.Get("key") != "abc" {
			t.Errorf("unexpected query %v", q)
		}
		if !strings.HasPrefix(q.Get("peer_id"), clientPrefix) || len(q.Get("peer_id")) != 20 {
			t.Errorf("bad peer_id %q", q.Get("peer_id"))
		}
	})

	res, err := announce(t, srv, "/announce?key=abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:6881"),
		netip.MustParseAddrPort("192.168.1.2:80"),
		netip.MustParseAddrPort("[2001:db8::1]:6881"),
	}
	if res.Interval != 15*time.Minute || len(res.Peers) != len(want) {
		t.Fatalf("got %+v", res)
	}
	for i := range want {
		if res.Peers[i] != want[i] {
			t.Errorf("peer %d = %v, want %v", i, res.Peers[i], want[i])
		}
	}
}

func TestAnnounceDictPeers(t *testing.T) {
	srv := fakeTracker(t, "d5:peersld2:ip8:10.0.0.74:porti51413eed2:ip9:not-an-ip4:porti1eeee", nil)
	res, err := announce(t, srv, "/announce")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Peers) != 1 || res.Peers[0] != netip.MustParseAddrPort("10.0.0.7:51413") {
		t.Fatalf("got %+v", res.Peers)
	}
	if res.Interval != defaultInterval {
		t.Fatalf("missing interval should fall back to %v, got %v", defaultInterval, res.Interval)
	}
}

func TestAnnounceErrors(t *testing.T) {
	cases := map[string]string{
		"failure reason": "d14:failure reason17:torrent not founde",
		"broken compact": "d8:intervali60e5:peers5:abcdee",
		"not bencode":    "<html>",
		"not a dict":     "li1ee",
	}
	for name, body := range cases {
		srv := fakeTracker(t, body, nil)
		if _, err := announce(t, srv, "/announce"); !errors.Is(err, ErrTracker) {
			t.Errorf("%s: expected ErrTracker, got %v", name, err)
		}
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if _, err := announce(t, down, "/announce"); !errors.Is(err, ErrTracker) || !strings.Contains(err.Error(), "503") {
		t.Errorf("HTTP 503 should be a tracker error, got %v", err)
	}

	if _, err := Announce(context.Background(), http.DefaultClient, "wss://tracker.example/announce", testRequest()); !errors.Is(err, ErrTracker) {
		t.Errorf("WebSocket trackers are not supported, got %v", err)
	}
}

func TestEscapeBytes(t *testing.T) {
	if got := escapeBytes([]byte{'a', ' ', '+', 0xff, '~'}); got != "a%20%2B%FF~" {
		t.Fatalf("escapeBytes = %q", got)
	}
}
