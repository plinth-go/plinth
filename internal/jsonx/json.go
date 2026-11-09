package jsonx

import (
	"bytes"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"reflect"
	"strings"
)

var (
	ErrNull           = errors.New("JSON value must not be null")
	ErrMultipleValues = errors.New("JSON input must contain exactly one value")
	decodeOptions     = jsonv2.JoinOptions(
		json.DefaultOptionsV1(),
		json.ReportErrorsWithLegacySemantics(false),
		jsontext.AllowDuplicateNames(false),
	)
	responseOptions = jsonv2.JoinOptions(
		json.DefaultOptionsV1(),
		jsontext.EscapeForHTML(false),
	)
)

// Decoder applies the framework's single strict JSON policy while retaining
// encoding/json v1 value semantics.
type Decoder struct {
	reader  io.Reader
	decoded bool
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{reader: r}
}

// Decode unmarshals one value and rejects null anywhere in the JSON value. The
// target is only updated after a successful, non-null decode.
func (d *Decoder) Decode(out any) error {
	if d == nil || d.reader == nil {
		return errors.New("jsonx: decoder is nil")
	}
	if d.decoded {
		return ErrMultipleValues
	}
	value := reflect.ValueOf(out)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("jsonx: decode target must be a non-nil pointer")
	}

	raw, err := io.ReadAll(d.reader)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return io.EOF
	}
	if err := rejectNull(raw); err != nil {
		return err
	}

	target := reflect.New(reflect.PointerTo(value.Elem().Type()))
	if err := jsonv2.Unmarshal(raw, target.Interface(), decodeOptions, jsonv2.RejectUnknownMembers(true)); err != nil {
		return normalizeDecodeError(err)
	}
	if target.Elem().IsNil() {
		return ErrNull
	}
	value.Elem().Set(target.Elem().Elem())
	d.decoded = true
	return nil
}

func normalizeDecodeError(err error) error {
	var syntax *jsontext.SyntacticError
	if errors.As(err, &syntax) && syntax.Err != nil && strings.Contains(syntax.Err.Error(), "after top-level value") {
		return ErrMultipleValues
	}
	return err
}

func rejectNull(raw jsontext.Value) error {
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		if ch == 'n' && i+3 < len(raw) && raw[i+1] == 'u' && raw[i+2] == 'l' && raw[i+3] == 'l' {
			return ErrNull
		}
	}
	return nil
}

func (d *Decoder) RequireEOF() error {
	if d == nil || d.reader == nil {
		return errors.New("jsonx: decoder is nil")
	}
	if d.decoded {
		return nil
	}
	return errors.New("jsonx: no JSON value decoded")
}

func DecodeOne(r io.Reader, out any) error {
	decoder := NewDecoder(r)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return decoder.RequireEOF()
}

// MarshalResponse encodes one HTTP response value using the framework's
// encoding/json v1 compatibility policy. Output is fully buffered so callers
// can commit headers only after encoding succeeds.
func MarshalResponse(out any) ([]byte, error) {
	body, err := jsonv2.Marshal(out, responseOptions)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}
