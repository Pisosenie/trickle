package metadata

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	anacrolixbencode "github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"trickle/internal/bt/bencode"
	"trickle/internal/bt/peer"
	"trickle/internal/bt/torrentfile"
)

var ourID = [20]byte{'-', 'T', 'K'}

func seeder(t *testing.T, files int) (*metainfo.MetaInfo, netip.AddrPort) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "pack")
	for i := range files {
		data := make([]byte, 128<<10)
		rand.Read(data)
		path := filepath.Join(root, "part", string(rune('a'+i%26))+string(rune('a'+i/26))+".bin")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, data, 0o644)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := anacrolixbencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion()})
	cfg.Seed = true
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenPort = 0
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	st, _ := cl.AddTorrent(mi)
	<-st.GotInfo()
	st.VerifyData()
	<-st.Complete().On()
	return mi, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort()))
}

func TestFetchFromAnacrolix(t *testing.T) {
	mi, addr := seeder(t, 120)
	if len(mi.InfoBytes) <= pieceSize {
		t.Fatalf("want metadata over several pieces, got %d bytes", len(mi.InfoBytes))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := Fetch(ctx, addr, mi.HashInfoBytes(), ourID)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(got, mi.InfoBytes) {
		t.Fatal("metadata differs from the seeder's")
	}
	tf, err := torrentfile.ParseInfo(got)
	if err != nil || tf.InfoHash != mi.HashInfoBytes() || len(tf.Files) != 120 {
		t.Fatalf("parsed %d files, hash %x, err %v", len(tf.Files), tf.InfoHash, err)
	}
}

func TestFetchAnySkipsDeadPeers(t *testing.T) {
	mi, addr := seeder(t, 3)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := FetchAny(ctx, []netip.AddrPort{dead, addr}, mi.HashInfoBytes(), ourID)
	if err != nil || !bytes.Equal(got, mi.InfoBytes) {
		t.Fatalf("fetch any: %v", err)
	}
	if _, err := FetchAny(ctx, []netip.AddrPort{dead}, mi.HashInfoBytes(), ourID); err == nil {
		t.Fatal("no live peers must be an error")
	}
}

// fake answers the extended handshake and serves the given bytes as metadata
func fake(t *testing.T, infoHash [20]byte, extensions bool, served []byte) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		if _, err := peer.ReadHandshake(nc); err != nil {
			return
		}
		hs := peer.Handshake{InfoHash: infoHash, PeerID: [20]byte{'-', 'F', 'K'}}
		if extensions {
			hs.Reserved[5] = 0x10
		}
		nc.Write(hs.Bytes())
		for {
			m, err := peer.ReadMessage(nc)
			if err != nil {
				return
			}
			if m == nil || m.ID != peer.Extended {
				continue
			}
			if m.Payload[0] == extHandshake {
				hello, _ := bencode.Encode(map[string]any{"m": map[string]any{"ut_metadata": 3}, "metadata_size": len(served)})
				nc.Write((&peer.Message{ID: peer.Extended, Payload: append([]byte{0}, hello...)}).Bytes())
				continue
			}
			head, _ := bencode.Encode(map[string]any{"msg_type": msgData, "piece": 0, "total_size": len(served)})
			payload := append(append([]byte{localID}, head...), served...)
			nc.Write((&peer.Message{ID: peer.Extended, Payload: payload}).Bytes())
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func TestFetchRejectsBadPeers(t *testing.T) {
	hash := [20]byte{0x42}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Fetch(ctx, fake(t, hash, false, nil), hash, ourID); !errors.Is(err, ErrUnsupported) {
		t.Errorf("peer without extensions: %v", err)
	}
	if _, err := Fetch(ctx, fake(t, hash, true, []byte("d4:name1:xe")), hash, ourID); !errors.Is(err, peer.ErrProtocol) {
		t.Errorf("metadata with the wrong hash must be rejected: %v", err)
	}
}
