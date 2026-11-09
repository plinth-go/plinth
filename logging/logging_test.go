package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestNewWritesToAllOutputs(t *testing.T) {
	var first bytes.Buffer
	var second bytes.Buffer
	logger, err := New(Config{
		Format:  FormatText,
		Console: ConsoleNone,
		Writers: []io.Writer{&first, &second},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Info("hello", "answer", 42)
	if !strings.Contains(first.String(), "msg=hello") || !strings.Contains(first.String(), "answer=42") {
		t.Fatalf("first output = %q", first.String())
	}
	if first.String() != second.String() {
		t.Fatalf("outputs differ:\n%q\n%q", first.String(), second.String())
	}
}

func TestNewJSONIsDefault(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(Config{Console: ConsoleNone, Writers: []io.Writer{&output}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Warn("careful", "code", "E_TEST")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode JSON log %q: %v", output.String(), err)
	}
	if record["msg"] != "careful" || record["code"] != "E_TEST" || record["level"] != "WARN" {
		t.Fatalf("record = %#v", record)
	}
}

func TestNewFiltersBelowLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(Config{Level: "warn", Console: ConsoleNone, Writers: []io.Writer{&output}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Info("hidden")
	logger.Warn("visible")
	if strings.Contains(output.String(), "hidden") {
		t.Fatalf("info record not filtered: %q", output.String())
	}
	if !strings.Contains(output.String(), "visible") {
		t.Fatalf("warn record missing: %q", output.String())
	}
}

func TestNewFileOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	logger, err := New(Config{Format: FormatText, Console: ConsoleNone, File: path})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("file record", "value", "ok")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "msg=\"file record\"") {
		t.Fatalf("file contents = %q", data)
	}
	if goruntime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o", info.Mode().Perm())
		}
	}
}

func TestNewFileAndWriterReceiveTheSameRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	var output bytes.Buffer
	logger, err := New(Config{Format: FormatText, Console: ConsoleNone, File: path, Writers: []io.Writer{&output}})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("both")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != output.String() {
		t.Fatalf("file = %q, writer = %q", data, output.String())
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	var typedNil *bytes.Buffer
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "level", config: Config{Level: "verbose", Console: ConsoleNone}, want: "log level"},
		{name: "format", config: Config{Format: "xml", Console: ConsoleNone}, want: "log format"},
		{name: "format and handler factory", config: Config{Format: FormatJSON, Console: ConsoleNone, NewHandler: func(io.Writer, *slog.HandlerOptions) slog.Handler { return slog.NewTextHandler(io.Discard, nil) }}, want: "mutually exclusive"},
		{name: "console", config: Config{Console: Console(99)}, want: "console"},
		{name: "nil writer", config: Config{Console: ConsoleNone, Writers: []io.Writer{nil}}, want: "writer 0 is nil"},
		{name: "typed nil writer", config: Config{Console: ConsoleNone, Writers: []io.Writer{typedNil}}, want: "writer 0 is nil"},
		{name: "missing parent", config: Config{Console: ConsoleNone, File: filepath.Join(t.TempDir(), "missing", "app.log")}, want: "open log file"},
		{name: "directory", config: Config{Console: ConsoleNone, File: t.TempDir()}, want: "open log file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger, err := New(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New error = %v", err)
			}
			if logger != nil {
				t.Fatal("New returned a logger on error")
			}
		})
	}
}

func TestConfigureOptionsCanCustomizeAndOverride(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(Config{
		Level:   "info",
		Console: ConsoleNone,
		Writers: []io.Writer{&output},
		ConfigureOptions: func(options *slog.HandlerOptions) {
			options.Level = slog.LevelDebug
			options.ReplaceAttr = func(_ []string, attr slog.Attr) slog.Attr {
				if attr.Key == slog.TimeKey {
					return slog.String("ts", "fixed")
				}
				return attr
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Debug("visible")
	if !strings.Contains(output.String(), `"msg":"visible"`) {
		t.Fatalf("debug record filtered: %q", output.String())
	}
	if !strings.Contains(output.String(), `"ts":"fixed"`) || strings.Contains(output.String(), `"time"`) {
		t.Fatalf("ReplaceAttr was not applied: %q", output.String())
	}
}

func TestNewHandlerFactoryUsesCombinedWriter(t *testing.T) {
	var output bytes.Buffer
	called := false
	logger, err := New(Config{
		Level:   "warn",
		Console: ConsoleNone,
		Writers: []io.Writer{&output},
		NewHandler: func(writer io.Writer, options *slog.HandlerOptions) slog.Handler {
			called = true
			if options == nil || options.Level != slog.LevelWarn {
				t.Fatalf("handler options = %#v", options)
			}
			return slog.NewTextHandler(writer, options)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Warn("custom", "value", 1)
	if !called {
		t.Fatal("handler factory was not called")
	}
	if !strings.Contains(output.String(), "msg=custom") || !strings.Contains(output.String(), "value=1") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestNewHandlerFactoryRejectsNil(t *testing.T) {
	logger, err := New(Config{
		Console:    ConsoleNone,
		NewHandler: func(io.Writer, *slog.HandlerOptions) slog.Handler { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("error = %v", err)
	}
	if logger != nil {
		t.Fatal("New returned a logger for a nil handler")
	}
}

func TestCloseIsIdempotentAndKeepsCallerWritersOpen(t *testing.T) {
	writer := &trackingWriter{}
	logger, err := New(Config{Console: ConsoleNone, Writers: []io.Writer{writer}})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if writer.closed {
		t.Fatal("logger closed a caller-owned writer")
	}
}

func TestWriteErrorIsObservable(t *testing.T) {
	writeErr := errors.New("disk full")
	logger, err := New(Config{Console: ConsoleNone, Writers: []io.Writer{failingWriter{err: writeErr}}})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("cannot write")
	if !errors.Is(logger.Err(), writeErr) {
		t.Fatalf("Err = %v", logger.Err())
	}
	if !errors.Is(logger.Close(), writeErr) {
		t.Fatalf("Close = %v", logger.Close())
	}
}

func TestWriteAfterCloseIsReported(t *testing.T) {
	logger, err := New(Config{Console: ConsoleNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logger.Info("after close")
	if !errors.Is(logger.Err(), ErrClosed) {
		t.Fatal("write after close was not recorded")
	}
	if err := logger.Close(); !errors.Is(err, ErrClosed) {
		t.Fatal("repeated Close hid the recorded error")
	}
}

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		" debug ": slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for input, want := range tests {
		got, err := ParseLevel(input)
		if err != nil {
			t.Fatalf("ParseLevel(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %s, want %s", input, got, want)
		}
	}
	if _, err := ParseLevel("trace"); err == nil {
		t.Fatal("ParseLevel accepted trace")
	}
}

type trackingWriter struct {
	closed bool
}

func (w *trackingWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *trackingWriter) Close() error {
	w.closed = true
	return nil
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
