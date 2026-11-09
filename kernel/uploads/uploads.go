// Package uploads provides bounded streaming multipart/form-data parsing. It
// buffers files in a private temporary directory and gives the caller explicit
// cleanup ownership through Form.Close.
package uploads

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/plinth-go/plinth/kernel"
)

const (
	defaultMaxFiles      = 8
	defaultMaxFields     = 32
	defaultMaxFileBytes  = 16 << 20
	defaultMaxTotalBytes = 32 << 20
	defaultMaxFieldBytes = 64 << 10
)

// Config bounds one multipart request. Zero values select safe defaults.
type Config struct {
	MaxFiles            int
	MaxFields           int
	MaxFileBytes        int64
	MaxTotalBytes       int64
	MaxFieldBytes       int64
	TempDir             string
	AllowedContentTypes []string
}

// Parser validates and parses multipart requests.
type Parser struct {
	cfg         Config
	allowed     []mediaRange
	contentType string
}

type mediaRange struct {
	kind    string
	subtype string
}

// File is one uploaded file stored in the form's private temporary directory.
type File struct {
	Field       string
	Filename    string
	ContentType string
	Size        int64
	header      textproto.MIMEHeader
	path        string
}

// Header returns a copy of the original multipart part headers.
func (f *File) Header() textproto.MIMEHeader {
	if f == nil {
		return nil
	}
	return cloneHeader(f.header)
}

// Open opens the temporary file for reading.
func (f *File) Open() (*os.File, error) {
	if f == nil || f.path == "" {
		return nil, errors.New("upload file is closed")
	}
	return os.Open(f.path)
}

// Form contains uploaded values and files. Call Close when the response is
// complete to remove all temporary files.
type Form struct {
	Values map[string][]string
	Files  []*File

	dir    string
	closed bool
}

// Value returns the first value for name.
func (f *Form) Value(name string) string {
	if f == nil || len(f.Values[name]) == 0 {
		return ""
	}
	return f.Values[name][0]
}

// Close removes every temporary file. It is idempotent.
func (f *Form) Close() error {
	if f == nil || f.closed {
		return nil
	}
	f.closed = true
	for _, file := range f.Files {
		file.path = ""
	}
	if f.dir == "" {
		return nil
	}
	return os.RemoveAll(f.dir)
}

// New validates config and constructs a parser.
func New(cfg Config) (*Parser, error) {
	if cfg.MaxFiles == 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	if cfg.MaxFields == 0 {
		cfg.MaxFields = defaultMaxFields
	}
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.MaxTotalBytes == 0 {
		cfg.MaxTotalBytes = defaultMaxTotalBytes
	}
	if cfg.MaxFieldBytes == 0 {
		cfg.MaxFieldBytes = defaultMaxFieldBytes
	}
	switch {
	case cfg.MaxFiles < 0:
		return nil, errors.New("upload max files must be positive")
	case cfg.MaxFields < 0:
		return nil, errors.New("upload max fields must be positive")
	case cfg.MaxFileBytes < 0:
		return nil, errors.New("upload max file bytes must be positive")
	case cfg.MaxTotalBytes < 0:
		return nil, errors.New("upload max total bytes must be positive")
	case cfg.MaxFieldBytes < 0:
		return nil, errors.New("upload max field bytes must be positive")
	case cfg.MaxTotalBytes < cfg.MaxFileBytes:
		return nil, errors.New("upload max total bytes must not be less than max file bytes")
	}
	allowed := make([]mediaRange, 0, len(cfg.AllowedContentTypes))
	for _, value := range cfg.AllowedContentTypes {
		kind, subtype, err := parseAllowedType(value)
		if err != nil {
			return nil, err
		}
		allowed = append(allowed, mediaRange{kind: kind, subtype: subtype})
	}
	return &Parser{cfg: cfg, allowed: allowed}, nil
}

