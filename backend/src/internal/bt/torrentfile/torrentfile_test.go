package torrentfile

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func buildTorrent(t *testing.T, size int, pieceLength int64) (*metainfo.MetaInfo, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "debian-mini.iso")
	content := make([]byte, size)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes, Announce: "http://tracker.example/announce"}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return mi, buf.Bytes()
}

func TestParseMatchesAnacrolix(t *testing.T) {
	mi, data := buildTorrent(t, 1<<20+777, 256<<10)
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.InfoHash != mi.HashInfoBytes() {
		t.Fatalf("info hash %x, anacrolix says %x", got.InfoHash, mi.HashInfoBytes())
	}
	if got.Announce != mi.Announce || got.Name != info.Name || got.Length != info.Length || got.PieceLength != info.PieceLength {
		t.Fatalf("fields differ: %+v", got)
	}
	if len(got.Pieces) != info.NumPieces() {
		t.Fatalf("%d pieces, want %d", len(got.Pieces), info.NumPieces())
	}
	for i, hash := range got.Pieces {
		if !bytes.Equal(hash[:], info.Pieces[i*hashLen:(i+1)*hashLen]) {
			t.Fatalf("piece %d hash differs", i)
		}
	}
	last := len(got.Pieces) - 1
	if got.PieceSize(0) != 256<<10 || got.PieceSize(last) != 777 {
		t.Fatalf("piece sizes %d / %d", got.PieceSize(0), got.PieceSize(last))
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"not bencode":    "hello",
		"not a dict":     "l1:ae",
		"no info":        "d8:announce3:urle",
		"empty files":    "d4:infod5:filesle4:name1:x12:piece lengthi1e6:pieces0:ee",
		"escaping path":  "d4:infod5:filesld6:lengthi1e4:pathl2:..1:xeee4:name1:x12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee",
		"slash in path":  "d4:infod5:filesld6:lengthi1e4:pathl3:a/beee4:name1:x12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee",
		"unsafe name":    "d4:infod6:lengthi1e4:name2:..12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee",
		"missing name":   "d4:infod6:lengthi1e12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee",
		"zero length":    "d4:infod6:lengthi0e4:name1:x12:piece lengthi1e6:pieces0:ee",
		"broken hashes":  "d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces3:abcee",
		"too few hashes": "d4:infod6:lengthi2e4:name1:x12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaaee",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
}

func TestParseMultiFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pack")
	write := func(rel string, size int) {
		path := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		data := make([]byte, size)
		rand.Read(data)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("readme.txt", 1000)
	write("disc/image.iso", 300<<10)

	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	mi.Write(&buf)

	got, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got.InfoHash != mi.HashInfoBytes() || got.Name != "pack" || got.Length != 1000+300<<10 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Files) != 2 || len(got.Pieces) != info.NumPieces() {
		t.Fatalf("files %+v, %d pieces", got.Files, len(got.Pieces))
	}
	for i, f := range info.UpvertedFiles() {
		want := append([]string{"pack"}, f.BestPath()...)
		if filepath.Join(got.Files[i].Path...) != filepath.Join(want...) || got.Files[i].Length != f.Length {
			t.Errorf("file %d = %+v, want %v (%d bytes)", i, got.Files[i], want, f.Length)
		}
	}
}

func TestAnnounceList(t *testing.T) {
	info := "d6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	data := "d8:announce5:http1" +
		"13:announce-listll5:http1e" + "l5:udp:a5:udp:be" + "l0:ee" +
		"4:info" + info + "e"
	got, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Announce != "http1" || !slices.Equal(got.Trackers, []string{"http1", "udp:a", "udp:b"}) {
		t.Fatalf("announce %q, trackers %v", got.Announce, got.Trackers)
	}

	bare, err := ParseInfo([]byte(info))
	if err != nil || bare.Announce != "" || len(bare.Trackers) != 0 {
		t.Fatalf("no trackers: %+v, %v", bare, err)
	}
}
