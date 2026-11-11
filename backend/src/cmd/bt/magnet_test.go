package main

import (
	"crypto/sha1"
	"testing"

	"trickle/internal/bt/torrentfile"
)

func TestSavedTorrentKeepsInfoHash(t *testing.T) {
	info := []byte("d6:lengthi1e4:name5:x.iso12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	for _, announce := range []string{"http://tracker.example/announce", ""} {
		tf, err := torrentfile.Parse(torrentBytes(info, announce))
		if err != nil {
			t.Fatalf("announce %q: %v", announce, err)
		}
		if tf.InfoHash != sha1.Sum(info) || tf.Announce != announce || tf.Name != "x.iso" {
			t.Fatalf("announce %q: got %+v", announce, tf)
		}
	}
}

func TestParseTarget(t *testing.T) {
	const hash = "c12fe1c06bba254a9dc9f519b335aa7c1367a88a"
	fromHex, err := parseTarget(hash)
	if err != nil {
		t.Fatal(err)
	}
	fromMagnet, err := parseTarget("magnet:?xt=urn:btih:" + hash)
	if err != nil || fromMagnet.InfoHash != fromHex.InfoHash {
		t.Fatalf("magnet and hex disagree: %v", err)
	}
	if _, err := parseTarget("not-a-hash"); err == nil {
		t.Fatal("garbage must be rejected")
	}
}

func TestFirstSupportedTracker(t *testing.T) {
	if got := firstSupportedTracker([]string{"wss://a/announce", "udp://b:1337/announce", "http://c"}); got != "udp://b:1337/announce" {
		t.Fatalf("got %q", got)
	}
	if firstSupportedTracker([]string{"wss://a/announce"}) != "" {
		t.Fatal("WebSocket trackers are not supported")
	}
}
