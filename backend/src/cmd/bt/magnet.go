package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"trickle/internal/bt/bencode"
	"trickle/internal/bt/magnet"
	"trickle/internal/bt/metadata"
	"trickle/internal/bt/torrentfile"
	"trickle/internal/bt/tracker"
)

func parseTarget(arg string) (magnet.Link, error) {
	if strings.HasPrefix(arg, "magnet:") {
		return magnet.Parse(arg)
	}
	raw, err := hex.DecodeString(arg)
	if err != nil || len(raw) != 20 {
		return magnet.Link{}, fmt.Errorf("expected a magnet link or a 40-character info hash")
	}
	var link magnet.Link
	copy(link.InfoHash[:], raw)
	return link, nil
}

func dhtPeers(arg string) error {
	link, err := parseTarget(arg)
	if err != nil {
		return err
	}
	peers, err := lookupDHT(context.Background(), link.InfoHash, dhtWant)
	if err != nil {
		return err
	}
	fmt.Printf("%d peers from the DHT\n", len(peers))
	for _, p := range peers {
		fmt.Println(p)
	}
	return nil
}

func fromMagnet(uri string) error {
	link, err := magnet.Parse(uri)
	if err != nil {
		return err
	}
	peerID, err := tracker.NewPeerID()
	if err != nil {
		return err
	}
	peers := gatherPeers(context.Background(), link.InfoHash, supportedTrackers(link.Trackers), peerID, 1)
	if len(peers) == 0 {
		return fmt.Errorf("found no peers for %x", link.InfoHash)
	}
	fmt.Printf("%d peers, asking them for the metadata\n", len(peers))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := metadata.FetchAny(ctx, peers, link.InfoHash, peerID)
	if err != nil {
		return err
	}
	tf, err := torrentfile.ParseInfo(info, supportedTrackers(link.Trackers)...)
	if err != nil {
		return err
	}
	saved := filepath.Base(tf.Name) + ".torrent"
	if err := os.WriteFile(saved, torrentBytes(info, tf.Trackers), 0o644); err != nil {
		return err
	}
	fmt.Printf("got the metadata for %s, saved %s\n", tf.Name, saved)
	return downloadFrom(tf, peerID, peers)
}

func supportedTrackers(trackers []string) []string {
	var ok []string
	for _, tr := range trackers {
		if strings.HasPrefix(tr, "http://") || strings.HasPrefix(tr, "https://") || strings.HasPrefix(tr, "udp://") {
			ok = append(ok, tr)
		}
	}
	return ok
}

// built by hand so the info dictionary keeps its exact bytes and therefore its hash
func torrentBytes(info []byte, trackers []string) []byte {
	out := []byte("d")
	if len(trackers) > 0 {
		out = fmt.Appendf(out, "8:announce%d:%s", len(trackers[0]), trackers[0])
	}
	if len(trackers) > 1 {
		tiers := make([]any, 0, len(trackers))
		for _, tr := range trackers {
			tiers = append(tiers, []any{tr})
		}
		list, _ := bencode.Encode(tiers)
		out = append(append(out, "13:announce-list"...), list...)
	}
	out = append(append(out, "4:info"...), info...)
	return append(out, 'e')
}
