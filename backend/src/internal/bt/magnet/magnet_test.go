package magnet

import (
	"encoding/hex"
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	const hexHash = "c12fe1c06bba254a9dc9f519b335aa7c1367a88a"
	link, err := Parse("magnet:?xt=urn:btih:" + hexHash +
		"&dn=debian-13.iso&tr=http%3A%2F%2Fbttracker.debian.org%3A6969%2Fannounce&tr=udp%3A%2F%2Ft.example%3A1")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(link.InfoHash[:]) != hexHash || link.Name != "debian-13.iso" {
		t.Fatalf("got %+v", link)
	}
	if len(link.Trackers) != 2 || link.Trackers[0] != "http://bttracker.debian.org:6969/announce" {
		t.Fatalf("trackers %v", link.Trackers)
	}

	b32, err := Parse("magnet:?xt=urn:btih:YEX6DQDLXISUVHOJ6UM3GNNKPQJWPKEK")
	if err != nil || b32.InfoHash != link.InfoHash {
		t.Fatalf("base32 form: %x, %v", b32.InfoHash, err)
	}
	upper, err := Parse("magnet:?xt=urn:btih:C12FE1C06BBA254A9DC9F519B335AA7C1367A88A")
	if err != nil || upper.InfoHash != link.InfoHash {
		t.Fatalf("upper-case hex: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, uri := range []string{
		"http://example.com",
		"magnet:?dn=no-hash",
		"magnet:?xt=urn:btih:1234",
		"magnet:?xt=urn:btih:zz2fe1c06bba254a9dc9f519b335aa7c1367a88a",
		"magnet:?xt=urn:btmh:1220abcd",
	} {
		if _, err := Parse(uri); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", uri, err)
		}
	}
}
