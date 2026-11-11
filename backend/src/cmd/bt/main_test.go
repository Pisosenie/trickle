package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"

	"trickle/internal/bt/peer"
	"trickle/internal/bt/torrentfile"
)

func TestHelloReadsHaveAll(t *testing.T) {
	tf := torrentfile.File{InfoHash: [20]byte{1}, Pieces: make([][20]byte, 3)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		c, err := peer.Accept(context.Background(), nc, tf.InfoHash, [20]byte{'-', 'q', 'B', '4', '6', '0', '0', '-'})
		if err != nil {
			return
		}
		c.Send(&peer.Message{ID: peer.HaveAll})
		c.Read()
	}()
	p := hello(netip.MustParseAddrPort(ln.Addr().String()), tf, [20]byte{'-', 'T', 'K'})
	if !p.ok || !strings.Contains(p.line, "has all 3 pieces") {
		t.Fatalf("probe = %+v", p)
	}
}
