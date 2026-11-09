package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
)

func testJSON[In, Out any](r *Router, method, pattern string, h Handler[In, Out], opts ...Option) {
	r.json("", nil, method, pattern, h, opts...)
}

func testGroupJSON[In, Out any](g *Group, method, pattern string, h Handler[In, Out], opts ...Option) {
	g.router.json(g.prefix, g.layers(), method, pattern, h, g.options(opts)...)
}

type testBody struct {
	Title string `json:"title" validate:"required,min=3,max=80"`
	Slug  string `json:"slug" slug:"title,max=60" validate:"omitempty,max=60"`
	Count int    `json:"count" validate:"required,min=1"`
}

type typedNilHandler struct{}

func (*typedNilHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

type failingJSONResponse struct{}

func (failingJSONResponse) MarshalJSON() ([]byte, error) {
	return nil, errors.New("response encoding failed")
}

type testInput struct {
	Body testBody `body:",required"`
}

type testOutput struct {
	Meta    `status:"201"`
	Headers Headers `json:"-"`
	Slug    string  `json:"slug"`
	Count   int     `json:"count"`
}

type plainValidatedInput struct {
	Value string `query:"value"`
}

func (in plainValidatedInput) Validate() error {
	if in.Value == "" {
		return Unprocessable("plain validation failed")
	}
	return nil
}

type contextValidatedInput struct {
	Value string `query:"value"`
}

func (in contextValidatedInput) Validate(ctx context.Context) error {
	if in.Value != "ok" || ctx.Value(plainValidatedInput{}) == nil {
		return Unprocessable("context validation failed")
	}
	return nil
}

type plainFailedInput struct {
	Value string `query:"value"`
}

func (plainFailedInput) Validate() error { return errors.New("plain validator failure") }

type conflictValidatedInput struct {
	Value string `query:"value"`
}

func (conflictValidatedInput) Validate() error { return Conflict("already exists") }

type fieldValidatedInput struct {
	Value string `query:"value"`
}

func (fieldValidatedInput) Validate() error {
	return &ValidationError{Errors: []FieldError{{Location: "query", Field: "value", Message: "is invalid"}}}
}

func TestInputValidatorsRunWithoutValidateTags(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	testJSON(router, "GET", "/plain", func(context.Context, plainValidatedInput) (struct{}, error) {
		return struct{}{}, nil
	})
	testJSON(router, "GET", "/context", func(context.Context, contextValidatedInput) (struct{}, error) {
		return struct{}{}, nil
	})

	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/plain?value=ok", nil))
	if plain.Code != http.StatusNoContent {
		t.Fatalf("plain status = %d, body = %s", plain.Code, plain.Body.String())
	}

	contextRoute := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/context?value=ok", nil)
	request = request.WithContext(context.WithValue(request.Context(), plainValidatedInput{}, plainValidatedInput{Value: "set"}))
	router.ServeHTTP(contextRoute, request)
	if contextRoute.Code != http.StatusNoContent {
		t.Fatalf("context status = %d, body = %s", contextRoute.Code, contextRoute.Body.String())
	}
}

func TestCustomValidatorErrorsKeepHTTPAndValidationSemantics(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	testJSON(router, "GET", "/plain-error", func(context.Context, plainFailedInput) (struct{}, error) {
		return struct{}{}, nil
	})
	testJSON(router, "GET", "/conflict", func(context.Context, conflictValidatedInput) (struct{}, error) {
		return struct{}{}, nil
	})
	testJSON(router, "GET", "/field-error", func(context.Context, fieldValidatedInput) (struct{}, error) {
		return struct{}{}, nil
	})
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path        string
		status      int
		detail      string
		fieldErrors int
	}{
		{path: "/plain-error?value=x", status: http.StatusUnprocessableEntity, detail: "plain validator failure"},
		{path: "/conflict?value=x", status: http.StatusConflict, detail: "already exists"},
		{path: "/field-error?value=x", status: http.StatusUnprocessableEntity, detail: "request validation failed", fieldErrors: 1},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s status = %d, body = %s", tc.path, rec.Code, rec.Body.String())
		}
		var problem Problem
		if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
			t.Fatal(err)
		}
		if problem.Detail != tc.detail || len(problem.Errors) != tc.fieldErrors {
			t.Fatalf("%s problem = %#v", tc.path, problem)
		}
	}
}

func (o testOutput) ResponseHeaders() http.Header { return http.Header(o.Headers) }

func testHandler(_ context.Context, in testInput) (testOutput, error) {
	return testOutput{
		Headers: Headers{"X-Test-Output": {"created"}},
		Slug:    in.Body.Slug,
		Count:   in.Body.Count,
	}, nil
}

func TestJSONBindingSlugValidationAndHeaders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(logger, "test", "1")
	router.SetDocs(DocSet{
		Operations: map[string]OperationDoc{
			handlerName(testHandler): {
				Summary:     "Create a slug-backed record",
				Description: "The slug is generated before validation.",
			},
		},
		Fields: map[string]string{
			fieldDocKey(reflect.TypeFor[testBody](), "Title"): "Human-readable title.",
		},
	})
	router.Use(RequestID())
	testJSON(router, "POST", "/items", testHandler, Tags("items"))

	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"A Fancy Title!","count":2}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Meta") {
		t.Fatalf("meta leaked into response: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Test-Output"); got != "created" {
		t.Fatalf("X-Test-Output = %q", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("X-Request-ID is empty")
	}

	var body testOutput
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Slug != "a-fancy-title" {
		t.Fatalf("slug = %q, want a-fancy-title", body.Slug)
	}

	spec := decodeSpec(t, router.OpenAPIJSON())
	operation := spec.Paths["/items"]["post"]
	if operation.Summary != "Create a slug-backed record" {
		t.Fatalf("summary = %q", operation.Summary)
	}
	if operation.Description != "The slug is generated before validation." {
		t.Fatalf("description = %q", operation.Description)
	}
	if operation.RequestBody == nil {
		t.Fatal("request body missing from OpenAPI")
	}
	if _, ok := operation.Responses["201"]; !ok {
		t.Fatalf("response status = %#v", operation.Responses)
	}
	responseProperties := operation.Responses["201"].Content["application/json"].Schema["properties"].(map[string]any)
	if _, ok := responseProperties["Meta"]; ok {
		t.Fatalf("meta leaked into response schema: %#v", responseProperties)
	}
	bodySchema := operation.RequestBody.Content["application/json"].Schema
	properties := bodySchema["properties"].(map[string]any)
	if desc := properties["title"].(map[string]any)["description"]; desc != "Human-readable title." {
		t.Fatalf("title description = %v", desc)
	}
	slugProperty := properties["slug"].(map[string]any)
	if source := slugProperty["x-plinth-slug-source"]; source != "title" {
		t.Fatalf("slug source extension = %v", source)
	}
	if desc := slugProperty["description"].(string); !strings.Contains(desc, "Generated from title") {
		t.Fatalf("slug description = %q", desc)
	}
}

