//go:build cgo

package utp

import (
	"context"
	"net"
	"testing"
	"time"

	libutp "github.com/anacrolix/go-libutp"
)

func reference(t *testing.T) *libutp.Socket {
	t.Helper()
	s, err := libutp.NewSocket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWeDialLibutp(t *testing.T) {
	for name, loss := range map[string]float64{"clean": 0, "lossy": 0.05} {
		t.Run(name, func(t *testing.T) {
			theirs := reference(t)
			accepted := make(chan net.Conn, 1)
			go func() {
				if c, err := theirs.Accept(); err == nil {
					accepted <- c
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ours, err := socket(t, loss, loss).Dial(ctx, addrOf(t, theirs.Addr()))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case c := <-accepted:
				exchange(t, ours, c, 2<<20)
			case <-ctx.Done():
				t.Fatal("libutp accepted nothing")
			}
		})
	}
}

func TestLibutpDialsUs(t *testing.T) {
	ours := socket(t, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialed := make(chan net.Conn, 1)
	go func() {
		if c, err := reference(t).DialContext(ctx, "udp", ours.Addr().String()); err == nil {
			dialed <- c
		}
	}()
	c, err := ours.Accept()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case theirs := <-dialed:
		exchange(t, c, theirs, 2<<20)
	case <-ctx.Done():
		t.Fatal("libutp could not dial us")
	}
}
