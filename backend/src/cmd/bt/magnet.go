package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"trickle/internal/bt/dht"
	"trickle/internal/bt/magnet"
	"trickle/internal/bt/metadata"
	"trickle/internal/bt/torrentfile"
	"trickle/internal/bt/tracker"
)

var bootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
}

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
	peers, err := lookupDHT(link.InfoHash, 100)
	if err != nil {
		return err
	}
	fmt.Printf("%d peers from the DHT\n", len(peers))
	for _, p := range peers {
		fmt.Println(p)
	}
	return nil
}

func lookupDHT(infoHash [20]byte, want int) ([]netip.AddrPort, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	client, err := dht.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer client.Close()
	return client.FindPeers(ctx, infoHash, resolve(ctx, bootstrapNodes), want)
}

func resolve(ctx context.Context, hosts []string) []netip.AddrPort {
	var addrs []netip.AddrPort
	for _, hostport := range hosts {
		host, portText, err := net.SplitHostPort(hostport)
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			continue
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			addrs = append(addrs, netip.AddrPortFrom(ip, uint16(port)))
		}
	}
	return addrs
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
	peers := gatherPeers(link, peerID)
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
	announce := firstSupportedTracker(link.Trackers)
	tf, err := torrentfile.ParseInfo(info, announce)
	if err != nil {
		return err
	}
	saved := filepath.Base(tf.Name) + ".torrent"
	if err := os.WriteFile(saved, torrentBytes(info, announce), 0o644); err != nil {
		return err
	}
	fmt.Printf("got the metadata for %s, saved %s\n", tf.Name, saved)
	return downloadFrom(tf, peerID, peers)
}

func gatherPeers(link magnet.Link, peerID [20]byte) []netip.AddrPort {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		seen  = map[netip.AddrPort]bool{}
		peers []netip.AddrPort
	)
	add := func(found []netip.AddrPort) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range found {
			if !seen[p] {
				seen[p] = true
				peers = append(peers, p)
			}
		}
	}

	for _, tr := range link.Trackers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			res, err := tracker.Announce(ctx, http.DefaultClient, tr, tracker.Request{
				InfoHash: link.InfoHash, PeerID: peerID, Port: listenPort, Left: 1, Event: tracker.EventStarted,
			})
			if err == nil {
				add(res.Peers)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if found, err := lookupDHT(link.InfoHash, 100); err == nil {
			add(found)
		}
	}()
	wg.Wait()
	return peers
}

func firstSupportedTracker(trackers []string) string {
	for _, tr := range trackers {
		if strings.HasPrefix(tr, "http://") || strings.HasPrefix(tr, "https://") || strings.HasPrefix(tr, "udp://") {
			return tr
		}
	}
	return ""
}

// built by hand so the info dictionary keeps its exact bytes and therefore its hash
func torrentBytes(info []byte, announce string) []byte {
	head := "d"
	if announce != "" {
		head += fmt.Sprintf("8:announce%d:%s", len(announce), announce)
	}
	out := append([]byte(head+"4:info"), info...)
	return append(out, 'e')
}