func TestTypedJSONEncodeFailureWritesCleanProblem(t *testing.T) {
	type input struct {
		Meta `route:"GET /encode-failure"`
	}
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	router.Handle(func(context.Context, input) (failingJSONResponse, error) {
		return failingJSONResponse{}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/encode-failure", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if strings.Contains(rec.Body.String(), `"id"`) || !strings.Contains(rec.Body.String(), `"status":500`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type groupUser struct{ ID string }
type groupScope struct{ Name string }
type groupOutput struct {
	User  string `json:"user"`
	Scope string `json:"scope"`
}

var (
	testUserKey  = NewContextKey[groupUser]("test.user")
	testScopeKey = NewContextKey[groupScope]("test.scope")
)

func TestNestedGroupsApplyMiddlewareAndContext(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	var order []string

	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "root-in")
			next.ServeHTTP(w, r)
			order = append(order, "root-out")
		})
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "auth-in")
			if r.Header.Get("Authorization") != "Bearer ok" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := testUserKey.With(r.Context(), groupUser{ID: "u1"})
			next.ServeHTTP(w, r.WithContext(ctx))
			order = append(order, "auth-out")
		})
	}
	scope := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "scope-in")
			ctx := testScopeKey.With(r.Context(), groupScope{Name: "books"})
			next.ServeHTTP(w, r.WithContext(ctx))
			order = append(order, "scope-out")
		})
	}

	v1 := router.Group("/v1", auth).Tag("v1")
	books := v1.Group("/books", scope).Tag("books")
	if books.Prefix() != "/v1/books" {
		t.Fatalf("prefix = %q", books.Prefix())
	}
	testGroupJSON(books, "POST", "", func(ctx context.Context, _ struct{}) (groupOutput, error) {
		order = append(order, "handler")
		user, _ := testUserKey.Get(ctx)
		ctxScope, _ := testScopeKey.Get(ctx)
		return groupOutput{User: user.ID, Scope: ctxScope.Name}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/books", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	wantOrder := []string{"root-in", "auth-in", "scope-in", "handler", "scope-out", "auth-out", "root-out"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("order = %#v", order)
	}
	var body groupOutput
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.User != "u1" || body.Scope != "books" {
		t.Fatalf("context output = %#v", body)
	}
	operation, ok := decodeSpec(t, router.OpenAPIJSON()).Paths["/v1/books"]["post"]
	if !ok {
		t.Fatal("grouped route missing from OpenAPI")
	}
	if strings.Join(operation.Tags, ",") != "v1,books" {
		t.Fatalf("tags = %#v", operation.Tags)
	}
}

func TestRegistrationOrderIsEnforced(t *testing.T) {
	noop := func(next http.Handler) http.Handler { return next }
	raw := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	t.Run("router middleware after route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "Router.Use", func() {
			router.Use(noop)
		})
	})

	t.Run("group middleware after route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		group := router.Group("/v1")
		group.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "Group.Use", func() {
			group.Use(noop)
		})
	})

	t.Run("parent middleware after child route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		parent := router.Group("/v1")
		child := parent.Group("/items")
		child.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "Group.Use", func() {
			parent.Use(noop)
		})
	})

	t.Run("group tag after route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		group := router.Group("/v1")
		group.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "Group.Tag", func() {
			group.Tag("late")
		})
	})

	t.Run("parent tag after child route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		parent := router.Group("/v1")
		child := parent.Group("/items")
		child.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "Group.Tag", func() {
			parent.Tag("late")
		})
	})

	t.Run("docs after route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Raw("GET", "/raw", raw, Hidden())
		assertPanicsWith(t, "SetDocs", func() {
			router.SetDocs(DocSet{})
		})
	})
}

func TestRegistrationRejectsNilMiddlewareAndHandlers(t *testing.T) {
	noop := func(next http.Handler) http.Handler { return next }
	raw := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	var typedNil *typedNilHandler

	t.Run("router middleware", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "Router.Use received nil middleware at index 0", func() {
			router.Use(nil)
		})
	})
	t.Run("group middleware", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "Group.Use received nil middleware at index 1", func() {
			router.Group("/v1").Use(noop, nil)
		})
	})
	t.Run("group constructor middleware", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "Router.Group received nil middleware at index 0", func() {
			router.Group("/v1", nil)
		})
	})
	t.Run("raw handler", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "raw handler is nil", func() {
			router.Raw("GET", "/raw", nil, Hidden())
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed raw registration left an operation behind")
		}
	})
	t.Run("typed nil raw handler", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "raw handler is nil", func() {
			router.Raw("GET", "/raw", typedNil, Hidden())
		})
	})
	t.Run("typed handler", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "typed handler is nil", func() {
			router.Handle[struct{}, struct{}](nil)
		})
	})
	t.Run("middleware returns nil", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Use(func(http.Handler) http.Handler { return nil })
		assertPanicsWith(t, "middleware returned nil handler", func() {
			router.Raw("GET", "/raw", raw, Hidden())
		})
	})
	t.Run("middleware returns typed nil", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Use(func(http.Handler) http.Handler { return typedNil })
		assertPanicsWith(t, "middleware returned nil handler", func() {
			router.Raw("GET", "/raw", raw, Hidden())
		})
	})
	t.Run("raw option", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "option at index 1 is nil", func() {
			router.Raw("GET", "/raw", raw, Hidden(), nil)
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed raw registration left an operation behind")
		}
	})
	t.Run("typed option", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "option at index 0 is nil", func() {
			router.Handle(func(context.Context, routeTagInput) (routeTagOutput, error) {
				return routeTagOutput{}, nil
			}, nil)
		})
		if len(router.operationSnapshot()) != 0 {
			t.Fatal("failed typed registration left an operation behind")
		}
	})
}

