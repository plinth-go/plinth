package kernel

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileSetsSecurityAndAttachmentHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(path, []byte("id,name\n1,alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	err := WriteFile(rec, httptest.NewRequest(http.MethodGet, "/report", nil), path, "report.csv")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "report.csv") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHeadersAdapterReturnsUnderlyingHeader(t *testing.T) {
	headers := Headers{"X-Test": {"value"}}
	if got := headers.ResponseHeaders().Get("X-Test"); got != "value" {
		t.Fatalf("X-Test = %q", got)
	}
	headers["X-Test"][0] = "changed"
	if got := headers.ResponseHeaders().Get("X-Test"); got != "changed" {
		t.Fatalf("adapter did not share the header map: %q", got)
	}
}

func TestCopyStreamsAttachment(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Copy(rec, "report.txt", "text/plain; charset=utf-8", strings.NewReader("report body")); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "report body" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "report.txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestWriteCSVToPropagatesCallbackFailure(t *testing.T) {
	var dst strings.Builder
	err := WriteCSVTo(&dst, []string{"id", "name"}, func(yield func([]string) error) error {
		if err := yield([]string{"1", "alpha"}); err != nil {
			return err
		}
		return errors.New("row failure")
	})
	if err == nil || !strings.Contains(err.Error(), "row failure") {
		t.Fatalf("error = %v", err)
	}
	if got := dst.String(); got != "" {
		t.Fatalf("body = %q", got)
	}
}

func TestWriteFileSupportsRangeAndConditionalRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	rangeRec := httptest.NewRecorder()
	rangeReq := httptest.NewRequest(http.MethodGet, "/report", nil)
	rangeReq.Header.Set("Range", "bytes=2-5")
	if err := WriteFile(rangeRec, rangeReq, path, ""); err != nil {
		t.Fatal(err)
	}
	if rangeRec.Code != http.StatusPartialContent || rangeRec.Body.String() != "2345" {
		t.Fatalf("range status = %d, body = %q", rangeRec.Code, rangeRec.Body.String())
	}
	if got := rangeRec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", got)
	}

	headRec := httptest.NewRecorder()
	if err := WriteFile(headRec, httptest.NewRequest(http.MethodHead, "/report", nil), path, ""); err != nil {
		t.Fatal(err)
	}
	lastModified := headRec.Header().Get("Last-Modified")
	if lastModified == "" {
		t.Fatal("Last-Modified is empty")
	}

	notModified := httptest.NewRecorder()
	conditional := httptest.NewRequest(http.MethodGet, "/report", nil)
	conditional.Header.Set("If-Modified-Since", lastModified)
	if err := WriteFile(notModified, conditional, path, ""); err != nil {
		t.Fatal(err)
	}
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional status = %d, body = %q", notModified.Code, notModified.Body.String())
	}
}

func TestWriteFileRejectsNonRegularFiles(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteFile(rec, httptest.NewRequest(http.MethodGet, "/dir", nil), t.TempDir(), "")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("error = %v", err)
	}
	if len(rec.Header()) != 0 {
		t.Fatalf("headers mutated before validation: %#v", rec.Header())
	}
}

func TestWriteAttachmentRejectsInvalidHeaderValuesBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		filename    string
		contentType string
	}{
		{name: "invalid content type", filename: "report.csv", contentType: "text/csv\r\nX-Injected: true"},
		{name: "invalid filename", filename: "report\r\nX-Injected: true.csv", contentType: "text/csv"},
		{name: "forward path separator", filename: "../report.csv", contentType: "text/csv"},
		{name: "backward path separator", filename: `dir\report.csv`, contentType: "text/csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := WriteAttachment(rec, tc.filename, tc.contentType, func(io.Writer) error { return nil })
			if err == nil {
				t.Fatal("invalid header value was accepted")
			}
			if rec.Header().Get("Content-Type") != "" || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("X-Injected") != "" {
				t.Fatalf("headers were committed: %#v", rec.Header())
			}
		})
	}
}

