package utils

import "testing"

func TestNonNilSlice(t *testing.T) {
	if got := NonNilSlice[string](nil); got == nil || len(got) != 0 {
		t.Fatalf("NonNilSlice(nil) = %#v", got)
	}
	values := []string{"one", "two"}
	got := NonNilSlice(values)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("NonNilSlice(values) = %#v", got)
	}
	got[0] = "changed"
	if values[0] != "changed" {
		t.Fatal("NonNilSlice copied the input slice")
	}
}

func TestNormalizePage(t *testing.T) {
	tests := []struct {
		name                   string
		limit, offset          int
		defaultLimit, maxLimit int
		wantLimit, wantOffset  int
	}{
		{name: "uses defaults", defaultLimit: 20, maxLimit: 100, wantLimit: 20},
		{name: "caps limit", limit: 500, defaultLimit: 20, maxLimit: 100, wantLimit: 100},
		{name: "keeps offset", limit: 30, offset: 40, defaultLimit: 20, maxLimit: 100, wantLimit: 30, wantOffset: 40},
		{name: "clamps negative offset", limit: 30, offset: -1, defaultLimit: 20, maxLimit: 100, wantLimit: 30},
		{name: "normalizes invalid maximum", limit: 30, defaultLimit: 20, maxLimit: 0, wantLimit: 1},
		{name: "caps default to maximum", defaultLimit: 200, maxLimit: 100, wantLimit: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limit, offset := NormalizePage(test.limit, test.offset, test.defaultLimit, test.maxLimit)
			if limit != test.wantLimit || offset != test.wantOffset {
				t.Fatalf("NormalizePage() = (%d, %d), want (%d, %d)", limit, offset, test.wantLimit, test.wantOffset)
			}
		})
	}
}
