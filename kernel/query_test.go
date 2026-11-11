package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestScanQueryMatchesURLParser(t *testing.T) {
	index := map[string]int{"q": 0, "limit": 1, "tag": 2}

	for _, raw := range []string{
		"",
		"q=alpha",
		"q=",
		"q",
		"=x",
		"&",
		"q=1&limit=2",
		"q=1&q=2&limit=3",
		"notdeclared=1&q=x",
		"q=a=b",
		"q=1&",
		"&&q=1&&",
		"q=alpha+beta",
		"q=alpha%20beta",
		"q=alpha%2Bbeta",
		"%71=encoded-key",
		"q=%E4%B8%AD",
		"q=1%262",
		"q=1&unknown=a+b",
		"q=1&q=2",
		"q=1&&q=2",
		"q=1&=x",
		"q=%zz",
		"q=a;b",
		"q=1&unknown=%zz",
	} {
		t.Run(raw, func(t *testing.T) {
			values := make([]queryValue, len(index))
			err := scanQuery(raw, index, values)
			want, wantErr := url.ParseQuery(raw)
			if (err == nil) != (wantErr == nil) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			if err != nil {
				if err.Error() != wantErr.Error() {
					t.Fatalf("error = %q, want %q", err, wantErr)
				}
				return
			}
			for name, slot := range index {
				got := queryValueStrings(values[slot])
				if !reflect.DeepEqual(got, want[name]) {
					t.Fatalf("%s = %#v, want %#v", name, got, want[name])
				}
			}
		})
	}
}

func queryValueStrings(value queryValue) []string {
	switch value.count {
	case 0:
		return nil
	case 1:
		return []string{value.first}
	default:
		return value.all
	}
}

type fastQueryInput struct {
	Meta  `route:"GET /query"`
	Q     string   `query:"q"`
	Limit int      `query:"limit" default:"10"`
	Tags  []string `query:"tag"`
}

type fastQueryOutput struct {
	Q     string   `json:"q"`
	Limit int      `json:"limit"`
	Tags  []string `json:"tags"`
}

func TestQueryFastPathAndFallbacks(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/query", func(_ context.Context, in fastQueryInput) (fastQueryOutput, error) {
		return fastQueryOutput{Q: in.Q, Limit: in.Limit, Tags: in.Tags}, nil
	})

	for _, tc := range []struct {
		name       string
		url        string
		wantStatus int
		want       fastQueryOutput
	}{
		{name: "simple", url: "/query?q=alpha&limit=2", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha", Limit: 2}},
		{name: "empty value", url: "/query?q", wantStatus: http.StatusOK, want: fastQueryOutput{Limit: 10}},
		{name: "default", url: "/query?q=alpha", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha", Limit: 10}},
		{name: "explicit zero", url: "/query?limit=0", wantStatus: http.StatusOK, want: fastQueryOutput{Limit: 0}},
		{name: "repeated slice", url: "/query?tag=a&tag=b", wantStatus: http.StatusOK, want: fastQueryOutput{Limit: 10, Tags: []string{"a", "b"}}},
		{name: "single slice", url: "/query?tag=a", wantStatus: http.StatusOK, want: fastQueryOutput{Limit: 10, Tags: []string{"a"}}},
		{name: "plus", url: "/query?q=alpha+beta", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha beta", Limit: 10}},
		{name: "escaped", url: "/query?q=alpha%2Bbeta", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha+beta", Limit: 10}},
		{name: "escaped key", url: "/query?%71=alpha", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha", Limit: 10}},
		{name: "unknown escaped value", url: "/query?q=alpha&unknown=a+b", wantStatus: http.StatusOK, want: fastQueryOutput{Q: "alpha", Limit: 10}},
		{name: "repeated scalar", url: "/query?q=a&q=b", wantStatus: http.StatusUnprocessableEntity},
		{name: "semicolon", url: "/query?q=a;b", wantStatus: http.StatusUnprocessableEntity},
		{name: "malformed value", url: "/query?q=valid&limit=%zz", wantStatus: http.StatusUnprocessableEntity},
		{name: "malformed unknown", url: "/query?q=valid&unknown=%zz", wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var got fastQueryOutput
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("output = %#v, want %#v", got, tc.want)
			}
		})
	}
}
