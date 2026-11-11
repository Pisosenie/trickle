package storage

import (
	"bytes"
	"crypto/sha1"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"trickle/internal/bt/torrentfile"
)

func layout(pieceLength int64, data []byte, files ...torrentfile.Entry) torrentfile.File {
	tf := torrentfile.File{Name: "pack", Length: int64(len(data)), PieceLength: pieceLength, Files: files}
	for off := int64(0); off < tf.Length; off += pieceLength {
		tf.Pieces = append(tf.Pieces, sha1.Sum(data[off:min(off+pieceLength, tf.Length)]))
	}
	return tf
}

func TestWriteAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello, wide world!")
	tf := layout(4, data,
		torrentfile.Entry{Path: []string{"pack", "a.txt"}, Length: 5},
		torrentfile.Entry{Path: []string{"pack", "empty"}, Length: 0},
		torrentfile.Entry{Path: []string{"pack", "sub", "b.txt"}, Length: 13},
	)
	s, err := Open(dir, tf)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteAt(data[3:9], 3); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAt(data[:3], 0); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAt(data[9:], 9); err != nil {
		t.Fatal(err)
	}

	a, _ := os.ReadFile(filepath.Join(dir, "pack", "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dir, "pack", "sub", "b.txt"))
	if string(a) != "hello" || string(b) != ", wide world!" {
		t.Fatalf("files hold %q and %q", a, b)
	}
	if info, err := os.Stat(filepath.Join(dir, "pack", "empty")); err != nil || info.Size() != 0 {
		t.Fatalf("empty file: %v", err)
	}

	back := make([]byte, 8)
	if err := s.ReadAt(back, 2); err != nil || !bytes.Equal(back, data[2:10]) {
		t.Fatalf("read %q, %v", back, err)
	}
	if err := s.ReadAt(make([]byte, 2), int64(len(data))-1); err == nil {
		t.Fatal("reading past the end must fail")
	}
}

func TestVerifyFindsGoodPieces(t *testing.T) {
	dir := t.TempDir()
	data := bytes.Repeat([]byte("0123456789"), 5)
	tf := layout(16, data, torrentfile.Entry{Path: []string{"one.bin"}, Length: int64(len(data))})

	s, err := Open(dir, tf)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WriteAt(data[16:32], 16)
	s.WriteAt(data[48:], 48)

	have, err := s.Verify(tf)
	if err != nil {
		t.Fatal(err)
	}
	if want := []bool{false, true, false, true}; !slices.Equal(have, want) {
		t.Fatalf("have %v, want %v", have, want)
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	data := []byte("persist me")
	tf := layout(4, data, torrentfile.Entry{Path: []string{"keep.bin"}, Length: int64(len(data))})
	s, _ := Open(dir, tf)
	s.WriteAt(data, 0)
	s.Close()

	again, err := Open(dir, tf)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	have, _ := again.Verify(tf)
	if !slices.Equal(have, []bool{true, true, true}) {
		t.Fatalf("data lost on reopen: %v", have)
	}
}

func TestOpenRejectsUnsafePath(t *testing.T) {
	tf := torrentfile.File{Length: 1, PieceLength: 1, Files: []torrentfile.Entry{{Path: []string{"..", "x"}, Length: 1}}}
	if _, err := Open(t.TempDir(), tf); err == nil {
		t.Fatal("path outside the download dir must be rejected")
	}
}
