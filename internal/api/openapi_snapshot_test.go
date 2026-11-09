package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAPISnapshot(t *testing.T) {
	router := testRouter(t)
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, filepath.Join("testdata", "openapi.json"), router.OpenAPIJSON())
}

func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	got = normalizeGolden(got)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI snapshot: %v\nrun UPDATE_GOLDEN=1 go test ./internal/api -run TestOpenAPISnapshot", err)
	}
	want = normalizeGolden(want)
	if bytes.Equal(want, got) {
		return
	}
	t.Fatalf("OpenAPI snapshot is stale\nrun UPDATE_GOLDEN=1 go test ./internal/api -run TestOpenAPISnapshot\n%s", firstDifference(want, got))
}

func normalizeGolden(data []byte) []byte {
	return append(append([]byte(nil), bytes.TrimSpace(data)...), '\n')
}

func firstDifference(want, got []byte) string {
	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(string(got), "\n")
	for i := 0; i < min(len(wantLines), len(gotLines)); i++ {
		if wantLines[i] != gotLines[i] {
			return fmt.Sprintf("first difference at line %d:\n- %s\n+ %s", i+1, wantLines[i], gotLines[i])
		}
	}
	return fmt.Sprintf("line count differs: snapshot has %d, generated output has %d", len(wantLines), len(gotLines))
}
