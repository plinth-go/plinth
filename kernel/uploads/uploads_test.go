package uploads

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/plinth-go/plinth/kernel"
)

func TestParseStreamsFilesAndValues(t *testing.T) {
	tempRoot := t.TempDir()
	parser := mustParser(t, Config{TempDir: tempRoot, AllowedContentTypes: []string{"text/*"}})
	req := multipartRequest(t, []part{
		{name: "title", value: "hello"},
		{name: "document", filename: "notes.txt", contentType: "text/plain", value: "file body"},
	})
	form, err := parser.Parse(req)
	if err != nil {
		t.Fatal(err)
	}
	defer form.Close()
	if form.Value("title") != "hello" || len(form.Files) != 1 {
		t.Fatalf("form = %#v", form)
	}
	file := form.Files[0]
	if file.Field != "document" || file.Filename != "notes.txt" || file.ContentType != "text/plain" || file.Size != int64(len("file body")) {
		t.Fatalf("file = %#v", file)
	}
	opened, err := file.Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(opened)
	_ = opened.Close()
	if err != nil || string(body) != "file body" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
	dir := form.dir
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("upload directory still exists: %v", err)
	}
	if _, err := file.Open(); err == nil {
		t.Fatal("closed form still opened a file")
	}
}

func TestParseRejectsLimitsAndTypes(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		parts  []part
		status int
	}{
		{
			name:   "file too large",
			config: Config{MaxFileBytes: 4, MaxTotalBytes: 10},
			parts:  []part{{name: "file", filename: "a.txt", value: "12345"}},
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name:   "too many files",
			config: Config{MaxFiles: 1},
			parts: []part{
				{name: "a", filename: "a.txt", value: "a"},
				{name: "b", filename: "b.txt", value: "b"},
			},
			status: http.StatusBadRequest,
		},
		{
			name:   "too many fields",
			config: Config{MaxFields: 1},
			parts: []part{
				{name: "a", value: "a"},
				{name: "b", value: "b"},
			},
			status: http.StatusBadRequest,
		},
		{
			name:   "disallowed type",
			config: Config{AllowedContentTypes: []string{"image/*"}},
			parts:  []part{{name: "file", filename: "a.txt", contentType: "text/plain", value: "text"}},
			status: http.StatusUnsupportedMediaType,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.TempDir = t.TempDir()
			parser := mustParser(t, test.config)
			_, err := parser.Parse(multipartRequest(t, test.parts))
			status, ok := errorStatus(err)
			if !ok || status != test.status {
				t.Fatalf("error = %v, status = %d", err, status)
			}
		})
	}
}

func TestSafeFilename(t *testing.T) {
	tests := []struct {
		value string
		want  string
		ok    bool
	}{
		{value: "notes.txt", want: "notes.txt", ok: true},
		{value: "../secret.txt", want: "secret.txt", ok: true},
		{value: `..\\secret.txt`, want: "secret.txt", ok: true},
		{value: ".", ok: false},
		{value: "..", ok: false},
		{value: "bad\x00name", ok: false},
		{value: "bad\nname", ok: false},
	}
	for _, test := range tests {
		got, err := safeFilename(test.value)
		if test.ok && (err != nil || got != test.want) {
			t.Fatalf("safeFilename(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
		if !test.ok && err == nil {
			t.Fatalf("safeFilename(%q) accepted unsafe value as %q", test.value, got)
		}
	}
}

func TestParseRequiresMultipart(t *testing.T) {
	parser := mustParser(t, Config{TempDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	_, err := parser.Parse(req)
	if status, _ := errorStatus(err); status != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{
		{MaxFiles: -1},
		{MaxFields: -1},
		{MaxFileBytes: -1},
		{MaxTotalBytes: -1},
		{MaxFieldBytes: -1},
		{MaxFileBytes: 10, MaxTotalBytes: 5},
		{AllowedContentTypes: []string{"text"}},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("config was accepted: %#v", cfg)
		}
	}
}

type part struct {
	name        string
	value       string
	filename    string
	contentType string
}

func multipartRequest(t *testing.T, parts []part) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, item := range parts {
		if item.filename == "" {
			if err := writer.WriteField(item.name, item.value); err != nil {
				t.Fatal(err)
			}
			continue
		}
		header := make(map[string][]string)
		if item.contentType != "" {
			header["Content-Disposition"] = []string{`form-data; name="` + item.name + `"; filename="` + item.filename + `"`}
			header["Content-Type"] = []string{item.contentType}
			part, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write([]byte(item.value))
			continue
		}
		part, err := writer.CreateFormFile(item.name, item.filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(item.value))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func mustParser(t *testing.T, cfg Config) *Parser {
	t.Helper()
	parser, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return parser
}

func errorStatus(err error) (int, bool) {
	var httpErr *kernel.HTTPError
	if err == nil || !errors.As(err, &httpErr) {
		return 0, false
	}
	return httpErr.Status, true
}