func TestFailedRegistrationDoesNotFreezeRouterOrGroup(t *testing.T) {
	raw := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	noop := func(next http.Handler) http.Handler { return next }

	t.Run("router malformed pattern", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		assertPanicsWith(t, "bad wildcard segment", func() {
			router.Raw("GET", "/items/{", raw, Hidden())
		})
		if router.frozen {
			t.Fatal("failed raw registration froze the router")
		}
		router.Use(noop)
		router.Raw("GET", "/ok", raw, Hidden())
	})

	t.Run("group route conflict", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		router.Raw("GET", "/raw", raw, Hidden())
		group := router.Group("")
		assertPanicsWith(t, "conflicts", func() {
			group.Raw("GET", "/raw", raw, Hidden())
		})
		group.Tag("late")
	})

	t.Run("group typed malformed pattern", func(t *testing.T) {
		type malformedInput struct {
			Meta `route:"GET /items/{ "`
		}
		router := NewRouter(nil, "test", "1")
		group := router.Group("/v1")
		assertPanicsWith(t, "bad wildcard segment", func() {
			group.Handle(func(context.Context, malformedInput) (struct{}, error) {
				return struct{}{}, nil
			})
		})
		group.Tag("late")
	})

	t.Run("group nil option", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		group := router.Group("/v1")
		assertPanicsWith(t, "option at index 0 is nil", func() {
			group.Raw("GET", "/raw", raw, nil)
		})
		group.Tag("late")
	})
}

func TestRegistrationRejectsNonCanonicalMethods(t *testing.T) {
	raw := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	router := NewRouter(nil, "test", "1")
	for _, method := range []string{"get", "Get", "GE T", ""} {
		assertPanicsWith(t, "uppercase HTTP token", func() {
			router.Raw(method, "/raw", raw, Hidden())
		})
	}

	type lowerMethodInput struct {
		Meta `route:"get /lower"`
	}
	assertPanicsWith(t, "uppercase HTTP token", func() {
		router.Handle(func(context.Context, lowerMethodInput) (struct{}, error) {
			return struct{}{}, nil
		})
	})
}

type emptyRouteInput struct {
	Meta `route:"POST /items"`
}

type statusOnInput struct {
	Meta `route:"POST /items" status:"201"`
}

type producesOnInput struct {
	Meta `route:"POST /items" produces:"text/csv"`
}

type routeOnOutput struct {
	Meta `route:"POST /wrong"`
}

func TestRegistrationRejectsMetaTagsInTheWrongDirection(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	for _, tc := range []struct {
		name string
		want string
		call func()
	}{
		{
			name: "status on input",
			want: "status tag on input",
			call: func() {
				router.Handle(func(context.Context, statusOnInput) (struct{}, error) { return struct{}{}, nil })
			},
		},
		{
			name: "produces on input",
			want: "produces tag on input",
			call: func() {
				router.Handle(func(context.Context, producesOnInput) (struct{}, error) { return struct{}{}, nil })
			},
		},
		{
			name: "route on output",
			want: "route tag on output",
			call: func() {
				router.Handle(func(context.Context, emptyRouteInput) (routeOnOutput, error) {
					return routeOnOutput{}, nil
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertPanicsWith(t, tc.want, tc.call)
		})
	}
}

func TestContextKeysAreExplicit(t *testing.T) {
	first := NewContextKey[string]("first")
	second := NewContextKey[string]("second")
	ctx := first.With(context.Background(), "value")

	if value, ok := first.Get(ctx); !ok || value != "value" {
		t.Fatalf("first key = %q, %v", value, ok)
	}
	if _, ok := second.Get(ctx); ok {
		t.Fatal("different key with the same value type must not match")
	}
	if first.Name() != "first" {
		t.Fatalf("name = %q", first.Name())
	}
}

func TestGroupMiddlewareCanShortCircuit(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	called := false
	v1 := router.Group("/v1", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	})
	testGroupJSON(v1, "GET", "/book", func(context.Context, struct{}) (groupOutput, error) {
		called = true
		return groupOutput{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/book", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if called {
		t.Fatal("handler ran after middleware short-circuit")
	}
}

type routeTagInput struct {
	Meta `route:"POST /items/{id}"`
	ID   string `path:"id"`
}

func TestOperationMetadataReachesMiddleware(t *testing.T) {
	var operation OperationMeta
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			operation, _ = OperationFromContext(r.Context())
			next.ServeHTTP(w, r)
		})
	})
	router.Handle(func(context.Context, authzTagInput) (struct{}, error) { return struct{}{}, nil })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/authz", nil))
	if operation.OperationID != "get--authz" || operation.Method != http.MethodGet || operation.Path != "/authz" {
		t.Fatalf("operation = %#v", operation)
	}
	if len(operation.RequiredPermissions) != 2 || operation.RequiredPermissions[0] != "links:read" || operation.RequiredPermissions[1] != "links:write" {
		t.Fatalf("permissions = %#v", operation.RequiredPermissions)
	}
}

type authzTagInput struct {
	Meta `route:"GET /authz" authz:"links:read, links:write,links:read"`
}

func TestAuthzTagValidation(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	router.Handle(func(context.Context, authzTagInput) (struct{}, error) { return struct{}{}, nil })
	spec := decodeSpec(t, router.OpenAPIJSON())
	operation := spec.Paths["/authz"]["get"]
	if len(operation.RequiredPermissions) != 2 || operation.RequiredPermissions[0] != "links:read" || operation.RequiredPermissions[1] != "links:write" {
		t.Fatalf("required permissions = %#v", operation.RequiredPermissions)
	}
}

type invalidAuthzInput struct {
	Meta `route:"GET /invalid" authz:"links read"`
}

func TestAuthzTagRejectsInvalidPermissions(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "invalid permission") {
			t.Fatalf("panic = %v", recovered)
		}
	}()
	router.Handle(func(context.Context, invalidAuthzInput) (struct{}, error) { return struct{}{}, nil })
}

type outputAuthzTag struct {
	Meta `authz:"links:read"`
}

func TestAuthzTagOnOutputIsRejected(t *testing.T) {
	router := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "1")
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "authz tag on output") {
			t.Fatalf("panic = %v", recovered)
		}
	}()
	router.Handle(func(context.Context, authzTagInput) (outputAuthzTag, error) { return outputAuthzTag{}, nil })
}

