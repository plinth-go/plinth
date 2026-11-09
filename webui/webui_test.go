package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type typedNilFS struct{}

func (*typedNilFS) Open(string) (fs.File, error) { panic("typed nil filesystem was used") }

type modeOverrideFS struct {
	fs.FS
	modes map[string]fs.FileMode
}

func (f modeOverrideFS) Stat(name string) (fs.FileInfo, error) {
	info, err := fs.Stat(f.FS, name)
	if err != nil {
		return nil, err
	}
	mode, ok := f.modes[name]
	if !ok {
		return info, nil
	}
	return modeOverrideInfo{FileInfo: info, mode: mode}, nil
}

type modeOverrideInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i modeOverrideInfo) Mode() fs.FileMode { return i.mode }

type missingIndexOpenFS struct {
	fs.FS
}

func (f missingIndexOpenFS) Open(name string) (fs.File, error) {
	if name == "index.html" {
		return nil, fs.ErrNotExist
	}
	return f.FS.Open(name)
}

func (f missingIndexOpenFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(f.FS, name)
}

func TestEmbeddedHandler(t *testing.T) {
	handler, err := Handler(Assets(), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Plinth") {
		t.Fatal("embedded index was not served")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestFSRejectsTypedNilFallback(t *testing.T) {
	var files *typedNilFS
	if _, err := FS(files, ""); err == nil {
		t.Fatal("typed-nil fallback filesystem was accepted")
	}
	if _, err := Handler(files, ""); err == nil {
		t.Fatal("handler accepted typed-nil fallback filesystem")
	}
}

func TestDirectoryOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("override"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != "override" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestHandlerRejectsUnsupportedMethods(t *testing.T) {
	handler, err := Handler(Assets(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", method, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
}

func TestHandlerDoesNotListDirectoriesWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/private", "/private/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, body = %q", target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret.txt") {
			t.Fatalf("%s leaked directory listing: %q", target, rec.Body.String())
		}
	}
}

func TestHandlerDoesNotListRootWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "asset.txt") {
		t.Fatalf("root directory listing leaked: %q", rec.Body.String())
	}
}

func TestHandlerDoesNotFallBackToDirectoryListingWhenIndexDisappears(t *testing.T) {
	files := missingIndexOpenFS{FS: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("root")},
		"secret.txt": &fstest.MapFile{Data: []byte("secret")},
	}}
	handler, err := Handler(files, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret.txt") || strings.Contains(rec.Body.String(), "index.html") {
		t.Fatalf("directory listing leaked: %q", rec.Body.String())
	}
}

func TestHandlerPreservesCanonicalStaticRedirects(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "index.html"), []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		target   string
		location string
	}{
		{target: "/index.html", location: "./"},
		{target: "/private", location: "private/"},
		{target: "/asset.txt/", location: "../asset.txt"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("%s status = %d, body = %q", tc.target, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != tc.location {
			t.Fatalf("%s Location = %q", tc.target, got)
		}
	}
}

func TestHandlerRejectsNonRegularFiles(t *testing.T) {
	files := modeOverrideFS{
		FS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("root")},
			"pipe.txt":   &fstest.MapFile{Data: []byte("pipe")},
		},
		modes: map[string]fs.FileMode{"pipe.txt": fs.ModeNamedPipe},
	}
	handler, err := Handler(files, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipe.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pipe") {
		t.Fatalf("non-regular file body leaked: %q", rec.Body.String())
	}
}

func TestHandlerRejectsNonRegularDirectoryIndex(t *testing.T) {
	files := modeOverrideFS{
		FS:    fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("root")}},
		modes: map[string]fs.FileMode{"index.html": fs.ModeNamedPipe},
	}
	handler, err := Handler(files, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "root") {
		t.Fatalf("non-regular index body leaked: %q", rec.Body.String())
	}
}

func TestHandlerConfinesSymlinksToStaticRoot(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Skipf("create file symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "leak.txt")); err != nil {
		t.Skipf("create outside file symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "external")); err != nil {
		t.Skipf("create outside directory symlink: %v", err)
	}

	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/leak.txt", "/external/secret.txt"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, body = %q", target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s leaked outside content: %q", target, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/alias.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "inside" {
		t.Fatalf("inside symlink status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerPreservesUpstreamCacheControl(t *testing.T) {
	handler, err := Handler(Assets(), "")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		handler.ServeHTTP(w, r)
	})
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerDelegatesRangeHeadAndConditionalRequests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(dir, "asset.txt")
	if err := os.WriteFile(assetPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(assetPath, modified, modified); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	ranged := httptest.NewRecorder()
	rangeRequest := httptest.NewRequest(http.MethodGet, "/asset.txt", nil)
	rangeRequest.Header.Set("Range", "bytes=2-5")
	handler.ServeHTTP(ranged, rangeRequest)
	if ranged.Code != http.StatusPartialContent || ranged.Body.String() != "2345" {
		t.Fatalf("range status = %d, body = %q", ranged.Code, ranged.Body.String())
	}
	if got := ranged.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", got)
	}

	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/asset.txt", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d, body = %q", head.Code, head.Body.String())
	}
	if got := head.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("HEAD Content-Length = %q", got)
	}

	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, "/asset.txt", nil))
	lastModified := initial.Header().Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("Last-Modified is missing")
	}

	conditional := httptest.NewRecorder()
	conditionalRequest := httptest.NewRequest(http.MethodGet, "/asset.txt", nil)
	conditionalRequest.Header.Set("If-Modified-Since", lastModified)
	handler.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional status = %d, body = %q", conditional.Code, conditional.Body.String())
	}
	if got := conditional.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("conditional X-Content-Type-Options = %q", got)
	}
}

func TestHandlerServesRangeHeadWithContentType(t *testing.T) {
	dir := t.TempDir()
	assetPath := filepath.Join(dir, "asset.txt")
	if err := os.WriteFile(assetPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	head := httptest.NewRecorder()
	headRequest := httptest.NewRequest(http.MethodHead, "/asset.txt", nil)
	headRequest.Header.Set("Range", "bytes=2-5")
	handler.ServeHTTP(head, headRequest)
	if head.Code != http.StatusPartialContent || head.Body.Len() != 0 {
		t.Fatalf("HEAD range status = %d, body = %q", head.Code, head.Body.String())
	}
	if got := head.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("HEAD Content-Range = %q", got)
	}
	if got := head.Header().Get("Content-Length"); got != "4" {
		t.Fatalf("HEAD Content-Length = %q", got)
	}
	if got := head.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("HEAD Content-Type = %q", got)
	}
}

func TestHandlerMakesErrorResponsesNonCacheable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(nil, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
		rangeH string
		status int
	}{
		{name: "missing", method: http.MethodGet, target: "/missing.txt", status: http.StatusNotFound},
		{name: "method", method: http.MethodPost, target: "/asset.txt", status: http.StatusMethodNotAllowed},
		{name: "range", method: http.MethodGet, target: "/asset.txt", rangeH: "bytes=100-200", status: http.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, nil)
			if tc.rangeH != "" {
				req.Header.Set("Range", tc.rangeH)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
		})
	}
}
