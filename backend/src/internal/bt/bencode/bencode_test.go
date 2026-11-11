package bencode

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"i42e", int64(42)},
		{"i-7e", int64(-7)},
		{"i0e", int64(0)},
		{"4:spam", "spam"},
		{"0:", ""},
		{"le", []any{}},
		{"de", map[string]any{}},
		{"l4:spami42ee", []any{"spam", int64(42)}},
		{"d3:cow3:moo4:spam4:eggse", map[string]any{"cow": "moo", "spam": "eggs"}},
		{"d4:listl1:ai1eee", map[string]any{"list": []any{"a", int64(1)}}},
	}
	for _, tc := range cases {
		got, err := Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("Decode(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Decode(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := []string{
		"", "x", "ie", "i03e", "i-0e", "i+5e", "i12", "i99999999999999999999e",
		"5:abc", "-1:a", "03:abc", "4spam",
		"l", "li1e", "d", "d3:fooe", "di1ei2ee", "d1:a1:b1:a1:ce",
		"i1ei2e",
		strings.Repeat("l", maxDepth+1) + strings.Repeat("e", maxDepth+1),
	}
	for _, in := range cases {
		if v, err := Decode([]byte(in)); !errors.Is(err, ErrSyntax) {
			t.Errorf("Decode(%q) = %#v, %v; want a syntax error", in, v, err)
		}
	}
}

func TestEncodeSortsKeys(t *testing.T) {
	got, err := Encode(map[string]any{"b": 1, "a": "x", "c": []any{int64(-2), []byte("yz")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "d1:a1:x1:bi1e1:cli-2e2:yzee"; string(got) != want {
		t.Fatalf("Encode = %q, want %q", got, want)
	}
	if _, err := Encode(1.5); err == nil {
		t.Fatal("floats are not bencode")
	}
}

func TestRawValue(t *testing.T) {
	data := []byte("d8:announce3:url4:infod6:lengthi5e4:name1:xe5:otheri1ee")
	raw, err := RawValue(data, "info")
	if err != nil {
		t.Fatal(err)
	}
	if want := "d6:lengthi5e4:name1:xe"; string(raw) != want {
		t.Fatalf("raw = %q, want %q", raw, want)
	}
	if _, err := RawValue(data, "missing"); err == nil {
		t.Fatal("missing key must fail")
	}
	if _, err := RawValue([]byte("l1:ae"), "info"); err == nil {
		t.Fatal("a list has no keys")
	}
}

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{"i42e", "4:spam", "l4:spami42ee", "d3:cow3:moo4:spam4:eggse", "d1:ald1:bi0eeee"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := Decode(data)
		if err != nil {
			return
		}
		encoded, err := Encode(v)
		if err != nil {
			t.Fatalf("decoded value does not encode: %v", err)
		}
		again, err := Decode(encoded)
		if err != nil || !reflect.DeepEqual(v, again) {
			t.Fatalf("round trip changed %q: %#v vs %#v (%v)", data, v, again, err)
		}
	})
}

func TestDecodePrefix(t *testing.T) {
	v, n, err := DecodePrefix([]byte("d8:msg_typei1e5:piecei0eeRAW DATA"))
	if err != nil || n != 25 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if v.(map[string]any)["msg_type"] != int64(1) {
		t.Fatalf("got %#v", v)
	}
}
