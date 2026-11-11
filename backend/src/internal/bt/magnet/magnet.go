package magnet

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalid = errors.New("invalid magnet link")

type Link struct {
	InfoHash [20]byte
	Name     string
	Trackers []string
}

func Parse(uri string) (Link, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "magnet" {
		return Link{}, fmt.Errorf("%w: not a magnet: URI", ErrInvalid)
	}
	q := u.Query()
	for _, xt := range q["xt"] {
		encoded, ok := strings.CutPrefix(xt, "urn:btih:")
		if !ok {
			continue
		}
		hash, err := decodeHash(encoded)
		if err != nil {
			return Link{}, err
		}
		return Link{InfoHash: hash, Name: q.Get("dn"), Trackers: q["tr"]}, nil
	}
	return Link{}, fmt.Errorf("%w: no urn:btih info hash", ErrInvalid)
}

// 40 hex characters, or the older 32-character base32 form
func decodeHash(s string) ([20]byte, error) {
	var hash [20]byte
	var raw []byte
	var err error
	switch len(s) {
	case 40:
		raw, err = hex.DecodeString(s)
	case 32:
		raw, err = base32.StdEncoding.DecodeString(strings.ToUpper(s))
	default:
		return hash, fmt.Errorf("%w: info hash %q has the wrong length", ErrInvalid, s)
	}
	if err != nil {
		return hash, fmt.Errorf("%w: info hash %q: %v", ErrInvalid, s, err)
	}
	copy(hash[:], raw)
	return hash, nil
}
