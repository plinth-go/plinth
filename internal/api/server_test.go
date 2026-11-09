package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/platform/jobs"
)

func testRouter(t *testing.T) *kernel.Router {
	t.Helper()
	return Routes()
}

func TestOpenAPIUsesGeneratedCommentDocs(t *testing.T) {
	router := testRouter(t)
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(router.OpenAPIJSON(), &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	if _, exists := paths["/docs/"]; exists {
		t.Fatal("hidden route appears in OpenAPI")
	}

	getJob := paths["/v1/jobs/{id}"].(map[string]any)["get"].(map[string]any)
	if summary := getJob["summary"].(string); !strings.Contains(summary, "durable lifecycle state") {
		t.Fatalf("summary was not extracted from comments: %q", summary)
	}
	response := getJob["responses"].(map[string]any)["200"].(map[string]any)
	content := response["content"].(map[string]any)["application/json"].(map[string]any)
	schema := content["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	status := properties["status"].(map[string]any)
	if description := status["description"].(string); !strings.Contains(description, "pending") {
		t.Fatalf("field comment missing from schema: %q", description)
	}

	events := paths["/demo/events"].(map[string]any)["get"].(map[string]any)
	eventResponses := events["responses"].(map[string]any)
	if _, ok := eventResponses["200"].(map[string]any)["content"].(map[string]any)["text/event-stream"]; !ok {
		t.Fatal("SSE media type missing from OpenAPI")
	}
}

func TestResponseHeadersOutput(t *testing.T) {
	router := testRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/demo/response-headers", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Plinth-Response"); got != "typed-header" {
		t.Fatalf("X-Plinth-Response = %q", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("request ID header missing")
	}
	var body ResponseHeadersOutput
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID == "" {
		t.Fatal("request ID body value missing")
	}
}

func TestOperationalRoutes(t *testing.T) {
	router := testRouter(t)

	docs := httptest.NewRecorder()
	router.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if docs.Code != http.StatusOK || !strings.Contains(docs.Body.String(), "swagger") {
		t.Fatalf("docs status = %d, body prefix = %.80s", docs.Code, docs.Body.String())
	}

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/not-found", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", missing.Code)
	}

	root := httptest.NewRecorder()
	router.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusTemporaryRedirect || root.Header().Get("Location") != "/web/" {
		t.Fatalf("root redirect = %d %q", root.Code, root.Header().Get("Location"))
	}
}

func TestEnqueueJobRejectsPayloadThatDoesNotMatchHandler(t *testing.T) {
	type payload struct {
		Message string `json:"message"`
	}
	registry := jobs.NewRegistry()
	if err := jobs.Register(registry, "test.echo", func(context.Context, payload) error { return nil }); err != nil {
		t.Fatal(err)
	}
	application := &API{deps: Dependencies{Registry: registry}}
	_, err := application.enqueueJob(context.Background(), EnqueueJobInput{Body: EnqueueJobBody{
		Kind:    "test.echo",
		Payload: json.RawMessage(`{"message":"hello","extra":true}`),
	}})
	var httpErr *kernel.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("error = %v", err)
	}
}
