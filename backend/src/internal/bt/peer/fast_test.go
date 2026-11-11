package peer

import (
	"errors"
	"testing"
)

func TestConnBoundsPieceIndexes(t *testing.T) {
	c := &Conn{Fast: true, Pieces: 10}
	if err := c.Apply(NewHave(1 << 30)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a have far past the end must not grow the bitfield, got %v", err)
	}
	if err := c.Apply(NewAllowedFast(10)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("allowed fast past the end, got %v", err)
	}
	if err := c.Apply(&Message{ID: BitfieldMsg, Payload: []byte{0xff}}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a bitfield of the wrong size, got %v", err)
	}
	if c.Bitfield != nil || c.Allowed != nil {
		t.Fatalf("nothing should be recorded: %08b / %08b", c.Bitfield, c.Allowed)
	}
	unknown := &Conn{}
	if err := unknown.Apply(NewHave(1 << 30)); err != nil || unknown.Bitfield != nil {
		t.Fatalf("before the piece count is known a have is read and dropped: %v, %08b", err, unknown.Bitfield)
	}
}

func TestAllowedFastLetsRequestsThroughAChoke(t *testing.T) {
	c := &Conn{Fast: true, Pieces: 10, Choked: true}
	for _, m := range []*Message{NewAllowedFast(2), NewAllowedFast(7)} {
		if err := c.Apply(m); err != nil {
			t.Fatal(err)
		}
	}
	if !c.MayRequest(2) || !c.MayRequest(7) || c.MayRequest(3) {
		t.Fatalf("allowed %08b", c.Allowed)
	}
	if err := c.Apply(NewReject(2, 0, 16384)); err != nil || c.MayRequest(2) || !c.MayRequest(7) {
		t.Fatalf("a reject while choked takes the piece back: %v, allowed %08b", err, c.Allowed)
	}
	c.Apply(&Message{ID: Unchoke})
	if err := c.Apply(NewReject(7, 0, 16384)); err != nil || !c.Allowed.Has(7) || !c.MayRequest(3) {
		t.Fatalf("an unchoked reject leaves the grant alone: %v, allowed %08b", err, c.Allowed)
	}
}
