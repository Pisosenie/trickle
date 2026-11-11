package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"trickle/internal/policy"
)

const MaxTorrentFileSize = 10 << 20

func (e *Engine) AddMagnet(uri, saveDir string) (Torrent, error) {
	if !strings.HasPrefix(uri, "magnet:?") {
		return Torrent{}, fmt.Errorf("%w: not a magnet link", ErrInvalid)
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return Torrent{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return e.add(spec, record{Magnet: uri, SaveDir: saveDir}, nil)
}

func (e *Engine) AddTorrentFile(data []byte, saveDir string) (Torrent, error) {
	if len(data) > MaxTorrentFileSize {
		return Torrent{}, fmt.Errorf("%w: file too large", ErrInvalid)
	}
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return Torrent{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return Torrent{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return e.add(spec, record{SaveDir: saveDir}, mi)
}

func (e *Engine) add(spec *torrent.TorrentSpec, rec record, mi *metainfo.MetaInfo) (Torrent, error) {
	if spec.InfoHash.IsZero() {
		return Torrent{}, fmt.Errorf("%w: no v1 info hash", ErrInvalid)
	}
	if !filepath.IsAbs(rec.SaveDir) {
		return Torrent{}, fmt.Errorf("%w: save dir must be absolute", ErrInvalid)
	}
	if err := os.MkdirAll(rec.SaveDir, 0o755); err != nil {
		return Torrent{}, fmt.Errorf("create save dir: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Torrent{}, ErrClosed
	}
	if existing, ok := e.items[spec.InfoHash]; ok {
		return e.viewLocked(spec.InfoHash, existing), nil
	}

	rec.AddedAt = time.Now().UTC()
	it, err := e.addLocked(spec, rec, mi)
	if err != nil {
		return Torrent{}, err
	}
	if err := e.persistLocked(); err != nil {
		return Torrent{}, err
	}
	return e.viewLocked(spec.InfoHash, it), nil
}

func (e *Engine) addLocked(spec *torrent.TorrentSpec, rec record, mi *metainfo.MetaInfo) (item, error) {
	spec.Storage = e.storageFor(rec.SaveDir)
	spec.DisallowDataDownload = true
	spec.DisallowDataUpload = true
	t, _, err := e.client.AddTorrentSpec(spec)
	if err != nil {
		return item{}, fmt.Errorf("add to client: %w", err)
	}
	if mi != nil {
		if err := e.registry.saveMetainfo(spec.InfoHash, *mi); err != nil {
			t.Drop()
			return item{}, err
		}
	}

	stored := rec
	stored.InfoHash = spec.InfoHash.HexString()
	it := item{t: t, rec: stored}
	e.items[spec.InfoHash] = it
	e.applyLocked(it)
	e.watch(spec.InfoHash, t)
	return it, nil
}

func (e *Engine) Pause(id string) error {
	return e.setPaused(id, true)
}

func (e *Engine) Resume(id string) error {
	return e.setPaused(id, false)
}

func (e *Engine) setPaused(id string, paused bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	hash, it, err := e.findLocked(id)
	if err != nil {
		return err
	}
	next := it
	next.rec.Paused = paused
	e.items[hash] = next
	e.applyLocked(next)
	return e.persistLocked()
}

func (e *Engine) Remove(id string, deleteFiles bool) error {
	e.mu.Lock()
	hash, it, err := e.findLocked(id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	info := it.t.Info()
	it.t.Drop()
	delete(e.items, hash)
	err = e.persistLocked()
	e.mu.Unlock()
	if err != nil {
		return err
	}

	if err := e.registry.removeMetainfo(hash); err != nil {
		e.log.Warn("cannot remove metainfo copy", "err", err)
	}
	if deleteFiles && info != nil {
		return removeData(it.rec.SaveDir, info)
	}
	return nil
}

func removeData(saveDir string, info *metainfo.Info) error {
	var rels []string
	for _, f := range info.UpvertedFiles() {
		rel := filePath(storage.FilePathMakerOpts{Info: info, File: &f})
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("refusing to delete %q outside %q", rel, saveDir)
		}
		rels = append(rels, rel)
	}

	var errs []error
	dirs := map[string]struct{}{}
	for _, rel := range rels {
		if err := os.Remove(filepath.Join(saveDir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir] = struct{}{}
		}
	}
	deepestFirst := slices.SortedFunc(maps.Keys(dirs), func(a, b string) int { return len(b) - len(a) })
	for _, dir := range deepestFirst {
		_ = os.Remove(filepath.Join(saveDir, dir))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("delete files: %w", err)
	}
	return nil
}

func (e *Engine) List() []Torrent {
	e.mu.Lock()
	defer e.mu.Unlock()
	views := make([]Torrent, 0, len(e.items))
	for hash, it := range e.items {
		views = append(views, e.viewLocked(hash, it))
	}
	slices.SortFunc(views, func(a, b Torrent) int { return b.AddedAt.Compare(a.AddedAt) })
	return views
}

func (e *Engine) ApplyDecision(d policy.Decision) {
	e.down.SetLimit(limitOf(d.DownloadLimit))
	e.up.SetLimit(limitOf(d.UploadLimit))
	e.mu.Lock()
	defer e.mu.Unlock()
	e.globalPaused = d.Paused
	e.seeding = d.Seed
	for _, it := range e.items {
		e.applyLocked(it)
	}
}

func (e *Engine) applyLocked(it item) {
	runnable := !it.rec.Paused && !e.globalPaused
	active := runnable && (!it.finished() || e.seeding)
	if runnable {
		it.t.AllowDataDownload()
	} else {
		it.t.DisallowDataDownload()
	}
	if active {
		it.t.AllowDataUpload()
		it.t.SetMaxEstablishedConns(e.maxConns)
	} else {
		it.t.DisallowDataUpload()
		it.t.SetMaxEstablishedConns(0)
	}
}

func (e *Engine) viewLocked(hash metainfo.Hash, it item) Torrent {
	ctx := viewContext{now: time.Now(), globalPaused: e.globalPaused, seeding: e.seeding}
	view, next := it.snapshot(ctx)
	e.items[hash] = next
	return view
}

func (e *Engine) findLocked(id string) (metainfo.Hash, item, error) {
	if e.closed {
		return metainfo.Hash{}, item{}, ErrClosed
	}
	var hash metainfo.Hash
	if err := hash.FromHexString(id); err != nil {
		return hash, item{}, ErrNotFound
	}
	it, ok := e.items[hash]
	if !ok {
		return hash, item{}, ErrNotFound
	}
	return hash, it, nil
}

func (e *Engine) persistLocked() error {
	records := make([]record, 0, len(e.items))
	for _, it := range e.items {
		records = append(records, it.rec)
	}
	slices.SortFunc(records, func(a, b record) int { return a.AddedAt.Compare(b.AddedAt) })
	if err := e.registry.save(records); err != nil {
		return fmt.Errorf("save torrent list: %w", err)
	}
	return nil
}
