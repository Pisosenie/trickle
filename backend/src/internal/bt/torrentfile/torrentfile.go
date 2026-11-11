package torrentfile

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"trickle/internal/bt/bencode"
)

const hashLen = sha1.Size

var ErrInvalid = errors.New("invalid torrent file")

type Entry struct {
	Path   []string
	Length int64
}

type File struct {
	Announce    string
	InfoHash    [hashLen]byte
	Name        string
	Length      int64
	PieceLength int64
	Pieces      [][hashLen]byte
	Files       []Entry
}

func Parse(data []byte) (File, error) {
	root, err := bencode.Decode(data)
	if err != nil {
		return File{}, invalid("%v", err)
	}
	top, ok := root.(map[string]any)
	if !ok {
		return File{}, invalid("top level is not a dictionary")
	}
	rawInfo, err := bencode.RawValue(data, "info")
	if err != nil {
		return File{}, invalid("%v", err)
	}
	announce, _ := top["announce"].(string)
	return ParseInfo(rawInfo, announce)
}

func ParseInfo(rawInfo []byte, announce string) (File, error) {
	decoded, err := bencode.Decode(rawInfo)
	if err != nil {
		return File{}, invalid("%v", err)
	}
	info, ok := decoded.(map[string]any)
	if !ok {
		return File{}, invalid("no info dictionary")
	}
	name, _ := info["name"].(string)
	pieceLength, _ := info["piece length"].(int64)
	pieces, _ := info["pieces"].(string)
	if !safeName(name) {
		return File{}, invalid("missing or unsafe name %q", name)
	}
	if pieceLength <= 0 || len(pieces)%hashLen != 0 {
		return File{}, invalid("bad piece length or piece hashes")
	}

	files, err := entries(info, name)
	if err != nil {
		return File{}, err
	}
	var length int64
	for _, f := range files {
		length += f.Length
	}
	if length <= 0 {
		return File{}, invalid("torrent has no data")
	}
	if want := (length + pieceLength - 1) / pieceLength; int64(len(pieces)/hashLen) != want {
		return File{}, invalid("%d piece hashes for %d pieces", len(pieces)/hashLen, want)
	}

	return File{
		Announce:    announce,
		InfoHash:    sha1.Sum(rawInfo),
		Name:        name,
		Length:      length,
		PieceLength: pieceLength,
		Pieces:      splitHashes(pieces),
		Files:       files,
	}, nil
}

func entries(info map[string]any, name string) ([]Entry, error) {
	list, multi := info["files"].([]any)
	if !multi {
		length, _ := info["length"].(int64)
		if length <= 0 {
			return nil, invalid("length must be positive")
		}
		return []Entry{{Path: []string{name}, Length: length}}, nil
	}

	files := make([]Entry, 0, len(list))
	for i, item := range list {
		dict, _ := item.(map[string]any)
		length, ok := dict["length"].(int64)
		parts, _ := dict["path"].([]any)
		if !ok || length < 0 || len(parts) == 0 {
			return nil, invalid("file %d has no length or path", i)
		}
		path := []string{name}
		for _, part := range parts {
			s, _ := part.(string)
			if !safeName(s) {
				return nil, invalid("file %d has unsafe path component %q", i, s)
			}
			path = append(path, s)
		}
		files = append(files, Entry{Path: path, Length: length})
	}
	return files, nil
}

func safeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && filepath.IsLocal(s)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func splitHashes(pieces string) [][hashLen]byte {
	hashes := make([][hashLen]byte, len(pieces)/hashLen)
	for i := range hashes {
		copy(hashes[i][:], pieces[i*hashLen:])
	}
	return hashes
}

func (f File) PieceSize(index int) int64 {
	start := int64(index) * f.PieceLength
	return min(f.PieceLength, f.Length-start)
}
