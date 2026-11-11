package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"time"

	"trickle/internal/bt/download"
	"trickle/internal/bt/peer"
	"trickle/internal/bt/torrentfile"
	"trickle/internal/bt/tracker"
)

const (
	usage = `usage:
  bt info      FILE.torrent   show what is inside a .torrent
  bt peers     FILE.torrent   ask every tracker in the torrent for peers
  bt handshake FILE.torrent   say hello to the peers the tracker returned
  bt download  FILE.torrent   download into the current directory, continuing where it stopped
  bt seed      FILE.torrent   share the downloaded files until Ctrl+C
  bt dht       MAGNET|HASH    find peers through the DHT, no tracker needed
  bt magnet    MAGNET         fetch the metadata from peers, save a .torrent, then download`
	maxProbes  = 30
	listenAddr = ":6881"
	listenPort = 6881
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "bt:", err)
		os.Exit(1)
	}
}

func run(command, arg string) error {
	switch command {
	case "dht":
		return dhtPeers(arg)
	case "magnet":
		return fromMagnet(arg)
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return err
	}
	tf, err := torrentfile.Parse(data)
	if err != nil {
		return err
	}
	switch command {
	case "info":
		printInfo(tf)
		return nil
	case "peers":
		return printPeers(tf)
	case "handshake":
		return printHandshakes(tf)
	case "download":
		return downloadFile(tf)
	case "seed":
		return seedFile(tf)
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usage)
	}
}

func printInfo(tf torrentfile.File) {
	fmt.Printf("name:         %s\n", tf.Name)
	fmt.Printf("size:         %d bytes\n", tf.Length)
	fmt.Printf("piece length: %d bytes\n", tf.PieceLength)
	fmt.Printf("pieces:       %d\n", len(tf.Pieces))
	fmt.Printf("info hash:    %s\n", hex.EncodeToString(tf.InfoHash[:]))
	fmt.Printf("tracker:      %s\n", tf.Announce)
}

func printPeers(tf torrentfile.File) error {
	peerID, err := tracker.NewPeerID()
	if err != nil {
		return err
	}
	peers, answered := announceAll(context.Background(), tf.InfoHash, tf.Trackers, peerID, tf.Length)
	fmt.Printf("%d peers from %d of %d trackers\n", len(peers), answered, len(tf.Trackers))
	for _, p := range peers {
		fmt.Println(p)
	}
	return nil
}

type probe struct {
	line string
	ok   bool
}

func printHandshakes(tf torrentfile.File) error {
	peerID, err := tracker.NewPeerID()
	if err != nil {
		return err
	}
	found, _ := announceAll(context.Background(), tf.InfoHash, tf.Trackers, peerID, tf.Length)
	peers := found[:min(len(found), maxProbes)]

	results := make(chan probe, len(peers))
	var wg sync.WaitGroup
	for _, addr := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- hello(addr, tf, peerID)
		}()
	}
	wg.Wait()
	close(results)

	answered := 0
	for r := range results {
		fmt.Println(r.line)
		if r.ok {
			answered++
		}
	}
	fmt.Printf("%d of %d peers answered\n", answered, len(peers))
	return nil
}

func hello(addr netip.AddrPort, tf torrentfile.File, peerID [20]byte) probe {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := peer.Dial(ctx, addr, tf.InfoHash, peerID)
	if err != nil {
		return probe{line: fmt.Sprintf("%-40s  no answer: %v", addr, err)}
	}
	defer c.Close()

	have := "sent no bitfield"
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if m, err := c.Read(); err == nil && m != nil && m.ID == peer.BitfieldMsg {
		have = fmt.Sprintf("has %d of %d pieces", c.Bitfield.Count(), len(tf.Pieces))
	}
	return probe{line: fmt.Sprintf("%-40s  %s  %s", addr, clientName(c.PeerID), have), ok: true}
}

func clientName(id [20]byte) string {
	name := []byte(string(id[:8]))
	for i, c := range name {
		if c < 0x20 || c > 0x7e {
			name[i] = '.'
		}
	}
	return string(name)
}

func downloadFile(tf torrentfile.File) error {
	peerID, err := tracker.NewPeerID()
	if err != nil {
		return err
	}
	peers := gatherPeers(context.Background(), tf.InfoHash, tf.Trackers, peerID, tf.Length)
	fmt.Printf("%s: %d peers from %d trackers and the DHT\n", tf.Name, len(peers), len(tf.Trackers))
	return downloadFrom(tf, peerID, peers)
}

func downloadFrom(tf torrentfile.File, peerID [20]byte, peers []netip.AddrPort) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Printf("not accepting incoming peers: %v\n", err)
		ln = nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	started := time.Now()
	err = download.Download(ctx, download.Config{
		Torrent:   tf,
		PeerID:    peerID,
		Peers:     peers,
		MorePeers: refill(ctx, tf, peerID),
		Dir:       ".",
		MaxPeers:  maxProbes,
		Listener:  ln,
		Progress: func(done, total int) {
			fmt.Printf("\r%5.1f%%  %d/%d pieces", float64(done)*100/float64(total), done, total)
		},
	})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("done in %v\n", time.Since(started).Round(time.Second))
	return nil
}

func seedFile(tf torrentfile.File) error {
	peerID, err := tracker.NewPeerID()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if _, answered := announceAll(context.Background(), tf.InfoHash, tf.Trackers, peerID, 0); answered == 0 {
		fmt.Println("no tracker took the announce; peers can still find us through others")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("checking %s, then sharing it on %s until Ctrl+C\n", tf.Name, listenAddr)
	return download.Seed(ctx, download.Config{Torrent: tf, PeerID: peerID, Dir: ".", Listener: ln})
}
