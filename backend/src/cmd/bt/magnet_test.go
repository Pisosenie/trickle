package main

import (
	"crypto/sha1"
	"slices"
	"testing"

	"trickle/internal/bt/torrentfile"
)

func TestSavedTorrentKeepsInfoHash(t *testing.T) {
	info := []byte("d6:lengthi1e4:name5:x.iso12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	for _, trackers := range [][]string{
		{"http://tracker.example/announce", "udp://backup.example:1337/announce"},
		{"udp://only.example:6969"},
		nil,
	} {
		tf, err := torrentfile.Parse(torrentBytes(info, trackers))
		if err != nil {
			t.Fatalf("trackers %v: %v", trackers, err)
		}
		if tf.InfoHash != sha1.Sum(info) || !slices.Equal(tf.Trackers, trackers) || tf.Name != "x.iso" {
			t.Fatalf("trackers %v: got %+v", trackers, tf)
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

func TestSupportedTrackers(t *testing.T) {
	got := supportedTrackers([]string{"wss://a/announce", "udp://b:1337/announce", "http://c"})
	if !slices.Equal(got, []string{"udp://b:1337/announce", "http://c"}) {
		t.Fatalf("got %v", got)
	}
}
