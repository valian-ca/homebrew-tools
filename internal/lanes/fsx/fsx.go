package fsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/valian-ca/homebrew-tools/internal/lanes/paths"
)

func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteIfChanged keeps the mtime of a file whose bytes already match.
func WriteIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	return true, WriteAtomic(path, data, mode)
}

func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(data, '\n'), paths.FileMode)
}

func ReadJSON(path string, v any) (found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func WithLock(path string, fn func() error) error {
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	l := flock.New(path)
	if err := l.Lock(); err != nil {
		return fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = l.Unlock() }()
	return fn()
}
