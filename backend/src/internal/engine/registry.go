package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"trickle/internal/fsutil"
)

type record struct {
	InfoHash  string    `json:"info_hash"`
	Magnet    string    `json:"magnet,omitempty"`
	SaveDir   string    `json:"save_dir"`
	AddedAt   time.Time `json:"added_at"`
	Paused    bool      `json:"paused"`
	Completed bool      `json:"completed"`
}

type registry struct {
	dir string
}

func (r registry) listPath() string {
	return filepath.Join(r.dir, "torrents.json")
}

func (r registry) metainfoPath(hash metainfo.Hash) string {
	return filepath.Join(r.dir, "metainfo", hash.HexString()+".torrent")
}

func (r registry) load() ([]record, error) {
	var records []record
	if _, err := fsutil.ReadJSON(r.listPath(), &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (r registry) save(records []record) error {
	return fsutil.WriteJSON(r.listPath(), records)
}

func (r registry) hasMetainfo(hash metainfo.Hash) bool {
	_, err := os.Stat(r.metainfoPath(hash))
	return err == nil
}

func (r registry) loadMetainfo(hash metainfo.Hash) (*metainfo.MetaInfo, error) {
	mi, err := metainfo.LoadFromFile(r.metainfoPath(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load metainfo %s: %w", hash.HexString(), err)
	}
	return mi, nil
}

func (r registry) saveMetainfo(hash metainfo.Hash, mi metainfo.MetaInfo) error {
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return fmt.Errorf("encode metainfo: %w", err)
	}
	return fsutil.WriteFileAtomic(r.metainfoPath(hash), buf.Bytes(), 0o600)
}

func (r registry) removeMetainfo(hash metainfo.Hash) error {
	err := os.Remove(r.metainfoPath(hash))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove metainfo: %w", err)
	}
	return nil
}
