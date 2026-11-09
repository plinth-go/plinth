package webui

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"reflect"
	"strings"
)

//go:embed assets/*
var embedded embed.FS

// Assets returns Plinth's embedded capability-page filesystem. Applications
// normally pass their own go:embed filesystem to Handler instead.
func Assets() fs.FS {
	files, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return files
}

// FS returns an on-disk directory when override is set, otherwise fallback.
// This keeps frontend deployment replaceable without rebuilding the binary.
func FS(fallback fs.FS, override string) (fs.FS, error) {
	if override == "" {
		if isNilFS(fallback) {
			return nil, fmt.Errorf("static filesystem is nil")
		}
		return fallback, nil
	}
	info, err := os.Stat(override)
	if err != nil {
		return nil, fmt.Errorf("open static directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("static path %q is not a directory", override)
	}
	return dirFS{dir: override}, nil
}

func isNilFS(files fs.FS) bool {
	if files == nil {
		return true
	}
	value := reflect.ValueOf(files)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type dirFS struct {
	dir string
}

func (d dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &rootedFile{File: file, root: root}, nil
}

func (d dirFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(name)
}

type rootedFile struct {
	*os.File
	root *os.Root
}

func (f *rootedFile) Close() error {
	err := f.File.Close()
	if rootErr := f.root.Close(); err == nil {
		err = rootErr
	}
	return err
}

func Handler(fallback fs.FS, override string) (http.Handler, error) {
	files, err := FS(fallback, override)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &staticResponseWriter{ResponseWriter: w}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		info, err := fs.Stat(files, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			index := path.Join(name, "index.html")
			indexInfo, err := fs.Stat(files, index)
			if err != nil || !indexInfo.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
			if !strings.HasSuffix(r.URL.Path, "/") {
				staticRedirect(w, r, path.Base(r.URL.Path)+"/")
				return
			}
			name = index
		} else if !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		} else if strings.HasSuffix(r.URL.Path, "/") {
			base := path.Base(r.URL.Path)
			if base == "/" || base == "." {
				http.Error(w, "static path does not resolve to a file", http.StatusInternalServerError)
				return
			}
			staticRedirect(w, r, "../"+base)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/index.html") {
			staticRedirect(w, r, "./")
			return
		}

		file, err := files.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		openedInfo, err := file.Stat()
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !openedInfo.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		content, ok := file.(io.ReadSeeker)
		if !ok {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, path.Base(name), openedInfo.ModTime(), content)
	}), nil
}

func staticRedirect(w http.ResponseWriter, r *http.Request, target string) {
	if escaped := r.URL.EscapedPath(); strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%2F") {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusMovedPermanently)
}

type staticResponseWriter struct {
	http.ResponseWriter
}

func (w *staticResponseWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *staticResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *staticResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{w}, src)
}
