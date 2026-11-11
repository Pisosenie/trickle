package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeepsToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) < 20 {
		t.Fatalf("token not reused: %q vs %q", first, second)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file must be private: %v %v", info.Mode(), err)
	}
}

func TestEqualAndBearer(t *testing.T) {
	if !Equal(FromBearer("Bearer abc"), "abc") {
		t.Fatal("bearer token should match")
	}
	if Equal(FromBearer("abc"), "abc") || Equal(FromBearer("Bearer "), "abc") {
		t.Fatal("malformed header must not match")
	}
}
