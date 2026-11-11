package peer

import "math/bits"

type Bitfield []byte

func (b Bitfield) Has(index int) bool {
	byteIndex := index / 8
	if index < 0 || byteIndex >= len(b) {
		return false
	}
	return b[byteIndex]>>(7-index%8)&1 == 1
}

func (b Bitfield) With(index int) Bitfield {
	byteIndex := index / 8
	next := make(Bitfield, max(len(b), byteIndex+1))
	copy(next, b)
	next[byteIndex] |= 1 << (7 - index%8)
	return next
}

func (b Bitfield) Count() int {
	n := 0
	for _, c := range b {
		n += bits.OnesCount8(c)
	}
	return n
}
