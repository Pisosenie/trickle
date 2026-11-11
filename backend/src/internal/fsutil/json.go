package fsutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func ReadJSON(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	return true, nil
}

func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, data, 0o600)
}
