package peer

import (
	"errors"
	"fmt"
	"io"
)

const (
	protocol     = "BitTorrent protocol"
	handshakeLen = 1 + len(protocol) + 8 + 20 + 20
)

var ErrProtocol = errors.New("peer protocol error")

const extensionByte, extensionBit = 5, 0x10

type Handshake struct {
	Reserved [8]byte
	InfoHash [20]byte
	PeerID   [20]byte
}

func (h Handshake) Bytes() []byte {
	buf := make([]byte, 0, handshakeLen)
	buf = append(buf, byte(len(protocol)))
	buf = append(buf, protocol...)
	buf = append(buf, h.Reserved[:]...)
	buf = append(buf, h.InfoHash[:]...)
	return append(buf, h.PeerID[:]...)
}

func ReadHandshake(r io.Reader) (Handshake, error) {
	buf := make([]byte, handshakeLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Handshake{}, fmt.Errorf("read handshake: %w", err)
	}
	if int(buf[0]) != len(protocol) || string(buf[1:1+len(protocol)]) != protocol {
		return Handshake{}, fmt.Errorf("%w: not a BitTorrent handshake", ErrProtocol)
	}
	var h Handshake
	rest := buf[1+len(protocol):]
	copy(h.Reserved[:], rest[:8])
	copy(h.InfoHash[:], rest[8:28])
	copy(h.PeerID[:], rest[28:48])
	return h, nil
}
