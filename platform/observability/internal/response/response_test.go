package response

import (
	"net/http"
	"testing"
)

type recordingWriter struct {
	statuses []int
	body     []byte
	flushes  int
}

func (w *recordingWriter) Header() http.Header { return make(http.Header) }

func (w *recordingWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
}

func (w *recordingWriter) Write(body []byte) (int, error) {
	w.body = append(w.body, body...)
	return len(body), nil
}

func (w *recordingWriter) Flush() { w.flushes++ }

func TestWriterIgnoresInformationalStatuses(t *testing.T) {
	underlying := &recordingWriter{}
	writer := &Writer{ResponseWriter: underlying}

	writer.WriteHeader(http.StatusEarlyHints)
	if writer.Status != 0 || writer.StatusCode() != http.StatusOK {
		t.Fatalf("informational status was recorded as final: %d", writer.Status)
	}

	writer.WriteHeader(http.StatusCreated)
	_, _ = writer.Write([]byte("ok"))
	if writer.Status != http.StatusCreated || writer.Bytes != 2 {
		t.Fatalf("final status/bytes = %d/%d", writer.Status, writer.Bytes)
	}
	if len(underlying.statuses) != 2 || underlying.statuses[0] != http.StatusEarlyHints || underlying.statuses[1] != http.StatusCreated {
		t.Fatalf("forwarded statuses = %v", underlying.statuses)
	}
}

func TestWriterWriteAndFlushImplyOK(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		writer := &Writer{ResponseWriter: &recordingWriter{}}
		_, _ = writer.Write([]byte("ok"))
		if writer.Status != http.StatusOK {
			t.Fatalf("status = %d", writer.Status)
		}
	})

	t.Run("flush", func(t *testing.T) {
		underlying := &recordingWriter{}
		writer := &Writer{ResponseWriter: underlying}
		writer.Flush()
		if writer.Status != http.StatusOK || underlying.flushes != 1 {
			t.Fatalf("status/flushes = %d/%d", writer.Status, underlying.flushes)
		}
	})

	t.Run("unsupported flush", func(t *testing.T) {
		writer := &Writer{ResponseWriter: &plainWriter{}}
		writer.Flush()
		if writer.Status != 0 {
			t.Fatalf("unsupported flush recorded status %d", writer.Status)
		}
	})
}

func TestWriterKeepsSwitchingProtocols(t *testing.T) {
	writer := &Writer{ResponseWriter: &recordingWriter{}}
	writer.WriteHeader(http.StatusSwitchingProtocols)
	if writer.Status != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", writer.Status)
	}
}

type plainWriter struct{}

func (*plainWriter) Header() http.Header            { return make(http.Header) }
func (*plainWriter) Write(body []byte) (int, error) { return len(body), nil }
func (*plainWriter) WriteHeader(int)                {}
