package kernel

import (
	"encoding/csv"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Headers embeds into JSON response structs to add dynamic headers without
// changing the framework's handler signature.
type Headers http.Header

func (h Headers) ResponseHeaders() http.Header { return http.Header(h) }

func writeBody(w io.Writer, body []byte) error {
	n, err := w.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

const (
	noSniffValue         = "nosniff"
	jsonContentTypeValue = "application/json; charset=utf-8"
	problemContentType   = "application/problem+json; charset=utf-8"
)

func setNoSniffHeader(header http.Header) {
	header["X-Content-Type-Options"] = []string{noSniffValue}
}

func setJSONHeaders(header http.Header) {
	// Share one backing array to avoid two identical slice allocations. The
	// full slice expressions cap each header at its element so Header.Add can
	// never append into the other header's value.
	values := []string{noSniffValue, jsonContentTypeValue}
	header["X-Content-Type-Options"] = values[:1:1]
	header["Content-Type"] = values[1:2:2]
}

func setProblemHeaders(header http.Header) {
	values := []string{noSniffValue, problemContentType}
	header["X-Content-Type-Options"] = values[:1:1]
	header["Content-Type"] = values[1:2:2]
}

// SetHeaders validates and canonicalizes response headers before applying
// them. Invalid names, control characters in values, empty values, and
// case-insensitive duplicate names are rejected. Framing and hop-by-hop
// headers controlled by net/http are not allowed; use a raw handler when the
// endpoint intentionally owns those protocol details.
func SetHeaders(w http.ResponseWriter, headers http.Header) error {
	if isNilInterface(w) {
		return fmt.Errorf("response writer is required")
	}
	if err := validateHeaders(headers); err != nil {
		return err
	}
	for key, values := range headers {
		w.Header()[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	return nil
}

// Attachment is a typed output that streams a generated file. It applies the
// attachment headers and status so handlers only provide the filename, media
// type, and body writer.
type Attachment struct {
	Filename    string
	ContentType string
	Write       func(io.Writer) error
}

func (a Attachment) Respond(w http.ResponseWriter, _ *http.Request) error {
	return WriteAttachment(w, a.Filename, a.ContentType, a.Write)
}

// CSV is a typed output that streams a CSV attachment. It declares text/csv as
// response metadata for OpenAPI generation.
type CSV struct {
	Meta     `produces:"text/csv"`
	Filename string
	Header   []string
	Rows     func(yield func([]string) error) error
}

func (c CSV) Respond(w http.ResponseWriter, _ *http.Request) error {
	return WriteCSV(w, c.Filename, c.Header, c.Rows)
}

// WriteAttachment streams an attachment. Return an error from write to stop
// the response; once bytes have been written the error can only be logged.
func WriteAttachment(w http.ResponseWriter, filename, contentType string, write func(io.Writer) error) error {
	if isNilInterface(w) {
		return fmt.Errorf("attachment response writer is required")
	}
	if write == nil {
		return fmt.Errorf("attachment writer is required")
	}
	if filename == "" {
		filename = "download"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return fmt.Errorf("invalid attachment content type %q: %w", contentType, err)
	}
	disposition, err := contentDisposition(filename)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	if err := write(w); err != nil {
		return fmt.Errorf("write attachment: %w", err)
	}
	return nil
}

// WriteCSV streams a CSV attachment. Return an error from the callback to stop
// the export; once bytes have been written the error can only be logged.
func WriteCSV(w http.ResponseWriter, filename string, header []string, rows func(yield func([]string) error) error) error {
	if rows == nil {
		return fmt.Errorf("CSV rows callback is required")
	}
	if filename == "" {
		filename = "export.csv"
	}
	return WriteAttachment(w, filename, "text/csv; charset=utf-8", func(dst io.Writer) error {
		return WriteCSVTo(dst, header, rows)
	})
}

// WriteCSVTo writes CSV bytes to dst without setting response headers.
func WriteCSVTo(dst io.Writer, header []string, rows func(yield func([]string) error) error) error {
	if isNilInterface(dst) {
		return fmt.Errorf("CSV destination is required")
	}
	if rows == nil {
		return fmt.Errorf("CSV rows callback is required")
	}
	writer := csv.NewWriter(dst)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}
	if err := rows(func(record []string) error {
		return writer.Write(record)
	}); err != nil {
		return fmt.Errorf("write CSV rows: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush CSV: %w", err)
	}
	return nil
}

// WriteFile serves a single local file with range and conditional-request
// support, always setting nosniff. Pass downloadName to force an attachment;
// leave it empty to rely on the detected content type.
func WriteFile(w http.ResponseWriter, r *http.Request, path, downloadName string) error {
	if isNilInterface(w) {
		return fmt.Errorf("response writer is required")
	}
	if isNilInterface(r) {
		return fmt.Errorf("HTTP request is required")
	}
	disposition := ""
	if downloadName != "" {
		var err error
		disposition, err = contentDisposition(downloadName)
		if err != nil {
			return err
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return NotFound("file not found")
	}
	file, err := os.Open(path)
	if err != nil {
		return NotFound("file not found")
	}
	defer file.Close()

	info, err = file.Stat()
	if err != nil {
		return Internal(err)
	}
	if !info.Mode().IsRegular() {
		return NotFound("file not found")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if w.Header().Get("Content-Type") == "" {
		if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
	}
	if disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	http.ServeContent(&fileResponseWriter{ResponseWriter: w}, r, info.Name(), info.ModTime(), file)
	return nil
}

type fileResponseWriter struct {
	http.ResponseWriter
}

func (w *fileResponseWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *fileResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *fileResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{w}, src)
}

// Copy writes a reader into a response with explicit headers. It is useful for
// generated downloads that are not backed by a file on disk.
func Copy(w http.ResponseWriter, filename, contentType string, src io.Reader) error {
	if isNilInterface(src) {
		return fmt.Errorf("copy source is required")
	}
	return WriteAttachment(w, filename, contentType, func(dst io.Writer) error {
		if _, err := io.Copy(dst, src); err != nil {
			return fmt.Errorf("copy response body: %w", err)
		}
		return nil
	})
}

func contentDisposition(filename string) (string, error) {
	if strings.ContainsAny(filename, "/\\") {
		return "", fmt.Errorf("invalid attachment filename %q: path separators are not allowed", filename)
	}
	if strings.IndexFunc(filename, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("invalid attachment filename %q: control characters are not allowed", filename)
	}
	value := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if value == "" {
		return "", fmt.Errorf("invalid attachment filename %q", filename)
	}
	return value, nil
}
