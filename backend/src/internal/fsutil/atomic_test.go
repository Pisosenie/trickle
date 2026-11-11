package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	if err := WriteFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "two" {
		t.Fatalf("content = %q, want %q", got, "two")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	var missing map[string]int
	if found, err := ReadJSON(path, &missing); found || err != nil {
		t.Fatalf("missing file: found=%v err=%v", found, err)
	}
	if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if found, err := ReadJSON(path, &got); !found || err != nil || got["a"] != 1 {
		t.Fatalf("got %v found=%v err=%v", got, found, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJSON(path, &got); err == nil {
		t.Fatal("broken JSON must fail")
	}
}
