package download

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"trickle/internal/bt/peer"
	"trickle/internal/bt/torrentfile"
)

const pieceLength = 64 << 10

var ourID = [20]byte{'-', 'T', 'K'}

type fixture struct {
	src   string
	files map[string][]byte
	mi    *metainfo.MetaInfo
	tf    torrentfile.File
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func newFixture(t *testing.T, multi bool) fixture {
	t.Helper()
	src := t.TempDir()
	files := map[string][]byte{"image.iso": randomBytes(1<<20 + 4321)}
	root := filepath.Join(src, "image.iso")
	if multi {
		files = map[string][]byte{
			"pack/readme.txt":      randomBytes(1000),
			"pack/disc/image.iso":  randomBytes(600 << 10),
			"pack/disc/extras.bin": randomBytes(200<<10 + 7),
		}
		root = filepath.Join(src, "pack")
	}
	for rel, data := range files {
		path := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	mi.Write(&buf)
	tf, err := torrentfile.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return fixture{src: src, files: files, mi: mi, tf: tf}
}

func (fx fixture) check(t *testing.T, dir string) {
	t.Helper()
	for rel, want := range fx.files {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs (err=%v)", rel, err)
		}
	}
}

func anacrolixClient(t *testing.T, dir string) *torrent.Client {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion()})
	cfg.Seed = true
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenPort = 0
	cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func seed(t *testing.T, fx fixture) netip.AddrPort {
	t.Helper()
	cl := anacrolixClient(t, fx.src)
	st, _ := cl.AddTorrent(fx.mi)
	<-st.GotInfo()
	st.VerifyData()
	<-st.Complete().On()
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort()))
}

// liar claims every piece and answers every request with zeros
func liar(t *testing.T, fx fixture, served *atomic.Int64) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	all := bytes.Repeat([]byte{0xff}, (len(fx.tf.Pieces)+7)/8)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				if _, err := peer.ReadHandshake(nc); err != nil {
					return
				}
				nc.Write(peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'L', 'I', 'E'}}.Bytes())
				nc.Write((&peer.Message{ID: peer.BitfieldMsg, Payload: all}).Bytes())
				nc.Write((&peer.Message{ID: peer.Unchoke}).Bytes())
				for {
					m, err := peer.ReadMessage(nc)
					if err != nil {
						return
					}
					if m == nil || m.ID != peer.Request {
						continue
					}
					payload := make([]byte, 8+binary.BigEndian.Uint32(m.Payload[8:12]))
					copy(payload, m.Payload[:8])
					served.Add(1)
					if _, err := nc.Write((&peer.Message{ID: peer.Piece, Payload: payload}).Bytes()); err != nil {
						return
					}
				}
			}()
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func download(t *testing.T, cfg Config) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	err := Download(ctx, cfg)
	return time.Since(started), err
}

