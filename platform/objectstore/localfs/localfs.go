// Package localfs implements objectstore.Store on a private local directory.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/plinth-go/plinth/platform/objectstore"
	"github.com/plinth-go/plinth/utils"
)

// Store owns one directory tree.
type Store struct {
	root string
}

// New creates or opens root, resolves symlinks in the root itself, and rejects
// a non-directory target.
func New(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("local object store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local object store root is not a directory: %s", resolved)
	}
	return &Store{root: resolved}, nil
}

func (s *Store) Put(ctx context.Context, key string, body io.Reader, size int64, opts objectstore.PutOptions) (objectstore.Object, error) {
	key, err := objectstore.NormalizeKey(key)
	if err != nil {
		return objectstore.Object{}, err
	}
	if body == nil {
		return objectstore.Object{}, errors.New("object body is nil")
	}
	if size < 0 {
		return objectstore.Object{}, errors.New("object size must not be negative")
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	path, err := s.safePath(key, true)
	if err != nil {
		return objectstore.Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return objectstore.Object{}, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".upload-")
	if err != nil {
		return objectstore.Object{}, err
	}
	tempPath := temp.Name()
	remove := true
	defer func() {
		_ = temp.Close()
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o640); err != nil {
		return objectstore.Object{}, err
	}
	written, err := copyContext(ctx, temp, body, size)
	if err != nil {
		return objectstore.Object{}, err
	}
	if written != size {
		return objectstore.Object{}, fmt.Errorf("object size mismatch: expected %d bytes, wrote %d", size, written)
	}
	if err := temp.Sync(); err != nil {
		return objectstore.Object{}, err
	}
	if err := temp.Close(); err != nil {
		return objectstore.Object{}, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return objectstore.Object{}, err
	}
	remove = false
	info, err := os.Stat(path)
	if err != nil {
		return objectstore.Object{}, err
	}
	return object(key, info, opts.ContentType), nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error) {
	key, err := objectstore.NormalizeKey(key)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, objectstore.Object{}, err
	}
	path, err := s.safePath(key, false)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, objectstore.Object{}, objectstore.ErrNotFound
	}
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, objectstore.Object{}, objectstore.ErrNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	return file, object(key, info, ""), nil
}

func (s *Store) Stat(ctx context.Context, key string) (objectstore.Object, error) {
	key, err := objectstore.NormalizeKey(key)
	if err != nil {
		return objectstore.Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	path, err := s.safePath(key, false)
	if err != nil {
		return objectstore.Object{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	if err != nil {
		return objectstore.Object{}, err
	}
	if !info.Mode().IsRegular() {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	return object(key, info, ""), nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	key, err := objectstore.NormalizeKey(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.safePath(key, false)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return objectstore.ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix string, limit int) ([]objectstore.Object, error) {
	prefix, err := objectstore.NormalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("object list limit must be between 1 and 1000")
	}
	objects := make([]objectstore.Object, 0, limit)
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == s.root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			if entry.IsDir() && !strings.HasPrefix(prefix, key+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		objects = append(objects, object(key, info, ""))
		if len(objects) >= limit {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return objects, err
}

var errStop = errors.New("object list complete")

func (s *Store) safePath(key string, create bool) (string, error) {
	parts := strings.Split(key, "/")
	current := s.root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if create || index < len(parts)-1 {
				continue
			}
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("object path contains a symlink: %s", key)
		}
	}
	return current, nil
}

func object(key string, info os.FileInfo, contentType string) objectstore.Object {
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(key))
	}
	contentType = utils.IfEmptyElse(contentType, "application/octet-stream")
	return objectstore.Object{
		Key: key, Size: info.Size(), ContentType: contentType,
		ModifiedAt: info.ModTime().UTC().Truncate(time.Second),
	}
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	buffer := make([]byte, 32<<10)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if written+int64(n) > size {
				return written, errors.New("object body exceeds declared size")
			}
			count, writeErr := dst.Write(buffer[:n])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
