//go:build unix

package kernel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWriteFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("create FIFO: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WriteFile(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/pipe", nil), path, "")
	}()
	select {
	case err := <-done:
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteFile blocked while opening a FIFO")
	}
}
