package localfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/plinth-go/plinth/platform/objectstore"
)

func TestStoreRoundTripListAndDelete(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := []byte("hello")
	stored, err := store.Put(ctx, "docs/readme.txt", bytes.NewReader(body), int64(len(body)), objectstore.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Key != "docs/readme.txt" || stored.Size != 5 || stored.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("stored = %#v", stored)
	}
	reader, opened, err := store.Open(ctx, "docs/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, body) || opened.Size != 5 {
		t.Fatalf("read = %q, %#v, %v", got, opened, err)
	}
	if _, err := store.Stat(ctx, "docs/readme.txt"); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, "docs/", 10)
	if err != nil || len(list) != 1 || list[0].Key != "docs/readme.txt" {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if err := store.Delete(ctx, "docs/readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "docs/readme.txt"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("stat after delete = %v", err)
	}
}

func TestStoreRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, key := range []string{"../escape", "/absolute", "a//b", "a/../b", `a\\b`} {
		if _, err := store.Put(ctx, key, bytes.NewReader(nil), 0, objectstore.PutOptions{}); err == nil {
			t.Fatalf("unsafe key %q was accepted", key)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create directory symlink: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "link/file.txt", bytes.NewReader([]byte("x")), 1, objectstore.PutOptions{}); err == nil {
		t.Fatal("symlink path was accepted")
	}
}

func TestPutRejectsSizeMismatch(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "a.txt", bytes.NewReader([]byte("toolong")), 3, objectstore.PutOptions{}); err == nil {
		t.Fatal("oversized body was accepted")
	}
	if _, err := store.Put(context.Background(), "a.txt", bytes.NewReader([]byte("x")), 2, objectstore.PutOptions{}); err == nil {
		t.Fatal("short body was accepted")
	}
}

func TestListValidation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), "", 0); err == nil {
		t.Fatal("zero limit was accepted")
	}
	if _, err := store.List(context.Background(), "../", 1); err == nil {
		t.Fatal("unsafe prefix was accepted")
	}
}
