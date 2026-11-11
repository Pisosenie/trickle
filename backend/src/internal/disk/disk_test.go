package disk

import (
	"path/filepath"
	"testing"
)

func TestUsageOfMissingPathMeasuresParent(t *testing.T) {
	free, total, err := Usage(filepath.Join(t.TempDir(), "not", "yet", "created"))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if total == 0 || free > total {
		t.Fatalf("free=%d total=%d", free, total)
	}
}

func TestLocationsWithoutMediaRoot(t *testing.T) {
	internal := filepath.Join(t.TempDir(), "Downloads")
	locs := Locations(internal, filepath.Join(t.TempDir(), "no-media"))
	if len(locs) != 1 || locs[0].Path != internal {
		t.Fatalf("unexpected locations: %+v", locs)
	}
	if locs[0].FreeBytes == 0 {
		t.Fatal("internal storage was not measured")
	}
}

func TestPlainFoldersAreNotMounts(t *testing.T) {
	root := t.TempDir()
	locs := Locations(filepath.Join(root, "Downloads"), root)
	if len(locs) != 1 {
		t.Fatalf("folders on the same device must not count as drives: %+v", locs)
	}
}
