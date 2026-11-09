// Package atomicfile writes files atomically or with exclusive creation.
package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrExists reports that an exclusive create found an existing destination,
// including a dangling symbolic link.
var ErrExists error = existsError{}

type existsError struct{}

func (existsError) Error() string { return "file already exists" }
func (existsError) Unwrap() error { return fs.ErrExist }

// Create writes a complete file only when path does not already exist. The
// completed temporary file is hard-linked into place, so concurrent callers
// cannot both succeed and readers never observe a partially written file.
func Create(path string, data []byte, mode fs.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return stage(path, data, mode, func(tempPath string) error {
		if err := os.Link(tempPath, path); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return ErrExists
			}
			return err
		}
		return nil
	})
}

// Replace writes a complete file and atomically renames it over path. An
// existing regular file keeps its permission bits. A symbolic link is replaced
// rather than followed, so the link target is never modified.
func Replace(path string, data []byte, mode fs.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return stage(path, data, mode, func(tempPath string) error {
		return os.Rename(tempPath, path)
	})
}

func stage(path string, data []byte, mode fs.FileMode, commit func(string) error) error {
	if path == "" {
		return errors.New("path is empty")
	}
	if commit == nil {
		return errors.New("commit function is nil")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".plinth-atomic-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()

	if err := writeAll(temp, data); err != nil {
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return commit(tempPath)
}

func writeAll(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
