package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const MediaRoot = "/run/media"

type Location struct {
	Path      string `json:"path"`
	Label     string `json:"label"`
	FreeBytes uint64 `json:"free_bytes"`
}

func Usage(path string) (free, total uint64, err error) {
	existing := nearestExisting(path)
	var st syscall.Statfs_t
	if err := syscall.Statfs(existing, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", existing, err)
	}
	blockSize := uint64(st.Bsize)
	return uint64(st.Bavail) * blockSize, uint64(st.Blocks) * blockSize, nil
}

func nearestExisting(path string) string {
	current := filepath.Clean(path)
	for {
		if _, err := os.Stat(current); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}

func Locations(internalDir, mediaRoot string) []Location {
	locations := []Location{measure(Location{Path: internalDir, Label: "Internal storage"})}
	for _, mount := range removableMounts(mediaRoot) {
		locations = append(locations, measure(Location{
			Path:  filepath.Join(mount, "Downloads"),
			Label: filepath.Base(mount),
		}))
	}
	return locations
}

func measure(loc Location) Location {
	free, _, err := Usage(loc.Path)
	if err != nil {
		return loc
	}
	next := loc
	next.FreeBytes = free
	return next
}

func removableMounts(mediaRoot string) []string {
	rootDev, ok := deviceOf(mediaRoot)
	if !ok {
		return nil
	}
	var mounts []string
	for _, first := range subdirs(mediaRoot) {
		if dev, ok := deviceOf(first); ok && dev != rootDev {
			mounts = append(mounts, first)
			continue
		}
		for _, second := range subdirs(first) {
			if dev, ok := deviceOf(second); ok && dev != rootDev {
				mounts = append(mounts, second)
			}
		}
	}
	return mounts
}

func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(dir, entry.Name()))
		}
	}
	return dirs
}

func deviceOf(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
