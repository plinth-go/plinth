package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestErrExistsMatchesStandardSentinel(t *testing.T) {
	if !errors.Is(ErrExists, fs.ErrExist) {
		t.Fatalf("ErrExists does not wrap fs.ErrExist: %v", ErrExists)
	}
}

func TestCreateWritesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	if err := Create(path, []byte("complete\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete\n" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	assertOnlyFile(t, dir, "output.txt")
}

func TestCreateIsExclusiveUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	const callers = 8

	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- Create(path, []byte("complete\n"), 0o644)
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes, exists := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrExists):
			exists++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || exists != callers-1 {
		t.Fatalf("successes = %d, ErrExists = %d", successes, exists)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete\n" {
		t.Fatalf("content = %q", data)
	}
	assertOnlyFile(t, dir, "output.txt")
}

func TestCreateRejectsDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	target := filepath.Join(dir, "missing.txt")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Create(path, []byte("data"), 0o644); !errors.Is(err, ErrExists) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

func TestReplaceIsAtomicAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("file was rewritten in place instead of atomically replaced")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", after.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("content = %q", data)
	}
	assertOnlyFile(t, dir, "output.txt")
}

func TestReplaceDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "output.txt")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil {
		t.Fatal(err)
	} else if string(data) != "keep\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("output is still a symlink: %s", info.Mode())
	}
}

func TestReplaceCommitFailureLeavesDestinationAndNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(path, []byte("new\n"), 0o644); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("destination mode = %s, want directory", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "output" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestStageAppliesFinalModeBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	var stagedMode fs.FileMode
	err := stage(path, []byte("complete\n"), 0o640, func(tempPath string) error {
		info, err := os.Stat(tempPath)
		if err != nil {
			return err
		}
		stagedMode = info.Mode().Perm()
		return os.Rename(tempPath, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stagedMode != 0o640 {
		t.Fatalf("staged mode = %o, want 640", stagedMode)
	}
	assertOnlyFile(t, dir, "output.txt")
}

func TestStageRejectsEmptyPathAndNilCommit(t *testing.T) {
	if err := stage("", []byte("data"), 0o644, func(string) error { return nil }); err == nil || err.Error() != "path is empty" {
		t.Fatalf("empty path error = %v", err)
	}
	dir := t.TempDir()
	if err := stage(filepath.Join(dir, "output.txt"), []byte("data"), 0o644, nil); err == nil || err.Error() != "commit function is nil" {
		t.Fatalf("nil commit error = %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestWriteAllRejectsShortWrite(t *testing.T) {
	if err := writeAll(shortWriter{}, []byte("data")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}

func assertOnlyFile(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
