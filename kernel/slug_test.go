package kernel

import (
	"reflect"
	"strings"
	"testing"
)

func TestMakeSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "Hello Fancy World", want: "hello-fancy-world"},
		{input: "C'est déjà l'été.", want: "cest-deja-lete"},
		{input: "This & that", want: "this-and-that"},
		{input: "A @ B", want: "a-at-b"},
		{input: "Hello—World", want: "hello-world"},
		{input: "kožušček", want: "kozuscek"},
		{input: "北京", want: "bei-jing"},
		{input: "影師", want: "ying-shi"},
		{input: "你好，世界", want: "ni-hao-shi-jie"},
		{input: "Привет мир", want: "privet-mir"},
		{input: "  _hello__world_  ", want: "hello__world"},
		{input: "!!!", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := makeSlug(tt.input); got != tt.want {
				t.Fatalf("makeSlug(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSlugTagAcceptsCanonicalForms(t *testing.T) {
	tests := []struct {
		name   string
		tag    string
		active bool
		want   slugTag
	}{
		{name: "empty", tag: ""},
		{name: "disabled", tag: "-"},
		{name: "positional source", tag: "title", active: true, want: slugTag{source: "title"}},
		{name: "canonical options", tag: "source=title,max=80,overwrite", active: true, want: slugTag{source: "title", max: 80, overwrite: true}},
		{name: "whitespace", tag: " source = title , max = 24 ", active: true, want: slugTag{source: "title", max: 24}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, active, err := parseSlugTag(tt.tag)
			if err != nil {
				t.Fatalf("parseSlugTag(%q) error = %v", tt.tag, err)
			}
			if active != tt.active {
				t.Fatalf("active = %v, want %v", active, tt.active)
			}
			if got != tt.want {
				t.Fatalf("rule = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSlugTagRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want string
	}{
		{name: "empty option", tag: "title,", want: "empty option"},
		{name: "empty first option", tag: ",title", want: "empty option"},
		{name: "zero max", tag: "title,max=0", want: "positive integer"},
		{name: "negative max", tag: "title,max=-1", want: "positive integer"},
		{name: "non numeric max", tag: "title,max=abc", want: "positive integer"},
		{name: "unknown option", tag: "title,unknown=1", want: "unknown slug option"},
		{name: "source alias", tag: "from=title", want: "unknown slug option"},
		{name: "missing source", tag: "max=80", want: "requires a source field"},
		{name: "empty source", tag: "source=", want: "requires a source field"},
		{name: "duplicate source", tag: "source=title,source=name", want: "source more than once"},
		{name: "positional and option source", tag: "title,source=name", want: "source more than once"},
		{name: "duplicate max", tag: "title,max=1,max=2", want: "max is declared more than once"},
		{name: "duplicate overwrite", tag: "title,overwrite,overwrite", want: "overwrite is declared more than once"},
		{name: "overwrite value", tag: "title,overwrite=yes", want: "does not take a value"},
		{name: "max without value", tag: "title,max", want: "max requires a value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseSlugTag(tt.tag)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseSlugTag(%q) error = %v, want substring %q", tt.tag, err, tt.want)
			}
		})
	}
}

func TestCheckSlugFieldsRejectsInvalidDeclarations(t *testing.T) {
	type nonStringTarget struct {
		Title string `json:"title"`
		Slug  int    `json:"slug" slug:"title"`
	}
	type missingSource struct {
		Slug string `json:"slug" slug:"missing"`
	}
	type selfSource struct {
		Slug string `json:"slug" slug:"slug"`
	}
	type chainedSource struct {
		Title string `json:"title"`
		Name  string `json:"name" slug:"source=title"`
		Slug  string `json:"slug" slug:"source=name"`
	}
	type nestedBadSource struct {
		Item struct {
			Slug string `json:"slug" slug:"missing"`
		} `json:"item"`
	}

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{name: "non string target", typ: reflect.TypeFor[nonStringTarget](), want: "slug target must be string"},
		{name: "missing source", typ: reflect.TypeFor[missingSource](), want: "is not a string field in the same struct"},
		{name: "self source", typ: reflect.TypeFor[selfSource](), want: "must not reference itself"},
		{name: "chained source", typ: reflect.TypeFor[chainedSource](), want: "must not itself declare slug generation"},
		{name: "nested bad source", typ: reflect.TypeFor[nestedBadSource](), want: "is not a string field in the same struct"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSlugFields(tt.typ, map[reflect.Type]bool{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("checkSlugFields error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCheckSlugFieldsAcceptsNestedAndJSONNameSources(t *testing.T) {
	type byJSONName struct {
		Title string `json:"headline"`
		Slug  string `json:"slug" slug:"source=headline,max=40"`
	}
	type byGoName struct {
		Title string `json:"headline"`
		Slug  string `json:"slug" slug:"source=Title"`
	}
	type payload struct {
		JSONName byJSONName   `json:"json_name"`
		GoName   byGoName     `json:"go_name"`
		Items    []byJSONName `json:"items"`
	}

	if err := checkSlugFields(reflect.TypeFor[payload](), map[reflect.Type]bool{}); err != nil {
		t.Fatalf("checkSlugFields error = %v", err)
	}
}

func TestHasSlugFieldsFindsNestedDeclarations(t *testing.T) {
	type plain struct {
		Name string `json:"name"`
	}
	type direct struct {
		Title string `json:"title"`
		Slug  string `json:"slug" slug:"title"`
	}
	type nested struct {
		Item  direct   `json:"item"`
		Items []direct `json:"items"`
	}
	type pointerNested struct {
		Item *direct `json:"item"`
	}

	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{name: "plain", typ: reflect.TypeFor[plain]()},
		{name: "direct", typ: reflect.TypeFor[direct](), want: true},
		{name: "nested", typ: reflect.TypeFor[nested](), want: true},
		{name: "pointer nested", typ: reflect.TypeFor[pointerNested](), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasSlugFields(tc.typ, map[reflect.Type]bool{}); got != tc.want {
				t.Fatalf("hasSlugFields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplySlugsHandlesNestedJSONNameSources(t *testing.T) {
	type nested struct {
		Title string `json:"headline"`
		Slug  string `json:"slug" slug:"source=headline,max=12"`
	}
	type payload struct {
		Item  nested   `json:"item"`
		Items []nested `json:"items"`
	}

	value := payload{
		Item:  nested{Title: "Hello Fancy World"},
		Items: []nested{{Title: "Second Fancy World"}},
	}
	applySlugs(&value)
	if value.Item.Slug != "hello-fancy" {
		t.Fatalf("item slug = %q", value.Item.Slug)
	}
	if value.Items[0].Slug != "second-fancy" {
		t.Fatalf("items[0] slug = %q", value.Items[0].Slug)
	}
}
