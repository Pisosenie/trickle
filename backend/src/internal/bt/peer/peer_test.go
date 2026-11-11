package peer

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
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

var (
	testHash = [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	testID   = [20]byte{'-', 'T', 'K', '0', '0', '0', '1', '-'}
)

func TestHandshakeRoundTrip(t *testing.T) {
	sent := Handshake{Reserved: [8]byte{7: 0x04}, InfoHash: testHash, PeerID: testID}
	data := sent.Bytes()
	if len(data) != handshakeLen || data[0] != 19 {
		t.Fatalf("handshake is %d bytes, starts with %d", len(data), data[0])
	}
	got, err := ReadHandshake(bytes.NewReader(data))
	if err != nil || got != sent {
		t.Fatalf("got %+v, %v", got, err)
	}
	broken := append([]byte{18}, data[1:]...)
	if _, err := ReadHandshake(bytes.NewReader(broken)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong protocol length should fail, got %v", err)
	}
	if _, err := ReadHandshake(bytes.NewReader(data[:30])); err == nil {
		t.Fatal("short handshake should fail")
	}
}

func TestMessages(t *testing.T) {
	var keepAlive *Message
	if got, err := ReadMessage(bytes.NewReader(keepAlive.Bytes())); got != nil || err != nil {
		t.Fatalf("keep-alive: %+v, %v", got, err)
	}

	req, err := ReadMessage(bytes.NewReader(NewRequest(3, 16384, 16384).Bytes()))
	if err != nil || req.ID != Request || len(req.Payload) != 12 {
		t.Fatalf("request: %+v, %v", req, err)
	}

	have, _ := ReadMessage(bytes.NewReader(NewHave(42).Bytes()))
	if index, err := ParseHave(have); index != 42 || err != nil {
		t.Fatalf("have: %d, %v", index, err)
	}

	piece := &Message{ID: Piece, Payload: append([]byte{0, 0, 0, 5, 0, 0, 0x40, 0}, "data"...)}
	block, err := ParsePiece(piece)
	if err != nil || block.Index != 5 || block.Begin != 16384 || string(block.Data) != "data" {
		t.Fatalf("piece: %+v, %v", block, err)
	}
	if _, err := ParsePiece(&Message{ID: Piece, Payload: []byte{1}}); !errors.Is(err, ErrProtocol) {
		t.Fatal("short piece must fail")
	}

	huge := []byte{0x7f, 0xff, 0xff, 0xff}
	if _, err := ReadMessage(bytes.NewReader(huge)); !errors.Is(err, ErrProtocol) {
		t.Fatal("oversized message must fail")
	}
	if Unchoke.String() != "unchoke" || ID(99).String() != "unknown(99)" {
		t.Fatal("message names are wrong")
	}
}

func TestRequestAndPieceMessages(t *testing.T) {
	req, err := ParseRequest(NewRequest(7, 32768, 16384))
	if err != nil || req != (BlockRequest{Index: 7, Begin: 32768, Length: 16384}) {
		t.Fatalf("request: %+v, %v", req, err)
	}
	if _, err := ParseRequest(&Message{ID: Request, Payload: []byte{1, 2}}); !errors.Is(err, ErrProtocol) {
		t.Fatal("short request must fail")
	}
	block, err := ParsePiece(NewPiece(7, 32768, []byte("abc")))
	if err != nil || block.Index != 7 || block.Begin != 32768 || string(block.Data) != "abc" {
		t.Fatalf("piece: %+v, %v", block, err)
	}
}

func TestAcceptAnswersOnlyMatchingTorrent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asked [20]byte
		ok    bool
	}{
		{"same torrent", testHash, true},
		{"other torrent", [20]byte{0xee}, false},
	} {
		dialer, acceptor := net.Pipe()
		accepted := make(chan error, 1)
		go func() {
			c, err := Accept(context.Background(), acceptor, testHash, [20]byte{'-', 'S', 'D'})
			if err == nil && c.PeerID != testID {
				err = errors.New("wrong peer id")
			}
			if err != nil {
				acceptor.Close()
			}
			accepted <- err
		}()
		c, err := handshake(context.Background(), dialer, tc.asked, testID)
		acceptErr := <-accepted
		if tc.ok && (err != nil || acceptErr != nil || c.PeerID[1] != 'S') {
			t.Errorf("%s: dial %v, accept %v", tc.name, err, acceptErr)
		}
		if !tc.ok && (!errors.Is(acceptErr, ErrProtocol) || err == nil) {
			t.Errorf("%s: dial %v, accept %v", tc.name, err, acceptErr)
		}
		dialer.Close()
		acceptor.Close()
	}
}