type routeTagOutput struct {
	ID string `json:"id"`
}

func TestHandleReadsMethodAndPathFromInput(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	v1 := router.Group("/v1")
	v1.Handle(func(_ context.Context, in routeTagInput) (routeTagOutput, error) {
		return routeTagOutput{ID: in.ID}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/items/42", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"42"`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, ok := decodeSpec(t, router.OpenAPIJSON()).Paths["/v1/items/{id}"]; !ok {
		t.Fatal("handled route missing from OpenAPI")
	}
}

type textOutput struct {
	Meta `produces:"text/plain"`
}

func (textOutput) Respond(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "text/plain")
	_, err := w.Write([]byte("plain text"))
	return err
}

func TestMethodShortcutAllowsNonJSONResponse(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/plain", func(context.Context, struct{}) (textOutput, error) {
		return textOutput{}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/plain", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "plain text" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/plain" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestServeMuxHandlesHEADAndMethodNotAllowed(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/items", func(context.Context, struct{}) (groupOutput, error) {
		return groupOutput{User: "u1", Scope: "read"}, nil
	})

	server := httptest.NewServer(router)
	defer server.Close()

	head, err := http.NewRequest(http.MethodHead, server.URL+"/items", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(head)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD status = %d, body = %q", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("HEAD Content-Type = %q", got)
	}

	options, err := http.NewRequest(http.MethodOptions, server.URL+"/items", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = server.Client().Do(options)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("OPTIONS status = %d", response.StatusCode)
	}
	if allow := response.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodHead) {
		t.Fatalf("Allow = %q", allow)
	}
}

func TestCSVOutputUsesBuiltInResponseMetadata(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/export", func(context.Context, struct{}) (CSV, error) {
		return CSV{
			Filename: "items.csv",
			Header:   []string{"id", "name"},
			Rows: func(yield func([]string) error) error {
				return yield([]string{"1", "alpha"})
			},
		}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/csv") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "items.csv") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if rec.Body.String() != "id,name\n1,alpha\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/export"]["get"]
	if _, ok := operation.Responses["200"].Content["text/csv"]; !ok {
		t.Fatalf("OpenAPI response = %#v", operation.Responses["200"])
	}
}

type attachmentOutput struct {
	Meta `produces:"application/pdf"`
	Attachment
}

