// Package logging builds an application slog logger from an explicit,
// deployment-friendly configuration. It deliberately does not add log
// rotation, sampling, or global state; those belong to the application or an
// external process.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
)

const (
	// FormatJSON writes one JSON object per record.
	FormatJSON = "json"
	// FormatText writes slog's human-readable key=value format.
	FormatText = "text"
)

// ErrClosed is returned when a record is written after Logger.Close.
var ErrClosed = errors.New("logging output is closed")

// Console selects the process stream used for terminal logging. The zero value
// is ConsoleStdout, so a zero Config writes to stdout.
type Console int

const (
	// ConsoleStdout writes terminal records to os.Stdout.
	ConsoleStdout Console = iota
	// ConsoleNone disables terminal logging.
	ConsoleNone
	// ConsoleStderr writes terminal records to os.Stderr.
	ConsoleStderr
)

// Config describes one application logger. Empty Level and Format use "info"
// and "json". Console defaults to stdout; set ConsoleNone for file-only or
// writer-only logging. File is optional. Writers are additional destinations
// owned by the caller and are never closed by Logger. ConfigureOptions may
// customize the standard slog.HandlerOptions after Level/AddSource are applied.
// NewHandler may replace the JSON/Text handler with a custom handler over the
// combined output writer; it is mutually exclusive with Format.
type Config struct {
	Level     string
	Format    string
	Console   Console
	File      string
	Writers   []io.Writer
	AddSource bool
	// ConfigureOptions runs after Level and AddSource are applied. It may set
	// ReplaceAttr, override Level, or adjust any other standard handler option.
	ConfigureOptions func(*slog.HandlerOptions)
	// NewHandler optionally builds a custom handler over the combined output
	// writer. It is mutually exclusive with a non-empty Format.
	NewHandler func(io.Writer, *slog.HandlerOptions) slog.Handler
}

// Logger owns the configured output sink. Its embedded slog.Logger is safe for
// concurrent use. Call Close during application shutdown to close the log file.
type Logger struct {
	*slog.Logger
	sink *sink
}

// Slog returns the embedded standard logger for APIs that accept *slog.Logger.
func (l *Logger) Slog() *slog.Logger {
	if l == nil || l.Logger == nil {
		return slog.Default()
	}
	return l.Logger
}

// New validates config, opens File when configured, and returns a logger whose
// records are written once to every configured destination. New fails before
// returning if a destination cannot be opened.
func New(config Config) (*Logger, error) {
	level, err := ParseLevel(config.Level)
	if err != nil {
		return nil, err
	}
	format, err := parseFormat(config.Format)
	if err != nil {
		return nil, err
	}
	if config.NewHandler != nil && strings.TrimSpace(config.Format) != "" {
		return nil, errors.New("logging Format and NewHandler are mutually exclusive")
	}
	if config.Console < ConsoleStdout || config.Console > ConsoleStderr {
		return nil, fmt.Errorf("logging console %d is invalid", config.Console)
	}
	writers, err := validateWriters(config.Writers)
	if err != nil {
		return nil, err
	}

	outputs := make([]io.Writer, 0, 2+len(writers))
	switch config.Console {
	case ConsoleStdout:
		outputs = append(outputs, os.Stdout)
	case ConsoleStderr:
		outputs = append(outputs, os.Stderr)
	}

	var file *os.File
	path := strings.TrimSpace(config.File)
	if path != "" {
		file, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open log file %q: %w", path, err)
		}
		outputs = append(outputs, file)
	}
	outputs = append(outputs, writers...)

	sink := &sink{outputs: outputs, file: file}
	options := &slog.HandlerOptions{Level: level, AddSource: config.AddSource}
	if config.ConfigureOptions != nil {
		config.ConfigureOptions(options)
	}
	var handler slog.Handler
	switch {
	case config.NewHandler != nil:
		handler = config.NewHandler(sink, options)
		if handler == nil {
			if file != nil {
				_ = file.Close()
			}
			return nil, errors.New("logging handler factory returned nil")
		}
	case format == FormatText:
		handler = slog.NewTextHandler(sink, options)
	default:
		handler = slog.NewJSONHandler(sink, options)
	}
	return &Logger{Logger: slog.New(handler), sink: sink}, nil
}

// ParseLevel normalizes a configured level. An empty value means info.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

func parseFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatText:
		return FormatText, nil
	default:
		return "", fmt.Errorf("unsupported log format %q", value)
	}
}

func validateWriters(writers []io.Writer) ([]io.Writer, error) {
	copied := make([]io.Writer, len(writers))
	for index, writer := range writers {
		if isNilWriter(writer) {
			return nil, fmt.Errorf("log writer %d is nil", index)
		}
		copied[index] = writer
	}
	return copied, nil
}

func isNilWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type sink struct {
	mu      sync.Mutex
	outputs []io.Writer
	file    *os.File
	closed  bool
	err     error
}

func (s *sink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if s.err == nil {
			s.err = ErrClosed
		}
		return 0, s.err
	}
	if len(s.outputs) == 0 {
		return len(data), nil
	}
	var writeErr error
	for index, output := range s.outputs {
		written, err := output.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil && writeErr == nil {
			writeErr = fmt.Errorf("write log output %d: %w", index, err)
			if s.err == nil {
				s.err = writeErr
			}
		}
	}
	if writeErr != nil {
		return len(data), writeErr
	}
	return len(data), nil
}

// Err returns the first output error observed by this logger. slog handlers do
// not surface writer errors, so applications can poll Err or rely on Close.
func (l *Logger) Err() error {
	if l == nil || l.sink == nil {
		return nil
	}
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	return l.sink.err
}

// Close closes the configured file and returns the first output or close error.
// Repeated calls are safe. Writers supplied through Config.Writers are not
// closed because the caller retains ownership.
func (l *Logger) Close() error {
	if l == nil || l.sink == nil {
		return nil
	}
	s := l.sink
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.file != nil {
		if err := s.file.Close(); err != nil && s.err == nil {
			s.err = fmt.Errorf("close log file: %w", err)
		}
	}
	return s.err
}
