package engine

import (
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

func (e *Engine) watch(hash metainfo.Hash, t *torrent.Torrent) {
	e.watchers.Add(1)
	go func() {
		defer e.watchers.Done()

		select {
		case <-t.GotInfo():
		case <-t.Closed():
			return
		case <-e.stop:
			return
		}
		t.DownloadAll()
		e.rememberMetainfo(hash, t)

		select {
		case <-t.Complete().On():
		case <-t.Closed():
			return
		case <-e.stop:
			return
		}
		e.finish(hash)
	}()
}

func (e *Engine) rememberMetainfo(hash metainfo.Hash, t *torrent.Torrent) {
	if e.registry.hasMetainfo(hash) {
		return
	}
	if err := e.registry.saveMetainfo(hash, t.Metainfo()); err != nil {
		e.log.Warn("cannot save metainfo", "info_hash", hash.HexString(), "err", err)
	}
}

func (e *Engine) finish(hash metainfo.Hash) {
	e.mu.Lock()
	it, ok := e.items[hash]
	if !ok || e.closed {
		e.mu.Unlock()
		return
	}
	e.applyLocked(it)
	if it.rec.Completed {
		e.mu.Unlock()
		return
	}
	next := it
	next.rec.Completed = true
	e.items[hash] = next
	if err := e.persistLocked(); err != nil {
		e.log.Error("cannot save completion", "err", err)
	}
	view := e.viewLocked(hash, next)
	e.mu.Unlock()

	e.log.Info("download complete", "name", view.Name)
	if e.onComplete != nil {
		e.onComplete(view)
	}
}
