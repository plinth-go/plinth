package jsonx

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// ValidateType rejects Go types that cannot be decoded from JSON under the
// framework's strict policy. Types that implement json.Unmarshaler or
// encoding.TextUnmarshaler are accepted because they own their wire format.
func ValidateType(t reflect.Type) error {
	if t == nil {
		return fmt.Errorf("JSON type is nil")
	}
	return validateType(t, "", make(map[reflect.Type]bool))
}

func validateType(t reflect.Type, path string, seen map[reflect.Type]bool) error {
	if implementsCustomUnmarshal(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		if seen[t] {
			return nil
		}
		seen[t] = true
	}

	switch t.Kind() {
	case reflect.Bool,
		reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return typeError(path, t, "JSON value")
		}
		return nil
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return validateType(t.Elem(), path+"[]", seen)
	case reflect.Map:
		if !supportsJSONMapKey(t.Key()) {
			return typeError(path, t.Key(), "map key")
		}
		return validateType(t.Elem(), path+"[]", seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Tag.Get("json") == "-" {
				continue
			}
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			fieldPath := field.Name
			if path != "" {
				fieldPath = path + "." + field.Name
			}
			if err := validateType(field.Type, fieldPath, seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Func, reflect.Chan, reflect.Complex64, reflect.Complex128, reflect.UnsafePointer:
		return typeError(path, t, "JSON value")
	default:
		return typeError(path, t, "JSON value")
	}
}

func implementsCustomUnmarshal(t reflect.Type) bool {
	if t.Implements(jsonUnmarshalerType) || t.Implements(textUnmarshalerType) {
		return true
	}
	pointer := reflect.PointerTo(t)
	return pointer.Implements(jsonUnmarshalerType) || pointer.Implements(textUnmarshalerType)
}

func supportsJSONMapKey(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return t.Implements(textUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func typeError(path string, t reflect.Type, context string) error {
	err := fmt.Errorf("unsupported %s type %s", context, t)
	if path == "" {
		return err
	}
	return fmt.Errorf("%s: %w", path, err)
}
