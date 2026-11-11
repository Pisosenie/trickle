package engine

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"trickle/internal/policy"
)

const payloadName = "payload.bin"

type fixture struct {
	torrentFile []byte
	content     []byte
	seeder      *torrent.Client
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	seedDir := t.TempDir()
	content := make([]byte, 3<<20+12345)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, payloadName), content, 0o644); err != nil {
		t.Fatal(err)
	}

	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(filepath.Join(seedDir, payloadName)); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = seedDir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   seedDir,
		PieceCompletion: storage.NewMapPieceCompletion(),
	})
	cfg.Seed = true
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenPort = 0
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seeder.Close() })

	st, err := seeder.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	<-st.GotInfo()
	if err := st.VerifyData(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.Complete().On():
	case <-time.After(10 * time.Second):
		t.Fatal("seeder does not have the data")
	}
	return fixture{torrentFile: buf.Bytes(), content: content, seeder: seeder}
}

func newEngine(t *testing.T, stateDir string, onComplete func(Torrent)) *Engine {
	t.Helper()
	eng, err := New(Config{
		StateDir:         stateDir,
		PeerPort:         0,
		NoDHT:            true,
		NoPortForwarding: true,
		OnComplete:       onComplete,
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

func connect(t *testing.T, eng *Engine, id string, seeder *torrent.Client) {
	t.Helper()
	eng.mu.Lock()
	hash, it, err := eng.findLocked(id)
	eng.mu.Unlock()
	if err != nil {
		t.Fatalf("find %s: %v", id, err)
	}
	_ = hash
	it.t.AddClientPeer(seeder)
}

func waitState(t *testing.T, eng *Engine, want State) Torrent {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list := eng.List()
		if len(list) == 1 && list[0].State == want {
			return list[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("torrent never reached state %q: %+v", want, eng.List())
	return Torrent{}
}

func TestDownloadPauseRestartRemove(t *testing.T) {
	fx := newFixture(t)
	stateDir := t.TempDir()
	saveDir := t.TempDir()
	completed := make(chan Torrent, 1)

	eng := newEngine(t, stateDir, func(v Torrent) { completed <- v })
	added, err := eng.AddTorrentFile(fx.torrentFile, saveDir)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	connect(t, eng, added.ID, fx.seeder)

	time.Sleep(1500 * time.Millisecond)
	held := eng.List()[0]
	if held.State != StateWaiting || held.DoneBytes != 0 {
		t.Fatalf("paused engine moved data: %+v", held)
	}

	eng.ApplyDecision(policy.Decision{})
	select {
	case done := <-completed:
		if done.ID != added.ID || done.Progress != 1 {
			t.Fatalf("unexpected completion: %+v", done)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("download did not finish: %+v", eng.List())
	}
	got, err := os.ReadFile(filepath.Join(saveDir, payloadName))
	if err != nil || !bytes.Equal(got, fx.content) {
		t.Fatalf("downloaded file differs (err=%v)", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := newEngine(t, stateDir, func(v Torrent) { t.Errorf("repeated completion: %+v", v) })
	defer reopened.Close()
	reopened.ApplyDecision(policy.Decision{})
	waitState(t, reopened, StateDone)

	if err := reopened.Remove(added.ID, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("torrent still listed after remove")
	}
	if _, err := os.Stat(filepath.Join(saveDir, payloadName)); !os.IsNotExist(err) {
		t.Fatalf("data not deleted: %v", err)
	}
}

func TestUserPauseOverridesPolicy(t *testing.T) {
	fx := newFixture(t)
	eng := newEngine(t, t.TempDir(), nil)
	defer eng.Close()

	added, err := eng.AddTorrentFile(fx.torrentFile, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Pause(added.ID); err != nil {
		t.Fatal(err)
	}
	connect(t, eng, added.ID, fx.seeder)
	eng.ApplyDecision(policy.Decision{})

	time.Sleep(1500 * time.Millisecond)
	if v := eng.List()[0]; v.State != StatePaused || v.DoneBytes != 0 {
		t.Fatalf("user-paused torrent moved data: %+v", v)
	}
	if err := eng.Resume(added.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, eng, StateDone)
}

func TestAddMagnet(t *testing.T) {
	eng := newEngine(t, t.TempDir(), nil)
	defer eng.Close()
	saveDir := t.TempDir()

	if _, err := eng.AddMagnet("https://example.com/file.torrent", saveDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid error, got %v", err)
	}
	if _, err := eng.AddMagnet("magnet:?xt=urn:btih:zz", saveDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid error for bad hash, got %v", err)
	}

	const uri = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=hello"
	first, err := eng.AddMagnet(uri, saveDir)
	if err != nil {
		t.Fatalf("add magnet: %v", err)
	}
	if first.Name != "hello" || first.State != StateWaiting {
		t.Fatalf("unexpected view: %+v", first)
	}
	second, err := eng.AddMagnet(uri, saveDir)
	if err != nil || second.ID != first.ID || len(eng.List()) != 1 {
		t.Fatalf("duplicate add should return the existing torrent: %+v, %v", second, err)
	}
	if _, err := eng.AddMagnet(uri, "relative/dir"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative save dir must be rejected, got %v", err)
	}
}

func TestUnknownTorrent(t *testing.T) {
	eng := newEngine(t, t.TempDir(), nil)
	defer eng.Close()
	for _, id := range []string{"nope", "0123456789abcdef0123456789abcdef01234567"} {
		if err := eng.Pause(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("pause %q: %v", id, err)
		}
		if err := eng.Remove(id, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("remove %q: %v", id, err)
		}
	}
}

func TestProgressSurvivesRestart(t *testing.T) {
	fx := newFixture(t)
	stateDir := t.TempDir()
	saveDir := t.TempDir()
	const pieceLength = 256 << 10
	const minPieces = 2

	eng := newEngine(t, stateDir, nil)
	added, err := eng.AddTorrentFile(fx.torrentFile, saveDir)
	if err != nil {
		t.Fatal(err)
	}
	connect(t, eng, added.ID, fx.seeder)
	eng.ApplyDecision(policy.Decision{DownloadLimit: pieceLength})

	verified := func(e *Engine) int {
		e.mu.Lock()
		defer e.mu.Unlock()
		_, it, err := e.findLocked(added.ID)
		if err != nil {
			t.Fatal(err)
		}
		return it.t.Stats().PiecesComplete
	}
	deadline := time.Now().Add(20 * time.Second)
	for verified(eng) < minPieces && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	before := verified(eng)
	if before < minPieces || eng.List()[0].State == StateDone {
		t.Fatalf("expected a partial download, got %+v", eng.List()[0])
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := newEngine(t, stateDir, nil)
	defer reopened.Close()
	deadline = time.Now().Add(10 * time.Second)
	for verified(reopened) < before && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if after := verified(reopened); after < before {
		t.Fatalf("verified pieces lost on restart: %d before, %d after", before, after)
	}
}

func TestRemoveDataRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	cases := []metainfo.Info{
		{Name: "..", Length: 1},
		{Name: "pack", Files: []metainfo.FileInfo{{Path: []string{"..", "..", "x"}, Length: 1}}},
	}
	for _, info := range cases {
		if err := removeData(dir, &info); err == nil {
			t.Errorf("removeData(%+v) should refuse", info)
		}
	}
}

func TestRemoveDataDeletesOnlyTorrentFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pack/a.txt")
	write("pack/sub/b.txt")
	write("pack/notes.txt")
	write("single.iso")

	multi := metainfo.Info{Name: "pack", Files: []metainfo.FileInfo{
		{Path: []string{"a.txt"}, Length: 1},
		{Path: []string{"sub", "b.txt"}, Length: 1},
	}}
	if err := removeData(dir, &multi); err != nil {
		t.Fatalf("remove multi: %v", err)
	}
	single := metainfo.Info{Name: "single.iso", Length: 1}
	if err := removeData(dir, &single); err != nil {
		t.Fatalf("remove single: %v", err)
	}

	for _, gone := range []string{"pack/a.txt", "pack/sub", "single.iso"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be deleted (err=%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pack", "notes.txt")); err != nil {
		t.Errorf("user's own file was deleted: %v", err)
	}
}

func TestRejectsMagnetWithoutInfoHash(t *testing.T) {
	eng := newEngine(t, t.TempDir(), nil)
	defer eng.Close()
	for _, uri := range []string{"magnet:?dn=x", "magnet:?xt=urn:btmh:1220" + strings.Repeat("ab", 32)} {
		if _, err := eng.AddMagnet(uri, t.TempDir()); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected invalid, got %v", uri, err)
		}
	}
}

func TestMeter(t *testing.T) {
	start := time.Unix(0, 0)
	m := meter{}.sample(start, 0, 0)
	m = m.sample(start.Add(100*time.Millisecond), 999, 0)
	if m.downRate != 0 {
		t.Fatal("samples closer than minInterval must not update the rate")
	}
	m = m.sample(start.Add(2*time.Second), 2048, 1024)
	if m.downRate != 1024 || m.upRate != 512 {
		t.Fatalf("rates = %d/%d, want 1024/512", m.downRate, m.upRate)
	}
}