func TestAttachmentOutputAndProducesTag(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/manual", func(context.Context, struct{}) (attachmentOutput, error) {
		return attachmentOutput{Attachment: Attachment{
			Filename:    "manual.pdf",
			ContentType: "application/pdf",
			Write: func(w io.Writer) error {
				_, err := w.Write([]byte("pdf"))
				return err
			},
		}}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manual", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "pdf" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/pdf" {
		t.Fatalf("Content-Type = %q", contentType)
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/manual"]["get"]
	if _, ok := operation.Responses["200"].Content["application/pdf"]; !ok {
		t.Fatalf("OpenAPI response = %#v", operation.Responses["200"])
	}
}

func TestAttachmentFailureBeforeFirstWriteBecomesProblem(t *testing.T) {
	router := NewRouter(discardLogger(), "test", "1")
	router.Use(RequestID())
	testJSON(router, "GET", "/manual", func(context.Context, struct{}) (attachmentOutput, error) {
		return attachmentOutput{Attachment: Attachment{
			Filename:    "manual.pdf",
			ContentType: "application/pdf",
			Write: func(io.Writer) error {
				return errors.New("source unavailable")
			},
		}}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manual", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition leaked: %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("base response header X-Request-ID was lost")
	}
}

func TestJSONValidationReportsJSONFieldNames(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"","count":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, field := range problem.Errors {
		fields[field.Field] = true
	}
	if !fields["title"] || !fields["count"] {
		t.Fatalf("validation fields = %#v", problem.Errors)
	}
	if fields["Title"] || fields["Count"] {
		t.Fatalf("errors leaked Go field names: %#v", problem.Errors)
	}
}

func TestJSONRejectsUnknownTrailingJSON(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"valid","count":1} {}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestJSONRejectsUnknownProperties(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"valid","count":1,"extra":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown property") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestJSONRequestKeepsLegacyDecodeSemantics(t *testing.T) {
	type body struct {
		Wait  time.Duration `json:"wait"`
		Title string        `json:"title"`
	}
	type input struct {
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", func(_ context.Context, in input) (map[string]string, error) {
		return map[string]string{"wait": in.Body.Wait.String(), "title": in.Body.Title}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"wait":1500000000,"Title":"case-insensitive"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["title"] != "case-insensitive" || response["wait"] != "1.5s" {
		t.Fatalf("response = %#v", response)
	}
}

func TestJSONRejectsDuplicateObjectProperties(t *testing.T) {
	type body struct {
		Title    string              `json:"title" validate:"required"`
		Metadata map[string]string   `json:"metadata"`
		Items    []map[string]string `json:"items"`
	}
	type input struct {
		Body body `body:",required"`
	}

	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", func(_ context.Context, _ input) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "top level", body: `{"title":"first","title":"second"}`},
		{name: "nested object", body: `{"title":"valid","metadata":{"tag":"a","tag":"b"}}`},
		{name: "nested array object", body: `{"title":"valid","items":[{"tag":"a","tag":"b"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "duplicate object properties") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestJSONBodyMustNotBeNull(t *testing.T) {
	type body struct {
		Limit int `json:"limit"`
	}
	type requiredStructInput struct {
		Body body `body:",required"`
	}
	type optionalStructInput struct {
		Body body `body:""`
	}
	type requiredPointerInput struct {
		Body *body `body:",required"`
	}
	type optionalPointerInput struct {
		Body *body `body:""`
	}

	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/required-struct", func(_ context.Context, _ requiredStructInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})
	testJSON(router, "POST", "/optional-struct", func(_ context.Context, _ optionalStructInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})
	testJSON(router, "POST", "/required-pointer", func(_ context.Context, _ requiredPointerInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})
	testJSON(router, "POST", "/optional-pointer", func(_ context.Context, _ optionalPointerInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})

	for _, path := range []string{"/required-struct", "/optional-struct", "/required-pointer", "/optional-pointer"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`null`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "must not be null") {
			t.Fatalf("%s body = %s", path, rec.Body.String())
		}
	}
}

func TestJSONBodyRejectsNestedNull(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type body struct {
		Name    string            `json:"name"`
		Items   []string          `json:"items"`
		Labels  map[string]string `json:"labels"`
		Nested  nested            `json:"nested"`
		Pointer *string           `json:"pointer"`
	}
	type input struct {
		Meta `route:"POST /nulls"`
		Body body `body:",required"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (map[string]string, error) {
		return map[string]string{"status": "ok"}, nil
	})

	for _, payload := range []string{
		`{"name":null}`,
		`{"items":[null]}`,
		`{"labels":{"key":null}}`,
		`{"nested":{"value":null}}`,
		`{"pointer":null}`,
	} {
		t.Run(payload, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/nulls", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "must not be null") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/nulls", strings.NewReader(`{"name":"null"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("literal string status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInternalErrorDoesNotLeakDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/failure", nil)
	WriteProblem(rec, req, Internal(errString("database password rejected")), nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("internal detail leaked: %s", rec.Body.String())
	}
}

type queryInput struct {
	Email string `query:"email" validate:"required,email"`
}

func queryHandler(_ context.Context, in queryInput) (map[string]string, error) {
	return map[string]string{"email": in.Email}, nil
}

func TestQueryValidationUsesQueryLocation(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/search", queryHandler)

	req := httptest.NewRequest(http.MethodGet, "/search?email=not-an-email", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if len(problem.Errors) != 1 {
		t.Fatalf("errors = %#v", problem.Errors)
	}
	if problem.Errors[0].Location != "query" || problem.Errors[0].Field != "email" {
		t.Fatalf("error = %#v", problem.Errors[0])
	}
}

func TestRequiredQueryValidation(t *testing.T) {
	type input struct {
		Query string `query:"q" validate:"required,min=2"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/search", func(_ context.Context, in input) (map[string]string, error) {
		return map[string]string{"q": in.Query}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing status = %d", rec.Code)
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "query" || problem.Errors[0].Message != "is required" {
		t.Fatalf("missing errors = %#v", problem.Errors)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=a", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short status = %d", rec.Code)
	}
	problem = Problem{}
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "query" {
		t.Fatalf("short errors = %#v", problem.Errors)
	}

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/search"]["get"]
	parameter := operation.Parameters[0]
	if !parameter.Required {
		t.Fatalf("OpenAPI parameter = %#v", parameter)
	}
	if minimum := parameter.Schema["minLength"]; minimum != float64(2) {
		t.Fatalf("OpenAPI minLength = %#v", minimum)
	}
}

func TestQueryDefaultsToOptional(t *testing.T) {
	type input struct {
		Query string `query:"q" validate:"omitempty,min=2"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/search", func(_ context.Context, in input) (map[string]string, error) {
		return map[string]string{"q": in.Query}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/search"]["get"]
	if operation.Parameters[0].Required {
		t.Fatalf("OpenAPI parameter = %#v", operation.Parameters[0])
	}
}

func TestMalformedQueryStringIsRejected(t *testing.T) {
	type input struct {
		Query string `query:"q"`
		Limit int    `query:"limit" default:"10"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/search", func(_ context.Context, in input) (map[string]any, error) {
		return map[string]any{"q": in.Query, "limit": in.Limit}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=valid&limit=%zz", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var problem Problem
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "query" || problem.Errors[0].Field != "query" {
		t.Fatalf("errors = %#v", problem.Errors)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?q=valid&limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestValidatorRequiredAlsoMarksQueryRequired(t *testing.T) {
	type input struct {
		Query string `query:"q" validate:"required,min=2"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/search", func(_ context.Context, in input) (map[string]string, error) {
		return map[string]string{"q": in.Query}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/search"]["get"]
	if !operation.Parameters[0].Required {
		t.Fatalf("OpenAPI parameter = %#v", operation.Parameters[0])
	}
}

func TestPointerBodyDistinguishesMissingAndZero(t *testing.T) {
	type body struct {
		Limit *int `json:"limit" validate:"omitempty,min=1"`
	}
	type input struct {
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", func(_ context.Context, _ input) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{name: "missing", body: `{}`, want: http.StatusOK},
		{name: "zero", body: `{"limit":0}`, want: http.StatusUnprocessableEntity},
		{name: "valid", body: `{"limit":2}`, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestJSONRequestBodyContentTypeContract(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	for _, tc := range []struct {
		name        string
		contentType string
		want        int
	}{
		{name: "missing", want: http.StatusUnsupportedMediaType},
		{name: "plain text", contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "other json suffix", contentType: "application/problem+json", want: http.StatusUnsupportedMediaType},
		{name: "json", contentType: "application/json", want: http.StatusUnprocessableEntity},
		{name: "json with charset", contentType: "application/json; charset=utf-8", want: http.StatusUnprocessableEntity},
		{name: "json with uppercase charset", contentType: "application/json; charset=UTF-8", want: http.StatusUnprocessableEntity},
		{name: "json with utf8 alias", contentType: "application/json; charset=utf8", want: http.StatusUnsupportedMediaType},
		{name: "json with unsupported charset", contentType: "application/json; charset=iso-8859-1", want: http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"","count":0}`))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.want == http.StatusUnsupportedMediaType {
				var problem Problem
				if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
					t.Fatal(err)
				}
				if problem.Status != http.StatusUnsupportedMediaType || !strings.Contains(problem.Detail, "application/json") {
					t.Fatalf("problem = %#v", problem)
				}
			}
		})
	}
}

func TestJSONRequestBodyRejectsAmbiguousContentHeaders(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	for _, tc := range []struct {
		name             string
		contentTypes     []string
		contentEncodings []string
		want             int
		detail           string
	}{
		{name: "identity", contentTypes: []string{"application/json"}, contentEncodings: []string{"identity"}, want: http.StatusCreated},
		{name: "gzip", contentTypes: []string{"application/json"}, contentEncodings: []string{"gzip"}, want: http.StatusUnsupportedMediaType, detail: "Content-Encoding must be identity"},
		{name: "empty encoding", contentTypes: []string{"application/json"}, contentEncodings: []string{""}, want: http.StatusUnsupportedMediaType, detail: "Content-Encoding must be identity"},
		{name: "repeated encoding", contentTypes: []string{"application/json"}, contentEncodings: []string{"identity", "identity"}, want: http.StatusUnsupportedMediaType, detail: "Content-Encoding must be identity"},
		{name: "repeated content type", contentTypes: []string{"application/json", "application/json"}, want: http.StatusUnsupportedMediaType, detail: "Content-Type must be application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"valid","count":1}`))
			for _, contentType := range tc.contentTypes {
				req.Header.Add("Content-Type", contentType)
			}
			for _, encoding := range tc.contentEncodings {
				req.Header.Add("Content-Encoding", encoding)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.detail != "" && !strings.Contains(rec.Body.String(), tc.detail) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

type nilBodyReadCloser struct{}

func (*nilBodyReadCloser) Read([]byte) (int, error) {
	panic("nil body reader must not be called")
}

func (*nilBodyReadCloser) Close() error { return nil }

func TestJSONBodyNilReaderIsRejectedWithoutPanic(t *testing.T) {
	type requiredInput struct {
		Body struct{} `body:",required"`
	}
	type optionalInput struct {
		Body struct{} `body:""`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/required", func(context.Context, requiredInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})
	testJSON(router, "POST", "/optional", func(context.Context, optionalInput) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})

	for _, tc := range []struct {
		name   string
		path   string
		body   io.ReadCloser
		length int64
		want   int
	}{
		{name: "required nil reader", path: "/required", want: http.StatusUnprocessableEntity},
		{name: "optional nil reader", path: "/optional", want: http.StatusOK},
		{name: "required typed nil reader", path: "/required", body: (*nilBodyReadCloser)(nil), length: 1, want: http.StatusUnprocessableEntity},
		{name: "optional typed nil reader", path: "/optional", body: (*nilBodyReadCloser)(nil), length: 1, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Body = tc.body
			req.ContentLength = tc.length
			if tc.length != 0 {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestOpenAPIDeclaresUnsupportedMediaTypeForBodyEndpoints(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "POST", "/items", testHandler)

	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/items"]["post"]
	if _, ok := operation.Responses["415"]; !ok {
		t.Fatalf("responses = %#v", operation.Responses)
	}
}

func TestOpenAPIJSONReflectsRoutesRegisteredAfterFirstBuild(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/first", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	first := decodeSpec(t, router.OpenAPIJSON())
	if _, ok := first.Paths["/first"]; !ok {
		t.Fatal("first route missing from initial OpenAPI document")
	}

	router.Raw("GET", "/second", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Produces("text/plain"))
	second := decodeSpec(t, router.OpenAPIJSON())
	if _, ok := second.Paths["/first"]; !ok {
		t.Fatal("first route missing after registering a second route")
	}
	if _, ok := second.Paths["/second"]; !ok {
		t.Fatal("route registered after the first OpenAPI build is missing")
	}
}

func TestOpenAPIJSONReturnsIndependentSnapshot(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/snapshot", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})

	first := router.OpenAPIJSON()
	if len(first) == 0 {
		t.Fatal("OpenAPI document is empty")
	}
	original := bytes.Clone(first)
	first[0] = '!'

	second := router.OpenAPIJSON()
	if !bytes.Equal(second, original) {
		t.Fatal("mutating a returned document changed the cached document")
	}
	if !json.Valid(second) {
		t.Fatalf("cached document is not valid JSON: %.100q", second)
	}
}

func TestOpenAPIJSONConcurrentSnapshots(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/concurrent", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})

	var wg sync.WaitGroup
	failures := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			document := router.OpenAPIJSON()
			if !json.Valid(document) {
				failures <- "invalid JSON snapshot"
				return
			}
			document[0] = '!'
			if !json.Valid(router.OpenAPIJSON()) {
				failures <- "snapshot mutation leaked into cache"
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

func TestJSONBodyLimitReturnsRequestEntityTooLarge(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Use(BodyLimit(8))
	testJSON(router, "POST", "/items", testHandler)

	for _, tc := range []struct {
		name          string
		contentLength int64
	}{
		{name: "declared length", contentLength: 41},
		{name: "streamed length", contentLength: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"title":"far too large","count":1}`))
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = tc.contentLength
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if problem.Status != http.StatusRequestEntityTooLarge || !strings.Contains(problem.Detail, "exceeds 8 bytes") {
				t.Fatalf("problem = %#v", problem)
			}
			if strings.Contains(problem.Detail, "request validation failed") {
				t.Fatalf("body limit was reported as validation: %#v", problem)
			}
		})
	}
}

func TestBodyLimitRejectsDeclaredOversizeWithoutReading(t *testing.T) {
	body := &countingBody{Reader: strings.NewReader("payload that must not be read")}
	handlerCalls := 0
	handler := BodyLimit(4)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalls++
	}))
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.ContentLength = int64(body.Len())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d", handlerCalls)
	}
	if body.reads != 0 {
		t.Fatalf("body reads = %d", body.reads)
	}
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type countingBody struct {
	*strings.Reader
	reads int
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *countingBody) Close() error { return nil }

func TestBodyLimitRejectsNegativeLimit(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(fmt.Sprint(recovered), "must not be negative") {
			t.Fatalf("panic = %v", recovered)
		}
	}()
	BodyLimit(-1)
}

func TestPointerQueryDistinguishesMissingAndZero(t *testing.T) {
	type input struct {
		Limit *int `query:"limit" validate:"omitempty,min=1"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/items", func(_ context.Context, _ input) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})

	for _, tc := range []struct {
		name string
		url  string
		want int
	}{
		{name: "missing", url: "/items", want: http.StatusOK},
		{name: "zero", url: "/items?limit=0", want: http.StatusUnprocessableEntity},
		{name: "valid", url: "/items?limit=2", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRejectsNonFiniteFloatParameters(t *testing.T) {
	type input struct {
		Meta  `route:"GET /numbers"`
		Ratio float64 `query:"ratio"`
		Score float32 `header:"X-Score"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(_ context.Context, in input) (map[string]string, error) {
		return map[string]string{"ratio": fmt.Sprint(in.Ratio), "score": fmt.Sprint(in.Score)}, nil
	})

	for _, tc := range []struct {
		name   string
		url    string
		header string
	}{
		{name: "query NaN", url: "/numbers?ratio=NaN"},
		{name: "query positive infinity", url: "/numbers?ratio=%2BInf"},
		{name: "query negative infinity", url: "/numbers?ratio=-Inf"},
		{name: "header NaN", url: "/numbers?ratio=1", header: "NaN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.header != "" {
				req.Header.Set("X-Score", tc.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"message":"invalid number"`) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestRejectsNonFiniteFloatDefault(t *testing.T) {
	type input struct {
		Meta  `route:"GET /numbers"`
		Ratio float64 `query:"ratio" default:"NaN"`
	}
	router := NewRouter(nil, "test", "1")
	assertPanicsWith(t, `invalid default "NaN"`, func() {
		router.Handle(func(context.Context, input) (map[string]string, error) {
			return nil, nil
		})
	})
}

func TestTextParameterAndPointerSliceBinding(t *testing.T) {
	type input struct {
		Meta   `route:"GET /params"`
		Mode   encodedParameter      `query:"mode"`
		Labels []encodedParameter    `query:"label"`
		Values *[]int                `query:"value"`
		CSV    encodedSliceParameter `query:"csv"`
	}
	router := NewRouter(nil, "test", "1")
	var got input
	testJSON(router, "GET", "/params", func(_ context.Context, in input) (map[string]string, error) {
		got = in
		return map[string]string{"ok": "true"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/params?mode=fast&label=a&label=b&value=3&value=4&csv=x,y", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got.Mode.Value != "decoded:fast" {
		t.Fatalf("mode = %#v", got.Mode)
	}
	if len(got.Labels) != 2 || got.Labels[0].Value != "decoded:a" || got.Labels[1].Value != "decoded:b" {
		t.Fatalf("labels = %#v", got.Labels)
	}
	if got.Values == nil || !reflect.DeepEqual(*got.Values, []int{3, 4}) {
		t.Fatalf("values = %#v", got.Values)
	}
	if !reflect.DeepEqual([]string(got.CSV), []string{"x", "y"}) {
		t.Fatalf("csv = %#v", got.CSV)
	}
}

func TestScalarParameterSetters(t *testing.T) {
	type input struct {
		Meta     `route:"GET /scalars"`
		Bool     bool          `query:"bool"`
		Int      int           `query:"int"`
		Uint     uint          `query:"uint"`
		Float    float64       `query:"float"`
		Duration time.Duration `query:"duration"`
		Time     time.Time     `query:"time"`
		Pointer  *int          `query:"pointer"`
	}
	router := NewRouter(nil, "test", "1")
	var got input
	router.Handle(func(_ context.Context, in input) (map[string]string, error) {
		got = in
		return map[string]string{"ok": "true"}, nil
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/scalars?bool=true&int=-2&uint=3&float=1.5&duration=250ms&time=2026-11-08T12:00:00Z&pointer=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !got.Bool || got.Int != -2 || got.Uint != 3 || got.Float != 1.5 || got.Duration != 250*time.Millisecond {
		t.Fatalf("scalar values = %#v", got)
	}
	if !got.Time.Equal(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("time = %s", got.Time)
	}
	if got.Pointer == nil || *got.Pointer != 7 {
		t.Fatalf("pointer = %#v", got.Pointer)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/scalars", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty query status = %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		name    string
		query   string
		message string
	}{
		{name: "bool", query: "bool=maybe", message: "invalid boolean"},
		{name: "int", query: "int=no", message: "invalid integer"},
		{name: "uint", query: "uint=-1", message: "invalid unsigned integer"},
		{name: "float", query: "float=NaN", message: "invalid number"},
		{name: "duration", query: "duration=soon", message: "invalid duration"},
		{name: "time", query: "time=yesterday", message: "parsing time"},
		{name: "pointer", query: "pointer=no", message: "invalid integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/scalars?"+tc.query, nil))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestRepeatedScalarParametersAreRejected(t *testing.T) {
	type input struct {
		Meta  `route:"GET /repeat"`
		Limit int              `query:"limit"`
		Mode  encodedParameter `query:"mode"`
		Key   string           `header:"X-Key"`
		Tags  []string         `query:"tag"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/repeat", func(_ context.Context, in input) (map[string][]string, error) {
		return map[string][]string{"tags": in.Tags}, nil
	})

	for _, tc := range []struct {
		name   string
		url    string
		header []string
		field  string
		want   int
	}{
		{name: "scalar query", url: "/repeat?limit=1&limit=2", field: "limit", want: http.StatusUnprocessableEntity},
		{name: "text query", url: "/repeat?mode=a&mode=b", field: "mode", want: http.StatusUnprocessableEntity},
		{name: "scalar header", url: "/repeat", header: []string{"a", "b"}, field: "X-Key", want: http.StatusUnprocessableEntity},
		{name: "slice query", url: "/repeat?tag=a&tag=b", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			for _, value := range tc.header {
				req.Header.Add("X-Key", value)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.want == http.StatusOK {
				return
			}
			var problem Problem
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatal(err)
			}
			if len(problem.Errors) != 1 || problem.Errors[0].Field != tc.field || problem.Errors[0].Message != "must not be repeated" {
				t.Fatalf("problem = %#v", problem)
			}
		})
	}
}

func TestDefaultQueryValueAppliesOnlyWhenMissing(t *testing.T) {
	type input struct {
		Limit int `query:"limit" default:"10" validate:"min=1"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/items", func(_ context.Context, in input) (map[string]int, error) {
		return map[string]int{"limit": in.Limit}, nil
	})

	for _, tc := range []struct {
		name      string
		url       string
		want      int
		wantLimit int
	}{
		{name: "missing uses default", url: "/items", want: http.StatusOK, wantLimit: 10},
		{name: "explicit zero is invalid", url: "/items?limit=0", want: http.StatusUnprocessableEntity},
		{name: "explicit value wins", url: "/items?limit=2", want: http.StatusOK, wantLimit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			var body map[string]int
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["limit"] != tc.wantLimit {
				t.Fatalf("limit = %d, want %d", body["limit"], tc.wantLimit)
			}
		})
	}

	parameter := decodeSpec(t, router.OpenAPIJSON()).Paths["/items"]["get"].Parameters[0]
	if parameter.Required {
		t.Fatalf("parameter = %#v", parameter)
	}
	if got := parameter.Schema["default"]; got != float64(10) {
		t.Fatalf("default = %#v", got)
	}
}

func TestDefaultHeaderValueAppliesWhenMissing(t *testing.T) {
	type input struct {
		Retries int `header:"X-Retries" default:"3" validate:"min=1"`
	}
	router := NewRouter(nil, "test", "1")
	testJSON(router, "GET", "/items", func(_ context.Context, in input) (map[string]int, error) {
		return map[string]int{"retries": in.Retries}, nil
	})

	for _, tc := range []struct {
		name     string
		headers  []string
		want     int
		wantBody int
	}{
		{name: "missing uses default", want: http.StatusOK, wantBody: 3},
		{name: "explicit value wins", headers: []string{"2"}, want: http.StatusOK, wantBody: 2},
		{name: "explicit zero is invalid", headers: []string{"0"}, want: http.StatusUnprocessableEntity},
		{name: "explicit empty is present", headers: []string{""}, want: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/items", nil)
			for _, value := range tc.headers {
				req.Header.Add("X-Retries", value)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			var body map[string]int
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["retries"] != tc.wantBody {
				t.Fatalf("retries = %d, want %d", body["retries"], tc.wantBody)
			}
		})
	}
}

func TestRegisterValidationIsUsedByPreflightAndBinding(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	if err := router.RegisterValidation("even", func(field validator.FieldLevel) bool {
		return field.Field().Int()%2 == 0
	}); err != nil {
		t.Fatal(err)
	}

	type input struct {
		Number int `query:"n" validate:"required,even"`
	}
	testJSON(router, "GET", "/even", func(_ context.Context, in input) (map[string]int, error) {
		return map[string]int{"n": in.Number}, nil
	})
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		url  string
		want int
	}{
		{url: "/even?n=2", want: http.StatusOK},
		{url: "/even?n=3", want: http.StatusUnprocessableEntity},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if rec.Code != tc.want {
			t.Fatalf("%s status = %d, body = %s", tc.url, rec.Code, rec.Body.String())
		}
	}
}

func TestRegisterValidationRejectsInvalidDefinitions(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	even := func(field validator.FieldLevel) bool { return field.Field().Int()%2 == 0 }

	if err := router.RegisterValidation("", even); err == nil {
		t.Fatal("empty tag was accepted")
	}
	if err := router.RegisterValidation("even", nil); err == nil {
		t.Fatal("nil validator was accepted")
	}
	if err := router.RegisterValidation("required", even); err == nil {
		t.Fatal("built-in tag was replaced")
	}
	if err := router.RegisterValidation("even", even); err != nil {
		t.Fatal(err)
	}
	if err := router.RegisterValidation("even", even); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration error = %v", err)
	}
}

func TestRegisterValidationMustPrecedeRoutes(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Raw("GET", "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden())

	err := router.RegisterValidation("even", func(field validator.FieldLevel) bool {
		return field.Field().Int()%2 == 0
	})
	if err == nil || !strings.Contains(err.Error(), "before routes") {
		t.Fatalf("error = %v", err)
	}
}

func TestRejectsNonCanonicalParameterTags(t *testing.T) {
	tests := []struct {
		name string
		want string
		run  func(*Router)
	}{
		{
			name: "query required shorthand",
			want: `query tag option "required" is not supported; use validate:"required" instead`,
			run: func(router *Router) {
				type input struct {
					Query string `query:"q,required"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "query omitempty option",
			want: `query tag option "omitempty" is not supported`,
			run: func(router *Router) {
				type input struct {
					Query string `query:"q,omitempty"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "legacy body required name",
			want: "body tag must be empty",
			run: func(router *Router) {
				type input struct {
					Body struct{} `body:"required"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "body default",
			want: "body default is not supported",
			run: func(router *Router) {
				type body struct {
					Limit int `json:"limit"`
				}
				type input struct {
					Body body `body:",required" default:"10"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "path default",
			want: "path default is not supported",
			run: func(router *Router) {
				type input struct {
					ID string `path:"id" default:"1"`
				}
				testJSON(router, "GET", "/test/{id}", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "default with required",
			want: `cannot combine default with validate:"required"`,
			run: func(router *Router) {
				type input struct {
					Limit int `query:"limit" default:"10" validate:"required"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
		{
			name: "invalid default",
			want: "invalid default",
			run: func(router *Router) {
				type input struct {
					Limit int `query:"limit" default:"not-an-int"`
				}
				testJSON(router, "GET", "/test", func(context.Context, input) (map[string]string, error) {
					return map[string]string{"ok": "true"}, nil
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertPanicsWith(t, tt.want, func() {
				tt.run(NewRouter(nil, "test", "1"))
			})
		})
	}
}

func assertPanicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		if got := fmt.Sprint(recovered); !strings.Contains(got, want) {
			t.Fatalf("panic = %q, want substring %q", got, want)
		}
	}()
	fn()
}

type errString string

func (e errString) Error() string { return string(e) }

func TestSlugTagOptions(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Slug string `json:"slug" slug:"source=name,max=12,overwrite"`
	}
	value := payload{Name: "Another Very Long Title", Slug: "keep-me-until-overwrite"}
	applySlugs(&value)
	if value.Slug != "another-very" {
		t.Fatalf("slug = %q", value.Slug)
	}

	type preserved struct {
		Name string `json:"name"`
		Slug string `json:"slug" slug:"name"`
	}
	preservedValue := preserved{Name: "Another Title", Slug: "custom"}
	applySlugs(&preservedValue)
	if preservedValue.Slug != "custom" {
		t.Fatalf("custom slug overwritten: %q", preservedValue.Slug)
	}
}

type specDocument struct {
	Paths map[string]map[string]specOperation `json:"paths"`
}

type specOperation struct {
	Summary             string                  `json:"summary"`
	Description         string                  `json:"description"`
	Tags                []string                `json:"tags"`
	RequiredPermissions []string                `json:"x-required-permissions"`
	Parameters          []specParameter         `json:"parameters"`
	RequestBody         *specRequestBody        `json:"requestBody"`
	Responses           map[string]specResponse `json:"responses"`
}

type specParameter struct {
	Name     string         `json:"name"`
	In       string         `json:"in"`
	Required bool           `json:"required"`
	Schema   map[string]any `json:"schema"`
}

type specRequestBody struct {
	Content map[string]specMediaType `json:"content"`
}

type specResponse struct {
	Content map[string]specMediaType `json:"content"`
}

type specMediaType struct {
	Schema map[string]any `json:"schema"`
}

func decodeSpec(t *testing.T, data []byte) specDocument {
	t.Helper()
	var spec specDocument
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}