func TestFastExtensionMessages(t *testing.T) {
	rej, err := ParseRequest(NewReject(3, 16384, 16384))
	if err != nil || rej != (BlockRequest{Index: 3, Begin: 16384, Length: 16384}) {
		t.Fatalf("reject: %+v, %v", rej, err)
	}
	if HaveAll.String() != "have all" || Reject.String() != "reject" {
		t.Fatal("fast extension message names")
	}
	plain := &Conn{}
	if err := plain.Apply(&Message{ID: HaveAll}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("have all from a peer without the fast bit should be a protocol error, got %v", err)
	}
	dialer, acceptor := net.Pipe()
	defer dialer.Close()
	defer acceptor.Close()
	go Accept(context.Background(), acceptor, testHash, [20]byte{'-', 'S', 'D'})
	c, err := handshake(context.Background(), dialer, testHash, testID)
	if err != nil || !c.Fast || !c.Extensions {
		t.Fatalf("both sides advertise the fast extension: fast=%v ext=%v err=%v", c != nil && c.Fast, c != nil && c.Extensions, err)
	}
}

func TestBitfield(t *testing.T) {
	b := Bitfield{0b1010_0000}
	if !b.Has(0) || b.Has(1) || !b.Has(2) || b.Has(9) || b.Has(-1) {
		t.Fatal("Has is wrong")
	}
	grown := b.With(9)
	if !grown.Has(9) || b.Has(9) || len(b) != 1 || grown.Count() != 3 {
		t.Fatalf("With must return a new, grown bitfield: %08b / %08b", b, grown)
	}
	full := Full(10)
	if full.Count() != 10 || full.Has(10) || len(full) != 2 {
		t.Fatalf("Full(10) = %08b", full)
	}
	rest := full.Minus(Bitfield{0b1000_0000, 0b0100_0000})
	if rest.Count() != 8 || rest.Has(0) || rest.Has(9) || full.Count() != 10 {
		t.Fatalf("Minus = %08b, original %08b", rest, full)
	}
}

func fakePeer(t *testing.T, infoHash [20]byte, script ...*Message) net.Conn {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	go func() {
		if _, err := ReadHandshake(theirs); err != nil {
			return
		}
		theirs.Write(Handshake{InfoHash: infoHash, PeerID: [20]byte{'-', 'F', 'K'}}.Bytes())
		for _, m := range script {
			if _, err := theirs.Write(m.Bytes()); err != nil {
				return
			}
		}
	}()
	return ours
}

func TestConnTracksPeerState(t *testing.T) {
	nc := fakePeer(t, testHash,
		&Message{ID: BitfieldMsg, Payload: []byte{0b1010_0000}},
		NewHave(3),
		&Message{ID: Unchoke},
	)
	c, err := handshake(context.Background(), nc, testHash, testID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Choked || c.PeerID[1] != 'F' {
		t.Fatalf("fresh connection: %+v", c)
	}
	for range 3 {
		if _, err := c.Read(); err != nil {
			t.Fatal(err)
		}
	}
	if c.Choked || !c.Bitfield.Has(0) || !c.Bitfield.Has(2) || !c.Bitfield.Has(3) || c.Bitfield.Has(1) {
		t.Fatalf("state not applied: choked=%v bitfield=%08b", c.Choked, c.Bitfield)
	}
}

func TestHandshakeRejectsOtherTorrent(t *testing.T) {
	nc := fakePeer(t, [20]byte{0xff})
	if _, err := handshake(context.Background(), nc, testHash, testID); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected ErrProtocol, got %v", err)
	}
}

func TestDownloadBlockFromAnacrolix(t *testing.T) {
	dir := t.TempDir()
	content := make([]byte, 300<<10)
	rand.Read(content)
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := bencode.Marshal(info)
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion()})
	cfg.Seed = true
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenPort = 0
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seeder.Close()
	st, _ := seeder.AddTorrent(mi)
	<-st.GotInfo()
	st.VerifyData()
	<-st.Complete().On()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addr := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(seeder.LocalPort()))
	c, err := Dial(ctx, addr, mi.HashInfoBytes(), testID)
	if err != nil {
		t.Fatalf("dial seeder: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))

	if err := c.Send(&Message{ID: Interested}); err != nil {
		t.Fatal(err)
	}
	for c.Choked {
		if _, err := c.Read(); err != nil {
			t.Fatalf("waiting for unchoke: %v", err)
		}
	}
	if !c.Has(0) || !c.Has(1) {
		t.Fatalf("seeder should have both pieces (bitfield %08b, have all %v)", c.Bitfield, c.All)
	}

	const blockSize = 16 << 10
	if err := c.Send(NewRequest(1, blockSize, blockSize)); err != nil {
		t.Fatal(err)
	}
	for {
		m, err := c.Read()
		if err != nil {
			t.Fatalf("waiting for piece: %v", err)
		}
		if m == nil || m.ID != Piece {
			continue
		}
		block, err := ParsePiece(m)
		if err != nil {
			t.Fatal(err)
		}
		start := 256<<10 + blockSize
		if block.Index != 1 || block.Begin != blockSize || !bytes.Equal(block.Data, content[start:start+blockSize]) {
			t.Fatalf("wrong block: index %d begin %d, %d bytes", block.Index, block.Begin, len(block.Data))
		}
		return
	}
}
