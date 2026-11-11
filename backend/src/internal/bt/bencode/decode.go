package bencode

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrSyntax = errors.New("bencode: syntax error")

const maxDepth = 64

type decoder struct {
	data  []byte
	pos   int
	depth int
}

func Decode(data []byte) (any, error) {
	d := decoder{data: data}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.pos != len(data) {
		return nil, d.errorf("trailing data")
	}
	return v, nil
}

func DecodePrefix(data []byte) (any, int, error) {
	d := decoder{data: data}
	v, err := d.value()
	if err != nil {
		return nil, 0, err
	}
	return v, d.pos, nil
}

func RawValue(data []byte, key string) ([]byte, error) {
	d := decoder{data: data}
	if len(data) == 0 || data[0] != 'd' {
		return nil, d.errorf("not a dictionary")
	}
	d.pos = 1
	for d.pos < len(data) && data[d.pos] != 'e' {
		k, err := d.key()
		if err != nil {
			return nil, err
		}
		start := d.pos
		if _, err := d.value(); err != nil {
			return nil, err
		}
		if k == key {
			return data[start:d.pos], nil
		}
	}
	return nil, fmt.Errorf("bencode: key %q not found", key)
}

func (d *decoder) errorf(format string, args ...any) error {
	return fmt.Errorf("%w at byte %d: %s", ErrSyntax, d.pos, fmt.Sprintf(format, args...))
}

func (d *decoder) value() (any, error) {
	if d.pos >= len(d.data) {
		return nil, d.errorf("unexpected end of input")
	}
	switch c := d.data[d.pos]; {
	case c == 'i':
		return d.integer()
	case c == 'l':
		return d.list()
	case c == 'd':
		return d.dict()
	case c >= '0' && c <= '9':
		return d.str()
	default:
		return nil, d.errorf("unexpected %q", c)
	}
}

func (d *decoder) integer() (int64, error) {
	end := bytes.IndexByte(d.data[d.pos:], 'e')
	if end < 0 {
		return 0, d.errorf("unterminated integer")
	}
	text := string(d.data[d.pos+1 : d.pos+end])
	if !canonicalInt(text) {
		return 0, d.errorf("bad integer %q", text)
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, d.errorf("integer %q out of range", text)
	}
	d.pos += end + 1
	return n, nil
}

// "0" is the only number allowed to start with 0; "-0" and "03" are invalid
func canonicalInt(s string) bool {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return false
	}
	return digits[0] != '0' || s == "0"
}

func (d *decoder) str() (string, error) {
	colon := bytes.IndexByte(d.data[d.pos:], ':')
	if colon < 0 {
		return "", d.errorf("missing ':' after string length")
	}
	text := string(d.data[d.pos : d.pos+colon])
	if !canonicalInt(text) || text[0] == '-' {
		return "", d.errorf("bad string length %q", text)
	}
	n, err := strconv.Atoi(text)
	start := d.pos + colon + 1
	if err != nil || n > len(d.data)-start {
		return "", d.errorf("string of length %s runs past the end", text)
	}
	d.pos = start + n
	return string(d.data[start:d.pos]), nil
}

func (d *decoder) key() (string, error) {
	if c := d.data[d.pos]; c < '0' || c > '9' {
		return "", d.errorf("dictionary key must be a string")
	}
	return d.str()
}

func (d *decoder) enter() error {
	d.depth++
	if d.depth > maxDepth {
		return d.errorf("nested deeper than %d", maxDepth)
	}
	d.pos++
	return nil
}

func (d *decoder) list() ([]any, error) {
	if err := d.enter(); err != nil {
		return nil, err
	}
	defer func() { d.depth-- }()

	items := []any{}
	for {
		if d.pos >= len(d.data) {
			return nil, d.errorf("unterminated list")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return items, nil
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
}

func (d *decoder) dict() (map[string]any, error) {
	if err := d.enter(); err != nil {
		return nil, err
	}
	defer func() { d.depth-- }()

	m := map[string]any{}
	for {
		if d.pos >= len(d.data) {
			return nil, d.errorf("unterminated dictionary")
		}
		if d.data[d.pos] == 'e' {
			d.pos++
			return m, nil
		}
		k, err := d.key()
		if err != nil {
			return nil, err
		}
		if _, dup := m[k]; dup {
			return nil, d.errorf("duplicate key %q", k)
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
}
