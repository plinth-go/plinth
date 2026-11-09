package kernel

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/gosimple/unidecode"
)

var (
	slugInvalidChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	slugRepeatedDash = regexp.MustCompile(`-+`)
	slugReplacer     = strings.NewReplacer(
		`"`, "",
		"'", "",
		"’", "",
		"‒", "-",
		"–", "-",
		"—", "-",
		"―", "-",
		"&", "and",
		"@", "at",
	)
)

func makeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = slugReplacer.Replace(s)
	s = unidecode.Unidecode(s)
	s = strings.ToLower(s)
	s = slugInvalidChars.ReplaceAllString(s, "-")
	s = slugRepeatedDash.ReplaceAllString(s, "-")
	return strings.Trim(s, "-_")
}

// applySlugs fills string fields marked with a slug tag after request binding
// and before validation. Supported tag forms:
//
//	slug:"title"
//	slug:"source=title,max=80,overwrite"
//
// By default an existing non-empty value is preserved. The overwrite option
// makes the generated value authoritative.
func applySlugs(value any) {
	if value == nil {
		return
	}
	fillSlugs(reflect.ValueOf(value), map[uintptr]bool{})
}

func hasSlugFields(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = indirectType(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			if _, active, _ := parseSlugTag(field.Tag.Get("slug")); active {
				return true
			}
			if hasSlugFields(field.Type, seen) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		return hasSlugFields(t.Elem(), seen)
	}
	return false
}

func fillSlugs(v reflect.Value, seen map[uintptr]bool) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if ptr != 0 {
			if seen[ptr] {
				return
			}
			seen[ptr] = true
		}
		fillSlugs(v.Elem(), seen)
		return
	}

	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			field := v.Field(i)
			rule, ok, err := parseSlugTag(t.Field(i).Tag.Get("slug"))
			if err != nil {
				panic("kernel: " + err.Error())
			}
			if ok && field.Kind() == reflect.String && field.CanSet() {
				source, found := stringFieldByJSONName(v, rule.source)
				if !found || source == "" {
					continue
				}
				if field.String() != "" && !rule.overwrite {
					continue
				}
				generated := makeSlug(source)
				if rule.max > 0 && len(generated) > rule.max {
					generated = strings.Trim(generated[:rule.max], "-")
				}
				if generated != "" {
					field.SetString(generated)
				}
			}
			fillSlugs(field, seen)
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return
		}
		for i := 0; i < v.Len(); i++ {
			fillSlugs(v.Index(i), seen)
		}
	}
}

type slugTag struct {
	source    string
	max       int
	overwrite bool
}

func parseSlugTag(tag string) (slugTag, bool, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "-" {
		return slugTag{}, false, nil
	}

	var rule slugTag
	sourceSet := false
	maxSet := false
	overwriteSet := false
	for i, raw := range strings.Split(tag, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			return slugTag{}, false, fmt.Errorf("slug tag contains an empty option")
		}
		key, value, hasValue := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if i == 0 && !hasValue {
			rule.source = key
			sourceSet = true
			continue
		}
		switch key {
		case "source":
			if !hasValue || value == "" {
				return slugTag{}, false, fmt.Errorf("slug option %q requires a source field", key)
			}
			if sourceSet {
				return slugTag{}, false, fmt.Errorf("slug tag declares its source more than once")
			}
			rule.source = value
			sourceSet = true
		case "max":
			if !hasValue {
				return slugTag{}, false, fmt.Errorf("slug option max requires a value")
			}
			if maxSet {
				return slugTag{}, false, fmt.Errorf("slug option max is declared more than once")
			}
			maximum, err := strconv.Atoi(value)
			if err != nil || maximum <= 0 {
				return slugTag{}, false, fmt.Errorf("slug option max must be a positive integer, got %q", value)
			}
			rule.max = maximum
			maxSet = true
		case "overwrite":
			if hasValue {
				return slugTag{}, false, fmt.Errorf("slug option overwrite does not take a value")
			}
			if overwriteSet {
				return slugTag{}, false, fmt.Errorf("slug option overwrite is declared more than once")
			}
			rule.overwrite = true
			overwriteSet = true
		default:
			return slugTag{}, false, fmt.Errorf("unknown slug option %q", key)
		}
	}
	if !sourceSet {
		return slugTag{}, false, fmt.Errorf("slug tag requires a source field")
	}
	return rule, true, nil
}

func checkSlugFields(t reflect.Type, seen map[reflect.Type]bool) error {
	t = indirectType(t)
	switch t.Kind() {
	case reflect.Struct:
		if seen[t] {
			return nil
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			rule, active, err := parseSlugTag(field.Tag.Get("slug"))
			if err != nil {
				return fmt.Errorf("%s.%s: %w", t, field.Name, err)
			}
			if active {
				if field.Type.Kind() != reflect.String {
					return fmt.Errorf("%s.%s slug target must be string, got %s", t, field.Name, field.Type)
				}
				source, found := slugSourceField(t, rule.source)
				if !found {
					return fmt.Errorf("%s.%s slug source %q is not a string field in the same struct", t, field.Name, rule.source)
				}
				if source.Name == field.Name {
					return fmt.Errorf("%s.%s slug source must not reference itself", t, field.Name)
				}
				if _, chained, err := parseSlugTag(source.Tag.Get("slug")); err != nil {
					return fmt.Errorf("%s.%s: %w", t, source.Name, err)
				} else if chained {
					return fmt.Errorf("%s.%s slug source %q must not itself declare slug generation", t, field.Name, rule.source)
				}
			}
			if err := checkSlugFields(field.Type, seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		return checkSlugFields(t.Elem(), seen)
	}
	return nil
}

func slugSourceField(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" || field.Type.Kind() != reflect.String {
			continue
		}
		if field.Name != name && jsonName(field) != name {
			continue
		}
		return field, true
	}
	return reflect.StructField{}, false
}

func stringFieldByJSONName(v reflect.Value, name string) (string, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" || field.Type.Kind() != reflect.String {
			continue
		}
		jsonName := jsonName(field)
		if field.Name != name && jsonName != name {
			continue
		}
		return v.Field(i).String(), true
	}
	return "", false
}
