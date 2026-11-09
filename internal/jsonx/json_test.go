package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMarshalResponseMatchesLegacyEncoder(t *testing.T) {
	type payload struct {
		HTML    string            `json:"html"`
		NilMap  map[string]string `json:"nil_map"`
		NilList []int             `json:"nil_list"`
		Ordered map[string]int    `json:"ordered"`
	}
	value := payload{
		HTML:    "<tag>&\u2028\u2029",
		Ordered: map[string]int{"z": 1, "a": 2},
	}

	var legacy bytes.Buffer
	encoder := json.NewEncoder(&legacy)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	got, err := MarshalResponse(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacy.Bytes()) {
		t.Fatalf("response JSON = %q, legacy = %q", got, legacy.Bytes())
	}
}

func TestDecodeRejectsNullAtEveryDepthWithoutMutatingTarget(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type payload struct {
		Name   string            `json:"name"`
		Values map[string]string `json:"values"`
		Nested nested            `json:"nested"`
	}

	for _, input := range []string{
		`null`,
		`{"name":null}`,
		`{"values":{"key":null}}`,
		`{"nested":{"value":null}}`,
	} {
		t.Run(input, func(t *testing.T) {
			out := payload{Name: "unchanged"}
			err := DecodeOne(strings.NewReader(input), &out)
			if !errors.Is(err, ErrNull) {
				t.Fatalf("error = %v, want ErrNull", err)
			}
			if out.Name != "unchanged" {
				t.Fatalf("target mutated: %#v", out)
			}
		})
	}
}

func TestDecodeAllowsNullStringContent(t *testing.T) {
	var out struct {
		Value string `json:"value"`
	}
	if err := DecodeOne(strings.NewReader(`{"value":"null"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Value != "null" {
		t.Fatalf("value = %q", out.Value)
	}
}

func TestDecodeOneRejectsMultipleValuesWithoutMutation(t *testing.T) {
	out := struct {
		Value string `json:"value"`
	}{Value: "unchanged"}
	err := DecodeOne(strings.NewReader(`{"value":"first"} {"value":"second"}`), &out)
	if !errors.Is(err, ErrMultipleValues) {
		t.Fatalf("error = %v, want ErrMultipleValues", err)
	}
	if out.Value != "unchanged" {
		t.Fatalf("target mutated: %#v", out)
	}
}

func TestDecodeNullScannerIgnoresStringsAndEscapes(t *testing.T) {
	var out struct {
		Value string `json:"value"`
	}
	if err := DecodeOne(strings.NewReader(`{"value":"quote: \" null"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Value != `quote: " null` {
		t.Fatalf("value = %q", out.Value)
	}
}
