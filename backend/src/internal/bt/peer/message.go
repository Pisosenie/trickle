package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

type ID uint8

const (
	Choke ID = iota
	Unchoke
	Interested
	NotInterested
	Have
	BitfieldMsg
	Request
	Piece
	Cancel
	Extended ID = 20
)

func (id ID) String() string {
	names := [...]string{"choke", "unchoke", "interested", "not interested", "have", "bitfield", "request", "piece", "cancel"}
	if int(id) < len(names) {
		return names[id]
	}
	if id == Extended {
		return "extended"
	}
	return fmt.Sprintf("unknown(%d)", uint8(id))
}

const maxMessage = 1 << 20

type Message struct {
	ID      ID
	Payload []byte
}

// a nil message is a keep-alive
func ReadMessage(r io.Reader) (*Message, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 {
		return nil, nil
	}
	if length > maxMessage {
		return nil, fmt.Errorf("%w: message of %d bytes", ErrProtocol, length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return &Message{ID: ID(buf[0]), Payload: buf[1:]}, nil
}

func (m *Message) Bytes() []byte {
	if m == nil {
		return make([]byte, 4)
	}
	buf := make([]byte, 4, 5+len(m.Payload))
	binary.BigEndian.PutUint32(buf, uint32(1+len(m.Payload)))
	buf = append(buf, byte(m.ID))
	return append(buf, m.Payload...)
}

func NewRequest(index, begin, length int) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:], uint32(index))
	binary.BigEndian.PutUint32(payload[4:], uint32(begin))
	binary.BigEndian.PutUint32(payload[8:], uint32(length))
	return &Message{ID: Request, Payload: payload}
}

func NewHave(index int) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(index))
	return &Message{ID: Have, Payload: payload}
}

func ParseHave(m *Message) (int, error) {
	if m.ID != Have || len(m.Payload) != 4 {
		return 0, fmt.Errorf("%w: malformed have", ErrProtocol)
	}
	return int(binary.BigEndian.Uint32(m.Payload)), nil
}

type Block struct {
	Index int
	Begin int
	Data  []byte
}

func ParsePiece(m *Message) (Block, error) {
	if m.ID != Piece || len(m.Payload) < 8 {
		return Block{}, fmt.Errorf("%w: malformed piece", ErrProtocol)
	}
	return Block{
		Index: int(binary.BigEndian.Uint32(m.Payload[0:4])),
		Begin: int(binary.BigEndian.Uint32(m.Payload[4:8])),
		Data:  m.Payload[8:],
	}, nil
}

type BlockRequest struct {
	Index  int
	Begin  int
	Length int
}

func ParseRequest(m *Message) (BlockRequest, error) {
	if (m.ID != Request && m.ID != Cancel) || len(m.Payload) != 12 {
		return BlockRequest{}, fmt.Errorf("%w: malformed request", ErrProtocol)
	}
	return BlockRequest{
		Index:  int(binary.BigEndian.Uint32(m.Payload[0:4])),
		Begin:  int(binary.BigEndian.Uint32(m.Payload[4:8])),
		Length: int(binary.BigEndian.Uint32(m.Payload[8:12])),
	}, nil
}

func NewPiece(index, begin int, data []byte) *Message {
	payload := make([]byte, 8, 8+len(data))
	binary.BigEndian.PutUint32(payload[0:], uint32(index))
	binary.BigEndian.PutUint32(payload[4:], uint32(begin))
	return &Message{ID: Piece, Payload: append(payload, data...)}
}

func NewCancel(index, begin, length int) *Message {
	return &Message{ID: Cancel, Payload: NewRequest(index, begin, length).Payload}
}
