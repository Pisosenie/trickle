package secret

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"trickle/internal/fsutil"
)

const size = 16

func New() (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func LoadOrCreate(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read token: %w", err)
	}
	token, err := New()
	if err != nil {
		return "", err
	}
	if err := Save(path, token); err != nil {
		return "", err
	}
	return token, nil
}

func Save(path, token string) error {
	if err := fsutil.WriteFileAtomic(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	return nil
}

func Equal(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func FromBearer(header string) string {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return ""
	}
	return token
}
