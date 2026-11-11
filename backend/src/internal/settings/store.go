package settings

import (
	"fmt"
	"sync"

	"trickle/internal/fsutil"
)

type Store struct {
	path string

	mu      sync.RWMutex
	current Settings
}

func Open(path string, defaults Settings) (*Store, error) {
	loaded := defaults
	if _, err := fsutil.ReadJSON(path, &loaded); err != nil {
		return nil, err
	}
	if err := loaded.Validate(); err != nil {
		return nil, fmt.Errorf("invalid settings in %s: %w", path, err)
	}
	return &Store{path: path, current: loaded}, nil
}

func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

func (s *Store) Replace(next Settings) error {
	if err := next.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fsutil.WriteJSON(s.path, next); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	s.current = next
	return nil
}