// Parse reads one multipart request. It rejects non-multipart content types,
// oversized fields/files, too many parts, unsafe filenames, and disallowed
// content types. On failure every temporary file is removed before return.
func (p *Parser) Parse(r *http.Request) (*Form, error) {
	if p == nil {
		return nil, kernel.Internal(errors.New("upload parser is nil"))
	}
	if r == nil {
		return nil, kernel.BadRequest("upload request is nil")
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, kernel.BadRequest("request content type must be multipart/form-data")
	}
	dir, err := os.MkdirTemp(p.cfg.TempDir, "plinth-uploads-")
	if err != nil {
		return nil, kernel.Internal(fmt.Errorf("create upload directory: %w", err))
	}
	form := &Form{Values: make(map[string][]string), dir: dir}
	fail := func(err error) (*Form, error) {
		_ = form.Close()
		return nil, err
	}

	var total int64
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(kernel.BadRequest("read multipart body: " + err.Error()))
		}
		name := strings.TrimSpace(part.FormName())
		if name == "" {
			_ = part.Close()
			return fail(kernel.BadRequest("multipart part name is required"))
		}
		if part.FileName() == "" {
			value, err := readField(part, p.cfg.MaxFieldBytes, p.cfg.MaxTotalBytes-total)
			_ = part.Close()
			if err != nil {
				return fail(err)
			}
			total += int64(len(value))
			if len(form.Values) >= p.cfg.MaxFields && len(form.Values[name]) == 0 {
				return fail(kernel.BadRequest("too many multipart fields"))
			}
			form.Values[name] = append(form.Values[name], string(value))
			continue
		}
		if len(form.Files) >= p.cfg.MaxFiles {
			_ = part.Close()
			return fail(kernel.BadRequest("too many uploaded files"))
		}
		filename, err := safeFilename(part.FileName())
		if err != nil {
			_ = part.Close()
			return fail(kernel.BadRequest(err.Error()))
		}
		remaining := p.cfg.MaxTotalBytes - total
		limit := p.cfg.MaxFileBytes
		if remaining < limit {
			limit = remaining
		}
		if limit < 0 {
			limit = 0
		}
		file, size, err := p.storeFile(dir, name, filename, part, limit)
		_ = part.Close()
		if err != nil {
			return fail(err)
		}
		total += size
		form.Files = append(form.Files, file)
	}
	return form, nil
}

func (p *Parser) storeFile(dir, field, filename string, part *multipart.Part, limit int64) (*File, int64, error) {
	file, err := os.CreateTemp(dir, "file-")
	if err != nil {
		return nil, 0, kernel.Internal(fmt.Errorf("create upload file: %w", err))
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return nil, 0, kernel.Internal(fmt.Errorf("secure upload file: %w", err))
	}
	limited := &io.LimitedReader{R: part, N: limit + 1}
	written, err := io.Copy(file, limited)
	if err != nil {
		return nil, 0, kernel.BadRequest("read uploaded file: " + err.Error())
	}
	if written > limit {
		return nil, 0, kernel.TooLarge("uploaded file exceeds configured size limit")
	}
	if err := file.Sync(); err != nil {
		return nil, 0, kernel.Internal(fmt.Errorf("flush uploaded file: %w", err))
	}
	contentType, err := detectContentType(path, part.Header.Get("Content-Type"))
	if err != nil {
		return nil, 0, kernel.Internal(err)
	}
	if p.contentType != "" {
		contentType = p.contentType
	}
	if !p.allowedType(contentType) {
		return nil, 0, kernel.UnsupportedMediaType("uploaded file content type is not allowed")
	}
	remove = false
	return &File{
		Field: field, Filename: filename, ContentType: contentType, Size: written,
		header: cloneHeader(part.Header), path: path,
	}, written, nil
}

func readField(part io.Reader, fieldLimit, totalRemaining int64) ([]byte, error) {
	limit := fieldLimit
	if totalRemaining < limit {
		limit = totalRemaining
	}
	if limit < 0 {
		limit = 0
	}
	value, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return nil, kernel.BadRequest("read multipart field: " + err.Error())
	}
	if int64(len(value)) > limit {
		return nil, kernel.TooLarge("multipart field exceeds configured size limit")
	}
	return value, nil
}

func detectContentType(path, declared string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open uploaded file: %w", err)
	}
	defer file.Close()
	head := make([]byte, 512)
	n, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect uploaded file: %w", err)
	}
	detected := http.DetectContentType(head[:n])
	if declared != "" {
		if mediaType, _, err := mime.ParseMediaType(declared); err == nil && mediaType != "" && mediaType != "application/octet-stream" {
			return mediaType, nil
		}
	}
	mediaType, _, err := mime.ParseMediaType(detected)
	if err != nil {
		return detected, nil
	}
	return mediaType, nil
}

func safeFilename(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	if value == "" || value == "." || value == ".." {
		return "", errors.New("uploaded filename is invalid")
	}
	if !utf8.ValidString(value) || len(value) > 255 {
		return "", errors.New("uploaded filename must be valid UTF-8 and at most 255 bytes")
	}
	for _, char := range value {
		if char == 0 || unicode.IsControl(char) || char == '/' || char == '\\' {
			return "", errors.New("uploaded filename contains unsafe characters")
		}
	}
	return value, nil
}

func parseAllowedType(value string) (string, string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	kind, subtype, ok := strings.Cut(value, "/")
	if !ok || kind == "" || subtype == "" {
		return "", "", fmt.Errorf("upload content type %q must use type/subtype", value)
	}
	return kind, subtype, nil
}

func (p *Parser) allowedType(value string) bool {
	if len(p.allowed) == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	kind, subtype, ok := strings.Cut(strings.ToLower(mediaType), "/")
	if !ok {
		return false
	}
	for _, allowed := range p.allowed {
		if allowed.kind == kind && (allowed.subtype == "*" || allowed.subtype == subtype) {
			return true
		}
	}
	return false
}

func cloneHeader(header textproto.MIMEHeader) textproto.MIMEHeader {
	if header == nil {
		return nil
	}
	cloned := make(textproto.MIMEHeader, len(header))
	for name, values := range header {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}
