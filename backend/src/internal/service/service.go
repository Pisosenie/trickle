package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"trickle/internal/disk"
	"trickle/internal/engine"
	"trickle/internal/policy"
	"trickle/internal/power"
	"trickle/internal/settings"
)

const pollInterval = 30 * time.Second

type Engine interface {
	AddMagnet(uri, saveDir string) (engine.Torrent, error)
	AddTorrentFile(data []byte, saveDir string) (engine.Torrent, error)
	Pause(id string) error
	Resume(id string) error
	Remove(id string, deleteFiles bool) error
	List() []engine.Torrent
	ApplyDecision(d policy.Decision)
}

type Phone interface {
	SetEnabled(enabled bool)
}

type Deps struct {
	Logger      *slog.Logger
	Settings    *settings.Store
	Engine      Engine
	ReadPower   func() (power.Status, error)
	Now         func() time.Time
	InternalDir string
	MediaRoot   string
	GameRunning bool
}

type Status struct {
	Decision   policy.Decision `json:"decision"`
	FreeBytes  uint64          `json:"free_bytes"`
	TotalBytes uint64          `json:"total_bytes"`
}

type Service struct {
	deps   Deps
	nudge  chan struct{}
	evalMu sync.Mutex

	mu       sync.Mutex
	game     bool
	phone    Phone
	decision policy.Decision
	applied  bool
}

func New(deps Deps) *Service {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Service{deps: deps, nudge: make(chan struct{}, 1), game: deps.GameRunning}
}

func (s *Service) AttachPhone(phone Phone) {
	s.mu.Lock()
	s.phone = phone
	s.mu.Unlock()
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		s.reevaluate()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.nudge:
		}
	}
}

func (s *Service) poke() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

func (s *Service) reevaluate() {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()

	current := s.deps.Settings.Get()
	pw, err := s.deps.ReadPower()
	if err != nil {
		s.deps.Logger.Warn("cannot read power state, assuming mains", "err", err)
		pw = power.Status{OnAC: true}
	}

	s.mu.Lock()
	phone := s.phone
	decision := policy.Evaluate(current, policy.Inputs{GameRunning: s.game, Power: pw, Now: s.deps.Now()})
	changed := !s.applied || !decision.Equal(s.decision)
	s.decision = decision
	s.applied = true
	s.mu.Unlock()

	if phone != nil {
		phone.SetEnabled(current.PhoneAccess)
	}
	if changed {
		s.deps.Engine.ApplyDecision(decision)
		s.deps.Logger.Info("policy applied", "paused", decision.Paused, "reasons", decision.Reasons,
			"down", decision.DownloadLimit, "up", decision.UploadLimit, "seed", decision.Seed)
	}
}

func (s *Service) SetGameRunning(running bool) {
	s.mu.Lock()
	changed := s.game != running
	s.game = running
	s.mu.Unlock()
	if changed {
		s.poke()
	}
}

func (s *Service) Status() Status {
	s.mu.Lock()
	status := Status{Decision: s.decision}
	s.mu.Unlock()
	if free, total, err := disk.Usage(s.deps.Settings.Get().DownloadDir); err == nil {
		status.FreeBytes, status.TotalBytes = free, total
	}
	return status
}

func (s *Service) Settings() settings.Settings {
	return s.deps.Settings.Get()
}

func (s *Service) UpdateSettings(next settings.Settings) error {
	if err := next.Validate(); err != nil {
		return fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	}
	if err := s.deps.Settings.Replace(next); err != nil {
		return err
	}
	s.reevaluate()
	return nil
}

func (s *Service) Torrents() []engine.Torrent {
	return s.deps.Engine.List()
}

func (s *Service) AddMagnet(uri string) (engine.Torrent, error) {
	return s.deps.Engine.AddMagnet(strings.TrimSpace(uri), s.deps.Settings.Get().DownloadDir)
}

func (s *Service) AddTorrentFile(data []byte) (engine.Torrent, error) {
	return s.deps.Engine.AddTorrentFile(data, s.deps.Settings.Get().DownloadDir)
}

func (s *Service) AddTorrentPath(path string) (engine.Torrent, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".torrent") {
		return engine.Torrent{}, fmt.Errorf("%w: expected an absolute path to a .torrent file", engine.ErrInvalid)
	}
	info, err := os.Stat(path)
	if err != nil {
		return engine.Torrent{}, fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	}
	if info.Size() > engine.MaxTorrentFileSize {
		return engine.Torrent{}, fmt.Errorf("%w: file too large", engine.ErrInvalid)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return engine.Torrent{}, fmt.Errorf("read %s: %w", path, err)
	}
	return s.AddTorrentFile(data)
}

func (s *Service) Pause(id string) error  { return s.deps.Engine.Pause(id) }
func (s *Service) Resume(id string) error { return s.deps.Engine.Resume(id) }

func (s *Service) Remove(id string, deleteFiles bool) error {
	return s.deps.Engine.Remove(id, deleteFiles)
}

func (s *Service) Locations() []disk.Location {
	return disk.Locations(s.deps.InternalDir, s.deps.MediaRoot)
}