func TestWriteAttachmentEncodesUnicodeFilename(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteAttachment(rec, "报告.csv", "text/csv; charset=utf-8", func(io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "filename*=utf-8''") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if strings.ContainsAny(disposition, "\r\n") {
		t.Fatalf("Content-Disposition contains newline: %q", disposition)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestWriteFileRejectsInvalidDownloadName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	err := WriteFile(rec, httptest.NewRequest(http.MethodGet, "/report", nil), path, "bad\r\nname.txt")
	if err == nil {
		t.Fatal("invalid download name was accepted")
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if len(rec.Header()) != 0 {
		t.Fatalf("headers mutated before validation: %#v", rec.Header())
	}
}

func TestWriteFileErrorAndCachePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	preserved := httptest.NewRecorder()
	preserved.Header().Set("Cache-Control", "private, max-age=60")
	if err := WriteFile(preserved, httptest.NewRequest(http.MethodGet, "/report", nil), path, ""); err != nil {
		t.Fatal(err)
	}
	if got := preserved.Header().Get("Cache-Control"); got != "private, max-age=60" {
		t.Fatalf("success Cache-Control = %q", got)
	}

	badRange := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/report", nil)
	request.Header.Set("Range", "bytes=100-200")
	badRange.Header().Set("Cache-Control", "public, max-age=3600")
	if err := WriteFile(badRange, request, path, ""); err != nil {
		t.Fatal(err)
	}
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, body = %q", badRange.Code, badRange.Body.String())
	}
	if got := badRange.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("error Cache-Control = %q", got)
	}
	if got := badRange.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestWriteAttachmentPreservesUpstreamCacheControl(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "private, max-age=60")
	if err := WriteAttachment(rec, "report.txt", "text/plain", func(io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=60" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestWriteAttachmentKeepsPartialOutputAfterFirstWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteAttachment(rec, "report.txt", "text/plain", func(dst io.Writer) error {
		if _, err := io.WriteString(dst, "partial"); err != nil {
			return err
		}
		return errors.New("source failed")
	})
	if err == nil {
		t.Fatal("writer error was ignored")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "report.txt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
}

func TestSetHeadersValidatesHeaderSyntax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{name: "empty name", headers: http.Header{"": {"value"}}},
		{name: "space in name", headers: http.Header{"Bad Header": {"value"}}},
		{name: "newline in value", headers: http.Header{"X-Test": {"a\r\nInjected: true"}}},
		{name: "control in value", headers: http.Header{"X-Test": {"a\x00b"}}},
		{name: "missing value", headers: http.Header{"X-Test": {}}},
		{name: "case-insensitive duplicate", headers: http.Header{"x-test": {"first"}, "X-Test": {"second"}}},
		{name: "content length", headers: http.Header{"Content-Length": {"999"}}},
		{name: "transfer encoding", headers: http.Header{"Transfer-Encoding": {"chunked"}}},
		{name: "trailer", headers: http.Header{"Trailer": {"X-Checksum"}}},
		{name: "connection", headers: http.Header{"Connection": {"close"}}},
		{name: "keep alive", headers: http.Header{"Keep-Alive": {"timeout=5"}}},
		{name: "proxy connection", headers: http.Header{"Proxy-Connection": {"keep-alive"}}},
		{name: "te", headers: http.Header{"TE": {"trailers"}}},
		{name: "upgrade", headers: http.Header{"Upgrade": {"websocket"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := SetHeaders(rec, tc.headers); err == nil {
				t.Fatal("invalid headers were accepted")
			}
			if len(rec.Header()) != 0 {
				t.Fatalf("headers were committed: %#v", rec.Header())
			}
		})
	}
}

func TestSetHeadersAppliesValidValues(t *testing.T) {
	rec := httptest.NewRecorder()
	headers := http.Header{"x-test": {"first", "second"}}
	if err := SetHeaders(rec, headers); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Values("X-Test"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("X-Test values = %#v", got)
	}
}

type nilWriter struct{}

func (*nilWriter) Write([]byte) (int, error) { panic("nil writer must not be called") }

type nilReader struct{}

func (*nilReader) Read([]byte) (int, error) { panic("nil reader must not be called") }

type nilResponseWriter struct{}

func (*nilResponseWriter) Header() http.Header { panic("nil response writer must not be called") }

func (*nilResponseWriter) Write([]byte) (int, error) { panic("nil response writer must not be called") }

func (*nilResponseWriter) WriteHeader(int) { panic("nil response writer must not be called") }

func TestTransferHelpersRejectTypedNilInputs(t *testing.T) {
	var writer *nilWriter
	rowsCalled := false
	err := WriteCSVTo(writer, []string{"id"}, func(yield func([]string) error) error {
		rowsCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "CSV destination is required") {
		t.Fatalf("CSV destination error = %v", err)
	}
	if rowsCalled {
		t.Fatal("CSV rows callback ran after destination validation failed")
	}

	var reader *nilReader
	rec := httptest.NewRecorder()
	if err := Copy(rec, "data.txt", "text/plain", reader); err == nil || !strings.Contains(err.Error(), "copy source is required") {
		t.Fatalf("copy source error = %v", err)
	}
	if len(rec.Header()) != 0 {
		t.Fatalf("copy committed headers: %#v", rec.Header())
	}

	var responseWriter *nilResponseWriter
	writeCalled := false
	if err := WriteAttachment(responseWriter, "data.txt", "text/plain", func(io.Writer) error {
		writeCalled = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "response writer is required") {
		t.Fatalf("attachment writer error = %v", err)
	}
	if writeCalled {
		t.Fatal("attachment callback ran after writer validation failed")
	}
	if err := SetHeaders(responseWriter, http.Header{"X-Test": {"value"}}); err == nil || !strings.Contains(err.Error(), "response writer is required") {
		t.Fatalf("SetHeaders error = %v", err)
	}

	var request *http.Request
	if err := WriteFile(rec, request, "unused", ""); err == nil || !strings.Contains(err.Error(), "HTTP request is required") {
		t.Fatalf("WriteFile request error = %v", err)
	}
	if err := WriteFile(responseWriter, httptest.NewRequest(http.MethodGet, "/file", nil), "unused", ""); err == nil || !strings.Contains(err.Error(), "response writer is required") {
		t.Fatalf("WriteFile writer error = %v", err)
	}
}

func TestSetJSONHeadersDoesNotAliasAppends(t *testing.T) {
	header := make(http.Header)
	setJSONHeaders(header)
	header.Add("X-Content-Type-Options", "extra-sniff")
	header.Add("Content-Type", "extra-type")

	if got := strings.Join(header.Values("X-Content-Type-Options"), ","); got != "nosniff,extra-sniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := strings.Join(header.Values("Content-Type"), ","); got != "application/json; charset=utf-8,extra-type" {
		t.Fatalf("Content-Type = %q", got)
	}
}
