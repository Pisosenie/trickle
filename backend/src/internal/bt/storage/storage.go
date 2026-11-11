package storage

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"trickle/internal/bt/torrentfile"
)

type span struct {
	start  int64
	length int64
}

type Storage struct {
	spans  []span
	files  []*os.File
	length int64
}

func Open(dir string, tf torrentfile.File) (*Storage, error) {
	s := &Storage{length: tf.Length}
	var offset int64
	for _, entry := range tf.Files {
		f, err := openEntry(dir, entry)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.spans = append(s.spans, span{start: offset, length: entry.Length})
		s.files = append(s.files, f)
		offset += entry.Length
	}
	return s, nil
}

func openEntry(dir string, entry torrentfile.Entry) (*os.File, error) {
	rel := filepath.Join(entry.Path...)
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("unsafe path %q", rel)
	}
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(entry.Length); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Storage) WriteAt(p []byte, off int64) error {
	return s.each(p, off, func(f *os.File, b []byte, at int64) error {
		_, err := f.WriteAt(b, at)
		return err
	})
}

func (s *Storage) ReadAt(p []byte, off int64) error {
	return s.each(p, off, func(f *os.File, b []byte, at int64) error {
		_, err := f.ReadAt(b, at)
		return err
	})
}

func (s *Storage) each(p []byte, off int64, fn func(*os.File, []byte, int64) error) error {
	if off < 0 || off+int64(len(p)) > s.length {
		return fmt.Errorf("range %d+%d is outside the torrent", off, len(p))
	}
	for i, sp := range s.spans {
		end := sp.start + sp.length
		if len(p) == 0 {
			break
		}
		if off >= end {
			continue
		}
		n := min(int64(len(p)), end-off)
		if err := fn(s.files[i], p[:n], off-sp.start); err != nil {
			return err
		}
		p = p[n:]
		off += n
	}
	return nil
}

func (s *Storage) Verify(tf torrentfile.File) ([]bool, error) {
	have := make([]bool, len(tf.Pieces))
	buf := make([]byte, tf.PieceLength)
	for i, hash := range tf.Pieces {
		chunk := buf[:tf.PieceSize(i)]
		if err := s.ReadAt(chunk, int64(i)*tf.PieceLength); err != nil {
			return nil, err
		}
		have[i] = sha1.Sum(chunk) == hash
	}
	return have, nil
}

func (s *Storage) Sync() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, f.Sync())
	}
	return errors.Join(errs...)
}

func (s *Storage) Close() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, f.Close())
	}
	return errors.Join(errs...)
}
