package jsonx

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

type customJSONPayload string

func (*customJSONPayload) UnmarshalJSON([]byte) error { return nil }

type textPayload string

func (*textPayload) UnmarshalText([]byte) error { return nil }

type textMapKey string

func (*textMapKey) UnmarshalText([]byte) error { return nil }

type customFuncPayload func()

func (*customFuncPayload) UnmarshalJSON([]byte) error { return nil }

type methodInterface interface {
	Marker()
}

type recursivePayload []recursivePayload

func TestValidateTypeAcceptsJSONDecodableTypes(t *testing.T) {
	type validPayload struct {
		Name    string
		Count   int
		Tags    []string
		Lookup  map[string]any
		Integer map[int]string
		TextKey map[textMapKey]string
		Raw     json.RawMessage
		Custom  customJSONPayload
		Text    textPayload
		Ignored chan int `json:"-"`
		private chan int
	}
	for _, typ := range []reflect.Type{
		reflect.TypeFor[validPayload](),
		reflect.TypeFor[any](),
		reflect.TypeFor[recursivePayload](),
		reflect.TypeFor[customFuncPayload](),
	} {
		if err := ValidateType(typ); err != nil {
			t.Fatalf("ValidateType(%s) error = %v", typ, err)
		}
	}
}

func TestValidateTypeRejectsUnsupportedJSONTypes(t *testing.T) {
	type nested struct {
		Work chan int
	}
	type dashNamed struct {
		Work chan int `json:"-,"`
	}
	tests := []reflect.Type{
		reflect.TypeFor[chan int](),
		reflect.TypeFor[func()](),
		reflect.TypeFor[complex64](),
		reflect.TypeFor[unsafe.Pointer](),
		reflect.TypeFor[nested](),
		reflect.TypeFor[dashNamed](),
		reflect.TypeFor[[]chan int](),
		reflect.TypeFor[map[string]func()](),
		reflect.TypeFor[map[bool]string](),
		reflect.TypeFor[methodInterface](),
		reflect.TypeFor[*chan int](),
	}
	for _, typ := range tests {
		err := ValidateType(typ)
		if err == nil {
			t.Fatalf("ValidateType(%s) succeeded", typ)
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("ValidateType(%s) error = %v", typ, err)
		}
	}
}

func TestValidateTypeReportsFieldPath(t *testing.T) {
	type nested struct {
		Work chan int
	}
	err := ValidateType(reflect.TypeFor[struct{ Child nested }]())
	if err == nil || !strings.Contains(err.Error(), "Child.Work") {
		t.Fatalf("error = %v", err)
	}
}
