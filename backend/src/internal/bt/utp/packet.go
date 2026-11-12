package utp

import (
	"encoding/binary"
	"errors"
)

const (
	headerSize = 20
	version    = 1
	extSack    = 1
)

type packetType uint8

const (
	stData packetType = iota
	stFin
	stState
	stReset
	stSyn
)

var errMalformed = errors.New("malformed utp packet")

type packet struct {
	typ       packetType
	connID    uint16
	timestamp uint32
	tsDiff    uint32
	wnd       uint32
	seq       uint16
	ack       uint16
	sack      []byte
	payload   []byte
}

func (p packet) marshal() []byte {
	ext := byte(0)
	size := headerSize + len(p.payload)
	if len(p.sack) > 0 {
		ext = extSack
		size += 2 + len(p.sack)
	}
	b := make([]byte, headerSize, size)
	b[0] = byte(p.typ)<<4 | version
	b[1] = ext
	binary.BigEndian.PutUint16(b[2:], p.connID)
	binary.BigEndian.PutUint32(b[4:], p.timestamp)
	binary.BigEndian.PutUint32(b[8:], p.tsDiff)
	binary.BigEndian.PutUint32(b[12:], p.wnd)
	binary.BigEndian.PutUint16(b[16:], p.seq)
	binary.BigEndian.PutUint16(b[18:], p.ack)
	if ext != 0 {
		b = append(b, 0, byte(len(p.sack)))
		b = append(b, p.sack...)
	}
	return append(b, p.payload...)
}

// extensions form a chain: the header names the first, each one names the next
func parsePacket(b []byte) (packet, error) {
	if len(b) < headerSize || b[0]&0x0f != version || packetType(b[0]>>4) > stSyn {
		return packet{}, errMalformed
	}
	p := packet{
		typ:       packetType(b[0] >> 4),
		connID:    binary.BigEndian.Uint16(b[2:]),
		timestamp: binary.BigEndian.Uint32(b[4:]),
		tsDiff:    binary.BigEndian.Uint32(b[8:]),
		wnd:       binary.BigEndian.Uint32(b[12:]),
		seq:       binary.BigEndian.Uint16(b[16:]),
		ack:       binary.BigEndian.Uint16(b[18:]),
	}
	kind, rest := b[1], b[headerSize:]
	for kind != 0 {
		if len(rest) < 2 || len(rest) < 2+int(rest[1]) {
			return packet{}, errMalformed
		}
		size := int(rest[1])
		if kind == extSack && size > 0 {
			p.sack = rest[2 : 2+size]
		}
		kind, rest = rest[0], rest[2+size:]
	}
	p.payload = rest
	return p, nil
}

// bit i of the selective ack mask stands for ack+2+i, least significant bit first
func (p packet) sacks(seq uint16) bool {
	i := int(seq - p.ack - 2)
	return i < len(p.sack)*8 && p.sack[i/8]&(1<<(i%8)) != 0
}

func seqLess(a, b uint16) bool {
	return int16(a-b) < 0
}
