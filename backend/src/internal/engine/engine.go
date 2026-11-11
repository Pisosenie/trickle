package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

var (
	ErrNotFound = errors.New("torrent not found")
	ErrInvalid  = errors.New("invalid torrent")
	ErrClosed   = errors.New("engine is closed")
)

type Config struct {
	StateDir string
	PeerPort int
	Logger   *slog.Logger

	OnComplete func(Torrent)

	NoDHT            bool
	NoPortForwarding bool
}

const rateBurst = 1 << 20

type Engine struct {
	log        *slog.Logger
	client     *torrent.Client
	completion storage.PieceCompletion
	down, up   *rate.Limiter
	registry   registry
	onComplete func(Torrent)
	stop       chan struct{}
	watchers   sync.WaitGroup

	maxConns int

	mu           sync.Mutex
	items        map[metainfo.Hash]item
	storages     map[string]storage.ClientImpl
	globalPaused bool
	seeding      bool
	closed       bool
}

func New(cfg Config) (*Engine, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	completion, err := storage.NewBoltPieceCompletion(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("open piece database: %w", err)
	}

	e := &Engine{
		log:          log,
		completion:   completion,
		down:         rate.NewLimiter(rate.Inf, rateBurst),
		up:           rate.NewLimiter(rate.Inf, rateBurst),
		registry:     registry{dir: cfg.StateDir},
		onComplete:   cfg.OnComplete,
		stop:         make(chan struct{}),
		items:        map[metainfo.Hash]item{},
		storages:     map[string]storage.ClientImpl{},
		globalPaused: true, // until the first ApplyDecision, so a running game is never disturbed
	}

	cc := e.clientConfig(cfg)
	e.maxConns = cc.EstablishedConnsPerTorrent
	client, err := torrent.NewClient(cc)
	if err != nil {
		completion.Close()
		return nil, fmt.Errorf("start torrent client: %w", err)
	}
	e.client = client

	if err := e.restore(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) clientConfig(cfg Config) *torrent.ClientConfig {
	cc := torrent.NewDefaultClientConfig()
	cc.DataDir = cfg.StateDir
	cc.DefaultStorage = e.storageFor(cfg.StateDir)
	cc.ListenPort = cfg.PeerPort
	cc.Seed = true
	cc.DownloadRateLimiter = e.down
	cc.UploadRateLimiter = e.up
	cc.NoDHT = cfg.NoDHT
	cc.NoDefaultPortForwarding = cfg.NoPortForwarding
	cc.Slogger = e.log.With("component", "torrent")
	return cc
}

func (e *Engine) restore() error {
	records, err := e.registry.load()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rec := range records {
		spec, err := e.specFor(rec)
		if err == nil {
			_, err = e.addLocked(spec, rec, nil)
		}
		if err != nil {
			e.log.Error("dropping torrent that cannot be restored", "info_hash", rec.InfoHash, "err", err)
		}
	}
	return nil
}

func (e *Engine) specFor(rec record) (*torrent.TorrentSpec, error) {
	var hash metainfo.Hash
	if err := hash.FromHexString(rec.InfoHash); err != nil {
		return nil, fmt.Errorf("bad info hash: %w", err)
	}
	mi, err := e.registry.loadMetainfo(hash)
	if err == nil && mi != nil {
		return torrent.TorrentSpecFromMetaInfoErr(mi)
	}
	if rec.Magnet != "" {
		return torrent.TorrentSpecFromMagnetUri(rec.Magnet)
	}
	if err != nil {
		return nil, err
	}
	return nil, errors.New("neither metainfo nor magnet link saved")
}

func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()

	close(e.stop)
	var errs []error
	if e.client != nil {
		errs = append(errs, e.client.Close()...)
	}
	e.watchers.Wait()
	errs = append(errs, e.completion.Close())
	return errors.Join(errs...)
}

func (e *Engine) storageFor(dir string) storage.ClientImpl {
	if s, ok := e.storages[dir]; ok {
		return s
	}
	s := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		FilePathMaker:   filePath,
		PieceCompletion: e.completion,
		UsePartFiles:    g.Some(false),
		Logger:          e.log,
	})
	e.storages[dir] = s
	return s
}

func filePath(opts storage.FilePathMakerOpts) string {
	parts := opts.File.BestPath()
	if name := opts.Info.BestName(); name != metainfo.NoName {
		parts = append([]string{name}, parts...)
	}
	return filepath.Join(parts...)
}

func limitOf(bytesPerSec int64) rate.Limit {
	if bytesPerSec <= 0 {
		return rate.Inf
	}
	return rate.Limit(min(float64(bytesPerSec), math.MaxInt32))
}