func TestDownloadSurvivesALiar(t *testing.T) {
	fx := newFixture(t, false)
	var lies atomic.Int64
	out := t.TempDir()
	var last atomic.Int64
	_, err := download(t, Config{
		Torrent:  fx.tf,
		PeerID:   ourID,
		Peers:    []netip.AddrPort{liar(t, fx, &lies), seed(t, fx), seed(t, fx)},
		Dir:      out,
		Progress: func(done, total int) { last.Store(int64(done*1000 + total)) },
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	fx.check(t, out)
	if total := int64(len(fx.tf.Pieces)); last.Load() != total*1000+total {
		t.Fatalf("progress ended at %d", last.Load())
	}
	if lies.Load() == 0 {
		t.Fatal("the liar was never asked, so the SHA-1 check was not exercised")
	}
}

func TestDownloadMultiFile(t *testing.T) {
	fx := newFixture(t, true)
	out := t.TempDir()
	if _, err := download(t, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{seed(t, fx), seed(t, fx)}, Dir: out}); err != nil {
		t.Fatal(err)
	}
	fx.check(t, out)
}

func TestResumeKeepsVerifiedPieces(t *testing.T) {
	fx := newFixture(t, false)
	out := t.TempDir()
	partial := bytes.Clone(fx.files["image.iso"])
	clear(partial[3*pieceLength : 6*pieceLength])
	if err := os.WriteFile(filepath.Join(out, "image.iso"), partial, 0o644); err != nil {
		t.Fatal(err)
	}

	var first atomic.Int64
	first.Store(-1)
	_, err := download(t, Config{
		Torrent: fx.tf,
		PeerID:  ourID,
		Peers:   []netip.AddrPort{seed(t, fx)},
		Dir:     out,
		Progress: func(done, total int) {
			first.CompareAndSwap(-1, int64(done))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fx.check(t, out)
	if want := int64(len(fx.tf.Pieces) - 3); first.Load() != want {
		t.Fatalf("resume found %d good pieces, want %d", first.Load(), want)
	}
}

func TestCompleteDataNeedsNoPeers(t *testing.T) {
	fx := newFixture(t, true)
	if _, err := download(t, Config{Torrent: fx.tf, PeerID: ourID, Dir: fx.src}); err != nil {
		t.Fatalf("already complete data should finish without peers: %v", err)
	}
}

func TestDownloadLimit(t *testing.T) {
	fx := newFixture(t, false)
	elapsed, err := download(t, Config{
		Torrent:       fx.tf,
		PeerID:        ourID,
		Peers:         []netip.AddrPort{seed(t, fx)},
		Dir:           t.TempDir(),
		DownloadLimit: 512 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 800*time.Millisecond {
		t.Fatalf("1 MiB at 512 KiB/s took only %v", elapsed)
	}
}

func TestSeedServesAnacrolix(t *testing.T) {
	fx := newFixture(t, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	seeded := make(chan error, 1)
	go func() {
		seeded <- Seed(ctx, Config{Torrent: fx.tf, PeerID: ourID, Dir: fx.src, Listener: ln})
	}()

	leechDir := t.TempDir()
	leecher := anacrolixClient(t, leechDir)
	lt, _ := leecher.AddTorrent(fx.mi)
	<-lt.GotInfo()
	lt.AddPeers([]torrent.PeerInfo{{Addr: ln.Addr().(*net.TCPAddr)}})
	lt.DownloadAll()
	select {
	case <-lt.Complete().On():
	case <-time.After(20 * time.Second):
		t.Fatalf("anacrolix did not finish: %d of %d bytes", lt.BytesCompleted(), lt.Length())
	}
	fx.check(t, leechDir)

	cancel()
	if err := <-seeded; err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestSeedRefusesIncompleteData(t *testing.T) {
	fx := newFixture(t, false)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	err := Seed(context.Background(), Config{Torrent: fx.tf, PeerID: ourID, Dir: t.TempDir(), Listener: ln})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected a missing-pieces error, got %v", err)
	}
}

func TestDownloadFailsWithoutPeers(t *testing.T) {
	fx := newFixture(t, false)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()

	_, err := download(t, Config{Torrent: fx.tf, Peers: []netip.AddrPort{closed}, Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "all peers are gone") {
		t.Fatalf("expected all-peers-gone error, got %v", err)
	}
}

// moody serves real data but chokes on the first request and drops everything that was in flight
func moody(t *testing.T, fx fixture) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	data := fx.files["image.iso"]
	inFlight := min(maxBacklog, pieceLength/blockSize)
	all := bytes.Repeat([]byte{0xff}, (len(fx.tf.Pieces)+7)/8)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		if _, err := peer.ReadHandshake(nc); err != nil {
			return
		}
		nc.Write(peer.Handshake{InfoHash: fx.tf.InfoHash, PeerID: [20]byte{'-', 'M', 'O'}}.Bytes())
		nc.Write((&peer.Message{ID: peer.BitfieldMsg, Payload: all}).Bytes())
		nc.Write((&peer.Message{ID: peer.Unchoke}).Bytes())
		requests := 0
		for {
			m, err := peer.ReadMessage(nc)
			if err != nil {
				return
			}
			if m == nil || m.ID != peer.Request {
				continue
			}
			requests++
			if requests == 1 {
				nc.Write((&peer.Message{ID: peer.Choke}).Bytes())
			}
			if requests < inFlight {
				continue
			}
			if requests == inFlight {
				nc.Write((&peer.Message{ID: peer.Unchoke}).Bytes())
				continue
			}
			req, _ := peer.ParseRequest(m)
			off := req.Index*pieceLength + req.Begin
			if _, err := nc.Write(peer.NewPiece(req.Index, req.Begin, data[off:off+req.Length]).Bytes()); err != nil {
				return
			}
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func TestChokeMidPieceDoesNotStall(t *testing.T) {
	fx := newFixture(t, false)
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Download(ctx, Config{Torrent: fx.tf, PeerID: ourID, Peers: []netip.AddrPort{moody(t, fx)}, Dir: out}); err != nil {
		t.Fatalf("download after a choke: %v", err)
	}
	fx.check(t, out)
}
