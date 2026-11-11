package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"trickle/internal/engine"
	"trickle/internal/policy"
	"trickle/internal/power"
	"trickle/internal/settings"
)

type fakeEngine struct {
	decisions chan policy.Decision

	mu    sync.Mutex
	added []string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{decisions: make(chan policy.Decision, 16)}
}

func (f *fakeEngine) AddMagnet(uri, saveDir string) (engine.Torrent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, uri+"@"+saveDir)
	return engine.Torrent{ID: "m"}, nil
}

func (f *fakeEngine) AddTorrentFile(data []byte, saveDir string) (engine.Torrent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, string(data)+"@"+saveDir)
	return engine.Torrent{ID: "f"}, nil
}

func (f *fakeEngine) Pause(string) error              { return nil }
func (f *fakeEngine) Resume(string) error             { return nil }
func (f *fakeEngine) Remove(string, bool) error       { return nil }
func (f *fakeEngine) List() []engine.Torrent          { return nil }
func (f *fakeEngine) ApplyDecision(d policy.Decision) { f.decisions <- d }

type fakePhone struct{ enabled []bool }

func (p *fakePhone) SetEnabled(enabled bool) { p.enabled = append(p.enabled, enabled) }

func newService(t *testing.T, game bool) (*Service, *fakeEngine, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := settings.Open(filepath.Join(dir, "settings.json"), settings.Defaults(filepath.Join(dir, "dl")))
	if err != nil {
		t.Fatal(err)
	}
	eng := newFakeEngine()
	svc := New(Deps{
		Settings:    store,
		Engine:      eng,
		ReadPower:   func() (power.Status, error) { return power.Status{HasBattery: true, OnAC: true, Percent: 80}, nil },
		Now:         func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) },
		InternalDir: filepath.Join(dir, "dl"),
		MediaRoot:   filepath.Join(dir, "media"),
		GameRunning: game,
	})
	return svc, eng, dir
}

func nextDecision(t *testing.T, eng *fakeEngine) policy.Decision {
	t.Helper()
	select {
	case d := <-eng.decisions:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no decision applied")
		return policy.Decision{}
	}
}

func TestGameStartPausesAndStopResumes(t *testing.T) {
	svc, eng, _ := newService(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	if d := nextDecision(t, eng); d.Paused {
		t.Fatalf("idle device should not be paused: %+v", d)
	}
	svc.SetGameRunning(true)
	if d := nextDecision(t, eng); !d.Paused || d.Reasons[0] != policy.ReasonGame {
		t.Fatalf("game should pause: %+v", d)
	}
	svc.SetGameRunning(false)
	if d := nextDecision(t, eng); d.Paused {
		t.Fatalf("game end should resume: %+v", d)
	}
	svc.SetGameRunning(false)
	select {
	case d := <-eng.decisions:
		t.Fatalf("repeated report must not re-apply: %+v", d)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStartsWithReportedGameState(t *testing.T) {
	svc, eng, _ := newService(t, true)
	svc.reevaluate()
	if d := nextDecision(t, eng); !d.Paused || d.Reasons[0] != policy.ReasonGame {
		t.Fatalf("a game running at start should hold downloads: %+v", d)
	}
}

func TestUpdateSettingsAppliesChanges(t *testing.T) {
	svc, eng, _ := newService(t, false)
	phone := &fakePhone{}
	svc.AttachPhone(phone)

	next := svc.Settings()
	next.SeedAfterDone = true
	next.PhoneAccess = true
	if err := svc.UpdateSettings(next); err != nil {
		t.Fatalf("update: %v", err)
	}
	if d := nextDecision(t, eng); !d.Seed {
		t.Fatalf("seeding not applied: %+v", d)
	}
	if len(phone.enabled) != 1 || !phone.enabled[0] {
		t.Fatalf("phone not switched: %v", phone.enabled)
	}

	bad := svc.Settings()
	bad.MinBatteryPct = 500
	if err := svc.UpdateSettings(bad); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAddUsesDownloadDir(t *testing.T) {
	svc, eng, dir := newService(t, false)
	if _, err := svc.AddMagnet("  magnet:?xt=urn:btih:abc  "); err != nil {
		t.Fatal(err)
	}
	want := "magnet:?xt=urn:btih:abc@" + filepath.Join(dir, "dl")
	if eng.added[0] != want {
		t.Fatalf("added %q, want %q", eng.added[0], want)
	}
}

func TestAddTorrentPathValidation(t *testing.T) {
	svc, eng, dir := newService(t, false)
	for _, path := range []string{"relative.torrent", filepath.Join(dir, "x.iso"), filepath.Join(dir, "missing.torrent")} {
		if _, err := svc.AddTorrentPath(path); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", path, err)
		}
	}
	good := filepath.Join(dir, "ok.TORRENT")
	if err := os.WriteFile(good, []byte("d4:infoe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddTorrentPath(good); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	if len(eng.added) != 1 {
		t.Fatalf("engine not called: %v", eng.added)
	}
}
