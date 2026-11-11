package bencode

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encode(buf *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case int:
		writeInt(buf, int64(v))
	case int64:
		writeInt(buf, v)
	case string:
		writeString(buf, v)
	case []byte:
		writeString(buf, string(v))
	case []any:
		buf.WriteByte('l')
		for _, item := range v {
			if err := encode(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[string]any:
		buf.WriteByte('d')
		for _, key := range slices.Sorted(maps.Keys(v)) {
			writeString(buf, key)
			if err := encode(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	default:
		return fmt.Errorf("bencode: cannot encode %T", v)
	}
	return nil
}

func writeInt(buf *bytes.Buffer, n int64) {
	buf.WriteByte('i')
	buf.WriteString(strconv.FormatInt(n, 10))
	buf.WriteByte('e')
}

func writeString(buf *bytes.Buffer, s string) {
	buf.WriteString(strconv.Itoa(len(s)))
	buf.WriteByte(':')
	buf.WriteString(s)
}
