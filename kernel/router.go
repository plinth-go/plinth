package kernel

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/plinth-go/plinth/internal/jsonx"
	"github.com/plinth-go/plinth/utils"
)

type Handler[In, Out any] func(context.Context, In) (Out, error)
type Middleware func(http.Handler) http.Handler

type Meta struct{}

var metaType = reflect.TypeFor[Meta]()
var responderType = reflect.TypeFor[Responder]()
var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

func implementsTextUnmarshaler(t reflect.Type) bool {
	t = indirectType(t)
	return t.Implements(textUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func isMetaType(t reflect.Type) bool {
	t = indirectType(t)
	return t == metaType
}

func isMetaField(field reflect.StructField) bool {
	return field.Anonymous && isMetaType(field.Type)
}

func implementsResponder(t reflect.Type) bool {
	if t.Implements(responderType) {
		return true
	}
	return t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(responderType)
}

func validateMetaFields(t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		if field := t.Field(i); isMetaType(field.Type) && !field.Anonymous {
			panic(fmt.Sprintf("kernel: Meta in %s must be embedded", t))
		}
	}
}

func validateInputMetaFields(t reflect.Type) {
	validateMetaFields(t)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !isMetaField(field) {
			continue
		}
		for _, tag := range []string{"status", "produces"} {
			if _, ok := field.Tag.Lookup(tag); ok {
				panic(fmt.Sprintf("kernel: %s tag on input %s belongs on the output Meta", tag, t))
			}
		}
	}
}

func validateOutputMetaFields(t reflect.Type) {
	validateMetaFields(t)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !isMetaField(field) {
			continue
		}
		if _, ok := field.Tag.Lookup("route"); ok {
			panic(fmt.Sprintf("kernel: route tag on output %s belongs on the input Meta", t))
		}
		if _, ok := field.Tag.Lookup("authz"); ok {
			panic(fmt.Sprintf("kernel: authz tag on output %s belongs on the input Meta", t))
		}
	}
}

type Responder interface {
	Respond(http.ResponseWriter, *http.Request) error
}

type Headerer interface {
	ResponseHeaders() http.Header
}

type ContextValidator interface {
	Validate(context.Context) error
}

type PlainValidator interface {
	Validate() error
}

type Option func(*OperationMeta)

type ParamMeta struct {
	Name        string
	In          string
	Required    bool
	Description string
	Schema      map[string]any
}

type BodyMeta struct {
	Required bool
	Schema   map[string]any
}

type OperationMeta struct {
	Method              string
	Path                string
	OperationID         string
	Summary             string
	Description         string
	Tags                []string
	RequiredPermissions []string
	Status              int
	NoContent           bool
	Hidden              bool
	// SkipAccessLog excludes matched requests from AccessLogWithConfig records.
	// It is independent of Hidden, which only controls OpenAPI visibility.
	SkipAccessLog  bool
	Produces       []string
	Params         []ParamMeta
	Body           *BodyMeta
	ResponseSchema map[string]any
	raw            bool
	responder      bool
	inputType      reflect.Type
	defaults       []defaultCheck
}

type defaultCheck struct {
	in          string
	name        string
	raw         string
	value       any
	validateTag string
}

type Router struct {
	mux             *http.ServeMux
	logger          *slog.Logger
	title           string
	version         string
	validate        *validator.Validate
	validationTags  map[string]bool
	middleware      []Middleware
	ops             []OperationMeta
	docs            DocSet
	frozen          bool
	opsMu           sync.Mutex
	opsRevision     uint64
	openapiJSON     []byte
	openapiRevision uint64
}

func NewRouter(logger *slog.Logger, title, version string) *Router {
	logger = utils.IfNilElse(logger, slog.Default())
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		for _, tagName := range []string{"json", "query", "header", "path"} {
			name, _, _ := strings.Cut(field.Tag.Get(tagName), ",")
			if name != "" && name != "-" {
				return name
			}
		}
		return field.Name
	})
	r := &Router{
		mux:            http.NewServeMux(),
		logger:         logger,
		title:          title,
		version:        version,
		validate:       v,
		validationTags: map[string]bool{},
	}
	return r
}

// RegisterValidation adds a custom go-playground/validator tag to this router.
// Call it before registering routes, Router.Validate, and serving requests.
// Custom tags are checked by Router.Validate but are not translated into
// OpenAPI constraints.
func (r *Router) RegisterValidation(tag string, fn validator.Func) (err error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return errors.New("kernel: validation tag is empty")
	}
	if fn == nil {
		return fmt.Errorf("kernel: validation function for %q is nil", tag)
	}
	if r.frozen {
		return errors.New("kernel: validation tags must be registered before routes")
	}
	if r.validationTags[tag] {
		return fmt.Errorf("kernel: validation tag %q is already registered", tag)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("kernel: register validation %q: %v", tag, recovered)
		}
	}()
	if err := r.validate.RegisterValidation(tag, fn); err != nil {
		return fmt.Errorf("kernel: register validation %q: %w", tag, err)
	}
	r.validationTags[tag] = true
	return nil
}

func (r *Router) Use(mw ...Middleware) {
	if r.frozen {
		panic("kernel: Router.Use must be called before registering routes")
	}
	requireMiddleware(mw, "Router.Use")
	r.middleware = append(r.middleware, mw...)
}

type Group struct {
	router        *Router
	parent        *Group
	prefix        string
	middleware    []Middleware
	tags          []string
	skipAccessLog bool
	frozen        bool
}

func (r *Router) Group(prefix string, mw ...Middleware) *Group {
	requireMiddleware(mw, "Router.Group")
	return &Group{
		router:     r,
		prefix:     normalizeGroupPrefix(prefix),
		middleware: append([]Middleware(nil), mw...),
	}
}

func (g *Group) Group(prefix string, mw ...Middleware) *Group {
	requireMiddleware(mw, "Group.Group")
	return &Group{
		router:     g.router,
		parent:     g,
		prefix:     joinGroupPrefix(g.prefix, prefix),
		middleware: append([]Middleware(nil), mw...),
	}
}

func (g *Group) Use(mw ...Middleware) {
	if g.frozen {
		panic("kernel: Group.Use must be called before registering routes on this group or its children")
	}
	requireMiddleware(mw, "Group.Use")
	g.middleware = append(g.middleware, mw...)
}

func (g *Group) freeze() {
	for current := g; current != nil; current = current.parent {
		current.frozen = true
	}
}

func (g *Group) Tag(tags ...string) *Group {
	if g.frozen {
		panic("kernel: Group.Tag must be called before registering routes on this group or its children")
	}
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			g.tags = append(g.tags, tag)
		}
	}
	return g
}

// SkipAccessLog marks routes registered directly on this group, on descendant
// groups, and through RegisterOps as excluded from AccessLogWithConfig records.
// It must be called before those routes are registered.
func (g *Group) SkipAccessLog() *Group {
	if g.frozen {
		panic("kernel: Group.SkipAccessLog must be called before registering routes on this group or its children")
	}
	g.skipAccessLog = true
	return g
}

func (g *Group) Prefix() string {
	if g.prefix == "" {
		return "/"
	}
	return g.prefix
}

func (g *Group) layers() []Middleware {
	if g.parent == nil {
		return append([]Middleware(nil), g.middleware...)
	}
	layers := g.parent.layers()
	return append(layers, g.middleware...)
}

func (g *Group) tagList() []string {
	if g.parent == nil {
		return append([]string(nil), g.tags...)
	}
	tags := g.parent.tagList()
	return append(tags, g.tags...)
}

func (g *Group) options(opts []Option) []Option {
	tags := g.tagList()
	skipAccessLog := g.skipsAccessLog()
	if len(tags) == 0 && !skipAccessLog {
		return opts
	}
	combined := make([]Option, 0, len(opts)+2)
	if len(tags) > 0 {
		combined = append(combined, Tags(tags...))
	}
	if skipAccessLog {
		combined = append(combined, SkipAccessLog())
	}
	return append(combined, opts...)
}

func (g *Group) skipsAccessLog() bool {
	for current := g; current != nil; current = current.parent {
		if current.skipAccessLog {
			return true
		}
	}
	return false
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) Raw(method, pattern string, h http.Handler, opts ...Option) {
	r.raw("", nil, method, pattern, h, opts...)
}

func (g *Group) Raw(method, pattern string, h http.Handler, opts ...Option) {
	g.router.raw(g.prefix, g.layers(), method, pattern, h, g.options(opts)...)
	g.freeze()
}

func (r *Router) raw(prefix string, mw []Middleware, method, pattern string, h http.Handler, opts ...Option) {
	requireHTTPMethod(method)
	if isNilHTTPHandler(h) {
		panic("kernel: raw handler is nil")
	}
	path := joinRoute(prefix, pattern)
	meta := OperationMeta{
		Method:      method,
		Path:        path,
		OperationID: operationID(method, path),
		Status:      http.StatusOK,
		raw:         true,
	}
	if doc, ok := r.operationDoc(h); ok {
		meta.Summary = doc.Summary
		meta.Description = doc.Description
	}
	applyOptions(&meta, opts)
	handler := r.routeHandler(meta, h, mw...)
	r.preflightRoutes(routePattern{method: method, path: path})
	r.mux.Handle(method+" "+path, handler)
	r.frozen = true
	r.appendOperation(meta)
}

func (r *Router) Handle[In, Out any](h Handler[In, Out], opts ...Option) {
	if h == nil {
		panic("kernel: typed handler is nil")
	}
	method, path := routeFromInput[In]()
	r.json("", nil, method, path, h, opts...)
}

func (g *Group) Handle[In, Out any](h Handler[In, Out], opts ...Option) {
	if h == nil {
		panic("kernel: typed handler is nil")
	}
	method, path := routeFromInput[In]()
	g.router.json(g.prefix, g.layers(), method, path, h, g.options(opts)...)
	g.freeze()
}

func (r *Router) json[In, Out any](prefix string, mw []Middleware, method, pattern string, h Handler[In, Out], opts ...Option) {
	if h == nil {
		panic("kernel: typed handler is nil")
	}
	path := joinRoute(prefix, pattern)
	meta, fields, body := analyzeInput[In](r.docs.Fields, method, path)
	meta.inputType = reflect.TypeFor[In]()
	var empty bool
	meta.ResponseSchema, empty = analyzeOutput[Out](r.docs.Fields)
	meta.responder = implementsResponder(reflect.TypeFor[Out]())
	status, produces := responseTags[Out]()
	if status != 0 {
		meta.Status = status
	} else if empty {
		meta.Status = http.StatusNoContent
	}
	meta.Produces = produces
	if doc, ok := r.operationDoc(h); ok {
		meta.Summary = doc.Summary
		meta.Description = doc.Description
	}
	applyOptions(&meta, opts)
	meta.NoContent = meta.Status == http.StatusNoContent

	plan := routePlan[In, Out]{
		bind:  bindInput[In](r.validate, fields, body),
		write: writeOutput[Out](meta.Status, meta.Produces, r.logger),
	}

	handler := r.routeHandler(meta, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		in, err := plan.bind(req)
		if err != nil {
			WriteProblem(w, req, err, r.logger.Error)
			return
		}
		out, err := h(req.Context(), in)
		if err != nil {
			WriteProblem(w, req, err, r.logger.Error)
			return
		}
		plan.write(w, req, out)
	}), mw...)
	r.preflightRoutes(routePattern{method: method, path: path})
	r.mux.Handle(method+" "+path, handler)
	r.frozen = true
	r.appendOperation(meta)
}

type routePattern struct {
	method string
	path   string
}

func (r *Router) preflightRoutes(routes ...routePattern) {
	probe := http.NewServeMux()
	for _, meta := range r.operationSnapshot() {
		probe.Handle(meta.Method+" "+meta.Path, http.NotFoundHandler())
	}
	for _, route := range routes {
		probe.Handle(route.method+" "+route.path, http.NotFoundHandler())
	}
}

// OpenAPIJSON returns an independent snapshot of the generated OpenAPI
// document. Callers may retain or modify it without affecting the router cache.
func (r *Router) OpenAPIJSON() []byte {
	r.opsMu.Lock()
	defer r.opsMu.Unlock()
	if r.openapiJSON == nil || r.openapiRevision != r.opsRevision {
		r.openapiJSON = buildOpenAPI(r.title, r.version, r.ops)
		r.openapiRevision = r.opsRevision
	}
	return bytes.Clone(r.openapiJSON)
}

func (r *Router) appendOperation(meta OperationMeta) {
	r.opsMu.Lock()
	defer r.opsMu.Unlock()
	r.ops = append(r.ops, meta)
	r.opsRevision++
}

func (r *Router) operationSnapshot() []OperationMeta {
	r.opsMu.Lock()
	defer r.opsMu.Unlock()
	return append([]OperationMeta(nil), r.ops...)
}

func (r *Router) chain(h http.Handler, group ...Middleware) http.Handler {
	for i := len(group) - 1; i >= 0; i-- {
		h = applyMiddleware(group[i], h)
	}
	for i := len(r.middleware) - 1; i >= 0; i-- {
		h = applyMiddleware(r.middleware[i], h)
	}
	return h
}

// routeHandler binds immutable registration metadata to the request before
// application middleware runs. Middleware can therefore use operation IDs and
// declarative permissions without re-parsing the matched path.
func (r *Router) routeHandler(meta OperationMeta, h http.Handler, group ...Middleware) http.Handler {
	next := r.chain(h, group...)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), operationContextKey{}, meta)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func requireMiddleware(mw []Middleware, source string) {
	for index, middleware := range mw {
		if middleware == nil {
			panic(fmt.Sprintf("kernel: %s received nil middleware at index %d", source, index))
		}
	}
}

func applyOptions(meta *OperationMeta, opts []Option) {
	for index, option := range opts {
		if option == nil {
			panic(fmt.Sprintf("kernel: option at index %d is nil", index))
		}
		option(meta)
	}
}

func applyMiddleware(middleware Middleware, next http.Handler) http.Handler {
	handler := middleware(next)
	if isNilHTTPHandler(handler) {
		panic("kernel: middleware returned nil handler")
	}
	return handler
}

func isNilHTTPHandler(handler http.Handler) bool {
	return isNilInterface(handler)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func normalizeGroupPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		panic("kernel: group prefix must start with /")
	}
	return strings.TrimRight(prefix, "/")
}

func joinGroupPrefix(parent, child string) string {
	child = normalizeGroupPrefix(child)
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return parent + child
}

func joinRoute(prefix, pattern string) string {
	if pattern != "" && !strings.HasPrefix(pattern, "/") {
		panic("kernel: route pattern must start with /")
	}
	if prefix == "" {
		if pattern == "" {
			return "/"
		}
		return pattern
	}
	if pattern == "" {
		return prefix
	}
	return prefix + pattern
}

func routeFromInput[In any]() (string, string) {
	t := reflect.TypeFor[In]()
	method := ""
	path := ""
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag, ok := field.Tag.Lookup("route")
		if !ok {
			continue
		}
		if !isMetaField(field) {
			panic(fmt.Sprintf("kernel: route tag on %s must be on kernel.Meta", t))
		}
		if method != "" {
			panic("kernel: multiple route tags in input")
		}
		parts := strings.Fields(tag)
		if len(parts) < 1 || len(parts) > 2 {
			panic("kernel: route tag must be \"METHOD\" or \"METHOD /path\"")
		}
		method = parts[0]
		if len(parts) == 2 {
			path = parts[1]
			if !strings.HasPrefix(path, "/") {
				panic("kernel: route path must start with /")
			}
		}
	}
	if method == "" {
		panic(fmt.Sprintf("kernel: %s requires a route tag", t))
	}
	requireHTTPMethod(method)
	return method, path
}

func requireHTTPMethod(method string) {
	if method == "" || method != strings.ToUpper(method) || !validHTTPToken(method) {
		panic(fmt.Sprintf("kernel: route method %q must be an uppercase HTTP token", method))
	}
}

func responseTags[Out any]() (int, []string) {
	t := indirectType(reflect.TypeFor[Out]())
	if t.Kind() != reflect.Struct {
		return 0, nil
	}
	validateOutputMetaFields(t)

	status := 0
	var produces []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !isMetaField(field) {
			if _, ok := field.Tag.Lookup("status"); ok {
				panic(fmt.Sprintf("kernel: status tag on %s must be on kernel.Meta", t))
			}
			if _, ok := field.Tag.Lookup("produces"); ok {
				panic(fmt.Sprintf("kernel: produces tag on %s must be on kernel.Meta", t))
			}
			continue
		}
		if raw, ok := field.Tag.Lookup("status"); ok {
			if status != 0 {
				panic(fmt.Sprintf("kernel: %s has multiple status tags", t))
			}
			code, err := strconv.Atoi(raw)
			if err != nil || code < 100 || code > 599 {
				panic(fmt.Sprintf("kernel: %s has invalid status tag %q", t, raw))
			}
			status = code
		}
		if raw, ok := field.Tag.Lookup("produces"); ok {
			for _, contentType := range strings.Split(raw, ",") {
				contentType = strings.TrimSpace(contentType)
				if contentType == "" {
					panic(fmt.Sprintf("kernel: %s has empty produces content type", t))
				}
				produces = append(produces, contentType)
			}
		}
	}
	return status, produces
}

func Summary(s string) Option     { return func(m *OperationMeta) { m.Summary = s } }
func Description(s string) Option { return func(m *OperationMeta) { m.Description = s } }
func Tags(tags ...string) Option  { return func(m *OperationMeta) { m.Tags = append(m.Tags, tags...) } }
func Status(code int) Option      { return func(m *OperationMeta) { m.Status = code } }
func OperationID(s string) Option { return func(m *OperationMeta) { m.OperationID = s } }
func Hidden() Option              { return func(m *OperationMeta) { m.Hidden = true } }

// SkipAccessLog marks the route as excluded from AccessLogWithConfig records.
// Group.SkipAccessLog applies the same policy to every route in a group.
func SkipAccessLog() Option { return func(m *OperationMeta) { m.SkipAccessLog = true } }

func RequirePermissions(permissions ...string) Option {
	normalized := normalizePermissions(permissions, "kernel.RequirePermissions")
	return func(m *OperationMeta) { m.RequiredPermissions = append([]string(nil), normalized...) }
}
func Produces(contentTypes ...string) Option {
	return func(m *OperationMeta) { m.Produces = append([]string(nil), contentTypes...) }
}

type routePlan[In, Out any] struct {
	bind  func(*http.Request) (In, error)
	write func(http.ResponseWriter, *http.Request, Out)
}

type bindField struct {
	index        []int
	in           string
	name         string
	headerName   string
	required     bool
	defaultValue string
	hasDefault   bool
	repeated     bool
	set          paramSetter
}

type paramSetter func(reflect.Value, string) error

func analyzeInput[In any](fieldDocs map[string]string, method, pattern string) (OperationMeta, []bindField, *bindField) {
	meta := OperationMeta{
		Method:      method,
		Path:        pattern,
		OperationID: operationID(method, pattern),
		Status:      http.StatusOK,
	}

	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("kernel: In must be a struct, got %s", t))
	}
	validateInputMetaFields(t)
	meta.RequiredPermissions = inputPermissions(t)
	if err := checkSlugFields(t, map[reflect.Type]bool{}); err != nil {
		panic("kernel: " + err.Error())
	}

	fields := make([]bindField, 0, t.NumField())
	var body *bindField
	parameterSchemas := newSchemaBuilder(fieldDocs, schemaParameter)
	bodySchemas := newSchemaBuilder(fieldDocs, schemaRequest)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if isMetaField(field) {
			continue
		}
		if field.PkgPath != "" {
			continue
		}

		if name, options, ok := tagValue(field, "path"); ok {
			if name == "" {
				panic(fmt.Sprintf("kernel: %s.%s path tag requires a parameter name", typeLabel(t), field.Name))
			}
			ensureTagOptions(t, field, "path", options)
			if _, ok := field.Tag.Lookup("default"); ok {
				panic(fmt.Sprintf("kernel: %s.%s path default is not supported; path parameters are always required", typeLabel(t), field.Name))
			}
			schema := parameterSchemas.schema(field.Type, field.Tag)
			meta.Params = append(meta.Params, ParamMeta{
				Name: name, In: "path", Required: true, Schema: schema,
				Description: parameterSchemas.fieldDescription(t, field),
			})
			setter, repeated := parameterSetter(field.Type)
			fields = append(fields, bindField{
				index: field.Index, in: "path", name: name, required: true,
				repeated: repeated, set: setter,
			})
			continue
		}
		if name, options, ok := tagValue(field, "query"); ok {
			if name == "" {
				panic(fmt.Sprintf("kernel: %s.%s query tag requires a parameter name", typeLabel(t), field.Name))
			}
			ensureTagOptions(t, field, "query", options)
			required := parseValidation(field.Tag.Get("validate")).required
			schema := parameterSchemas.schema(field.Type, field.Tag)
			check, hasDefault := applyDefault(t, field, name, "query", required, schema)
			if hasDefault {
				meta.defaults = append(meta.defaults, check)
			}
			meta.Params = append(meta.Params, ParamMeta{
				Name: name, In: "query", Required: required, Schema: schema,
				Description: parameterSchemas.fieldDescription(t, field),
			})
			setter, repeated := parameterSetter(field.Type)
			fields = append(fields, bindField{
				index: field.Index, in: "query", name: name, required: required,
				defaultValue: check.raw, hasDefault: hasDefault, repeated: repeated, set: setter,
			})
			continue
		}
		if name, options, ok := tagValue(field, "header"); ok {
			if name == "" {
				panic(fmt.Sprintf("kernel: %s.%s header tag requires a parameter name", typeLabel(t), field.Name))
			}
			ensureTagOptions(t, field, "header", options)
			required := parseValidation(field.Tag.Get("validate")).required
			schema := parameterSchemas.schema(field.Type, field.Tag)
			check, hasDefault := applyDefault(t, field, name, "header", required, schema)
			if hasDefault {
				meta.defaults = append(meta.defaults, check)
			}
			meta.Params = append(meta.Params, ParamMeta{
				Name: name, In: "header", Required: required, Schema: schema,
				Description: parameterSchemas.fieldDescription(t, field),
			})
			setter, repeated := parameterSetter(field.Type)
			fields = append(fields, bindField{
				index: field.Index, in: "header", name: name, required: required,
				headerName:   textproto.CanonicalMIMEHeaderKey(name),
				defaultValue: check.raw, hasDefault: hasDefault, repeated: repeated, set: setter,
			})
			continue
		}
		if name, options, ok := tagValue(field, "body"); ok {
			if body != nil {
				panic("kernel: multiple body fields in input")
			}
			if name != "" {
				panic(fmt.Sprintf("kernel: %s.%s body tag must be empty; use `body:\",required\"` for a required body", typeLabel(t), field.Name))
			}
			ensureTagOptions(t, field, "body", options, "required")
			if _, ok := field.Tag.Lookup("default"); ok {
				panic(fmt.Sprintf("kernel: %s.%s body default is not supported; JSON absence and zero values are indistinguishable, so use *T and apply the default in the handler", typeLabel(t), field.Name))
			}
			required := options["required"]
			body = &bindField{index: field.Index, in: "body", required: required}
			meta.Body = &BodyMeta{
				Required: required,
				Schema:   bodySchemas.schema(field.Type, field.Tag),
			}
			continue
		}

		panic(fmt.Sprintf("kernel: input field %s.%s must have one of path/query/header/body tags", t.Name(), field.Name))
	}

	return meta, fields, body
}

func typeLabel(t reflect.Type) string {
	if t.Name() != "" {
		return t.Name()
	}
	return t.String()
}

func ensureTagOptions(t reflect.Type, field reflect.StructField, tagName string, options map[string]bool, allowed ...string) {
	permitted := make(map[string]bool, len(allowed))
	for _, option := range allowed {
		permitted[option] = true
	}
	for option := range options {
		if permitted[option] {
			continue
		}
		hint := ""
		if option == "required" && (tagName == "query" || tagName == "header") {
			hint = `; use validate:"required" instead`
		}
		panic(fmt.Sprintf("kernel: %s.%s %s tag option %q is not supported%s", typeLabel(t), field.Name, tagName, option, hint))
	}
}

func applyDefault(t reflect.Type, field reflect.StructField, name, location string, required bool, schema map[string]any) (defaultCheck, bool) {
	raw, ok := field.Tag.Lookup("default")
	if !ok {
		return defaultCheck{}, false
	}
	if location != "query" && location != "header" {
		panic(fmt.Sprintf("kernel: %s.%s default is only supported on query and header parameters", typeLabel(t), field.Name))
	}
	if required {
		panic(fmt.Sprintf("kernel: %s.%s cannot combine default with validate:\"required\"", typeLabel(t), field.Name))
	}
	value, err := parseDefaultValue(field.Type, raw)
	if err != nil {
		panic(fmt.Sprintf("kernel: %s.%s has invalid default %q: %v", typeLabel(t), field.Name, raw, err))
	}
	schema["default"] = schemaDefaultValue(field.Type, raw, value)
	return defaultCheck{
		in:          location,
		name:        name,
		raw:         raw,
		value:       value,
		validateTag: field.Tag.Get("validate"),
	}, true
}

func schemaDefaultValue(t reflect.Type, raw string, value any) any {
	t = indirectType(t)
	if t == durationType || implementsTextUnmarshaler(t) {
		return raw
	}
	return value
}

func parseDefaultValue(t reflect.Type, raw string) (any, error) {
	value := reflect.New(t).Elem()
	if err := setParam(value, raw); err != nil {
		return nil, err
	}
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value.Interface(), nil
}

func analyzeOutput[Out any](fieldDocs map[string]string) (map[string]any, bool) {
	declared := reflect.TypeFor[Out]()
	t := indirectType(declared)
	responseSchemas := newSchemaBuilder(fieldDocs, schemaResponse).withAddressable(declared.Kind() == reflect.Pointer)
	if t.Kind() != reflect.Struct {
		return responseSchemas.schema(t, reflect.StructTag("")), false
	}
	validateMetaFields(t)
	if implementsResponder(t) {
		return nil, false
	}
	if declared.Implements(textMarshalerType) || declared.Implements(jsonMarshalerType) {
		return responseSchemas.schema(t, reflect.StructTag("")), false
	}
	payloadFields := 0
	for i := 0; i < t.NumField(); i++ {
		if !isMetaField(t.Field(i)) {
			payloadFields++
		}
	}
	if payloadFields == 0 {
		return nil, true
	}
	return responseSchemas.schema(t, reflect.StructTag("")), false
}

func tagValue(field reflect.StructField, key string) (string, map[string]bool, bool) {
	tag, ok := field.Tag.Lookup(key)
	if !ok {
		return "", nil, false
	}
	name, rawOptions, _ := strings.Cut(tag, ",")
	options := make(map[string]bool)
	for _, option := range strings.Split(rawOptions, ",") {
		option = strings.TrimSpace(option)
		if option != "" {
			options[option] = true
		}
	}
	return name, options, true
}

func bindInput[In any](validate *validator.Validate, fields []bindField, body *bindField) func(*http.Request) (In, error) {
	locations := map[string]string{}
	inputType := reflect.TypeFor[In]()
	hasQueryFields := false
	for _, f := range fields {
		field := inputType.FieldByIndex(f.index)
		locations[field.Name] = f.in
		if f.in == "query" {
			hasQueryFields = true
		}
		for _, tagName := range []string{"json", "query", "header", "path"} {
			name, _, _ := strings.Cut(field.Tag.Get(tagName), ",")
			if name != "" && name != "-" {
				locations[name] = f.in
			}
		}
	}
	validateStruct := hasValidationRules(inputType, map[reflect.Type]bool{})
	applySlugFields := hasSlugFields(inputType, map[reflect.Type]bool{})
	inputPointer := reflect.PointerTo(inputType)
	contextValidator := inputPointer.Implements(reflect.TypeFor[ContextValidator]())
	plainValidator := inputPointer.Implements(reflect.TypeFor[PlainValidator]())

	return func(req *http.Request) (In, error) {
		var in In
		rv := reflect.ValueOf(&in).Elem()
		errs := make([]FieldError, 0)
		var queryValues url.Values
		var queryErr error
		if hasQueryFields && req.URL.RawQuery != "" {
			queryValues, queryErr = url.ParseQuery(req.URL.RawQuery)
			if queryErr != nil {
				errs = append(errs, FieldError{Location: "query", Field: "query", Message: "malformed query string: " + queryErr.Error()})
			}
		}

		for _, f := range fields {
			fv := rv.FieldByIndex(f.index)
			switch f.in {
			case "path":
				raw := req.PathValue(f.name)
				if raw == "" {
					if f.required {
						errs = append(errs, FieldError{Location: "path", Field: f.name, Message: "is required"})
					}
					continue
				}
				if err := f.set(fv, raw); err != nil {
					errs = append(errs, FieldError{Location: "path", Field: f.name, Message: err.Error()})
				}
			case "query":
				if queryErr != nil {
					continue
				}
				values, ok := queryValues[f.name]
				if !ok || len(values) == 0 {
					if f.required {
						errs = append(errs, FieldError{Location: "query", Field: f.name, Message: "is required"})
					} else if f.hasDefault {
						if err := f.set(fv, f.defaultValue); err != nil {
							errs = append(errs, FieldError{Location: "query", Field: f.name, Message: err.Error()})
						}
					}
					continue
				}
				if len(values) > 1 && !f.repeated {
					errs = append(errs, FieldError{Location: "query", Field: f.name, Message: "must not be repeated"})
					continue
				}
				var err error
				if f.repeated {
					err = setValues(fv, values, f.set)
				} else {
					err = f.set(fv, values[0])
				}
				if err != nil {
					errs = append(errs, FieldError{Location: "query", Field: f.name, Message: err.Error()})
				}
			case "header":
				values := req.Header[f.headerName]
				if len(values) == 0 {
					if f.required {
						errs = append(errs, FieldError{Location: "header", Field: f.name, Message: "is required"})
					} else if f.hasDefault {
						if err := f.set(fv, f.defaultValue); err != nil {
							errs = append(errs, FieldError{Location: "header", Field: f.name, Message: err.Error()})
						}
					}
					continue
				}
				if len(values) > 1 {
					errs = append(errs, FieldError{Location: "header", Field: f.name, Message: "must not be repeated"})
					continue
				}
				if err := f.set(fv, values[0]); err != nil {
					errs = append(errs, FieldError{Location: "header", Field: f.name, Message: err.Error()})
				}
			}
		}

		if body != nil {
			if requestHasBody(req) {
				contentTypes := req.Header.Values("Content-Type")
				if len(contentTypes) != 1 {
					return in, UnsupportedMediaType("Content-Type must be application/json")
				}
				if err := validateJSONContentType(contentTypes[0]); err != nil {
					return in, UnsupportedMediaType(err.Error())
				}
				if encodings := req.Header.Values("Content-Encoding"); len(encodings) != 0 && (len(encodings) != 1 || !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity")) {
					return in, UnsupportedMediaType("Content-Encoding must be identity")
				}
			}
			fv := rv.FieldByIndex(body.index)
			var bodyReader io.Reader = http.NoBody
			if !isNilInterface(req.Body) {
				bodyReader = req.Body
			}
			dec := jsonx.NewDecoder(bodyReader)
			err := dec.Decode(fv.Addr().Interface())
			if err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					return in, TooLarge(fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit))
				}
				if errors.Is(err, io.EOF) && !body.required {
					// Optional body and no bytes: leave zero value.
				} else {
					errs = append(errs, FieldError{Location: "body", Field: "body", Message: decodeErrorMessage(err)})
				}
			}
			if err == nil {
				trailingErr := dec.RequireEOF()
				var maxBytesErr *http.MaxBytesError
				if errors.As(trailingErr, &maxBytesErr) {
					return in, TooLarge(fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit))
				}
				if trailingErr != nil {
					errs = append(errs, FieldError{Location: "body", Field: "body", Message: "must contain exactly one JSON value"})
				}
			}
		}

		if len(errs) > 0 {
			return in, &ValidationError{Errors: errs}
		}

		if applySlugFields {
			applySlugs(&in)
		}

		if validateStruct {
			if err := validate.Struct(&in); err != nil {
				var ves validator.ValidationErrors
				if errors.As(err, &ves) {
					for _, fe := range ves {
						errs = append(errs, FieldError{
							Location: fieldLocation(fe.Namespace(), locations),
							Field:    fe.Field(),
							Message:  validationMessage(fe.Tag(), fe.Param()),
						})
					}
					return in, &ValidationError{Errors: errs}
				}
				return in, Internal(err)
			}
		}

		if contextValidator {
			v := any(&in).(ContextValidator)
			if err := v.Validate(req.Context()); err != nil {
				return in, validationOrHTTP(err)
			}
		}
		if plainValidator {
			v := any(&in).(PlainValidator)
			if err := v.Validate(); err != nil {
				return in, validationOrHTTP(err)
			}
		}

		return in, nil
	}
}

func hasValidationRules(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = indirectType(t)
	if t == timeType || t == rawMessageType || t == metaType {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return hasValidationRules(t.Elem(), seen)
	case reflect.Map:
		return hasValidationRules(t.Key(), seen) || hasValidationRules(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return false
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || isMetaField(field) {
				continue
			}
			if tag := field.Tag.Get("validate"); tag != "" && tag != "-" {
				return true
			}
			if hasValidationRules(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

func requestHasBody(req *http.Request) bool {
	if isNilInterface(req.Body) || req.Body == http.NoBody {
		return false
	}
	return req.ContentLength != 0
}

func validateJSONContentType(value string) error {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "application/json") ||
		strings.EqualFold(value, "application/json;charset=utf-8") ||
		strings.EqualFold(value, "application/json; charset=utf-8") {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	if charset, ok := params["charset"]; ok && !strings.EqualFold(charset, "utf-8") {
		return errors.New("Content-Type must be application/json with charset utf-8")
	}
	return nil
}

func setValues(fv reflect.Value, values []string, set paramSetter) error {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		fv = fv.Elem()
	}
	if fv.Kind() == reflect.Slice {
		out := reflect.MakeSlice(fv.Type(), len(values), len(values))
		for i, value := range values {
			if err := set(out.Index(i), value); err != nil {
				return err
			}
		}
		fv.Set(out)
		return nil
	}
	return set(fv, values[0])
}

func parameterSetter(t reflect.Type) (paramSetter, bool) {
	if implementsTextUnmarshaler(t) {
		return setParam, false
	}
	base := indirectType(t)
	if base.Kind() == reflect.Slice {
		return scalarSetter(base.Elem()), true
	}
	return scalarSetter(t), false
}

func scalarSetter(t reflect.Type) paramSetter {
	if implementsTextUnmarshaler(t) {
		return setParam
	}
	base := indirectType(t)
	if base == timeType || base == durationType {
		return setParam
	}
	switch base.Kind() {
	case reflect.String:
		return func(fv reflect.Value, value string) error {
			indirectValue(fv).SetString(value)
			return nil
		}
	case reflect.Bool:
		return func(fv reflect.Value, value string) error {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean")
			}
			indirectValue(fv).SetBool(parsed)
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bits := base.Bits()
		return func(fv reflect.Value, value string) error {
			parsed, err := strconv.ParseInt(value, 10, bits)
			if err != nil {
				return fmt.Errorf("invalid integer")
			}
			indirectValue(fv).SetInt(parsed)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := base.Bits()
		return func(fv reflect.Value, value string) error {
			parsed, err := strconv.ParseUint(value, 10, bits)
			if err != nil {
				return fmt.Errorf("invalid unsigned integer")
			}
			indirectValue(fv).SetUint(parsed)
			return nil
		}
	case reflect.Float32, reflect.Float64:
		bits := base.Bits()
		return func(fv reflect.Value, value string) error {
			parsed, err := strconv.ParseFloat(value, bits)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return fmt.Errorf("invalid number")
			}
			indirectValue(fv).SetFloat(parsed)
			return nil
		}
	default:
		return setParam
	}
}

func indirectValue(fv reflect.Value) reflect.Value {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		fv = fv.Elem()
	}
	return fv
}

func setParam(fv reflect.Value, value string) error {
	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setParam(fv.Elem(), value)
	}
	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(value))
		}
	}
	if fv.Type() == timeType {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return fmt.Errorf("invalid RFC3339 time")
		}
		fv.Set(reflect.ValueOf(t))
		return nil
	}
	if fv.Type() == reflect.TypeFor[time.Duration]() {
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration")
		}
		fv.SetInt(int64(d))
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(value)
	case reflect.Bool:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean")
		}
		fv.SetBool(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(value, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer")
		}
		fv.SetInt(v)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(value, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid unsigned integer")
		}
		fv.SetUint(v)
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(value, fv.Type().Bits())
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid number")
		}
		fv.SetFloat(v)
	default:
		return fmt.Errorf("unsupported parameter type %s", fv.Type())
	}
	return nil
}

type outputCapabilities uint8

const (
	outputPointerHeaderer outputCapabilities = 1 << iota
	outputValueHeaderer
	outputPointerResponder
	outputValueResponder
)

func analyzeOutputCapabilities[Out any]() outputCapabilities {
	outType := reflect.TypeFor[Out]()
	headererType := reflect.TypeFor[Headerer]()
	responderType := reflect.TypeFor[Responder]()
	var capabilities outputCapabilities
	if reflect.PointerTo(outType).Implements(headererType) {
		capabilities |= outputPointerHeaderer
	}
	if outType.Implements(headererType) {
		capabilities |= outputValueHeaderer
	}
	if reflect.PointerTo(outType).Implements(responderType) {
		capabilities |= outputPointerResponder
	}
	if outType.Implements(responderType) {
		capabilities |= outputValueResponder
	}
	return capabilities
}

func writeOutput[Out any](defaultStatus int, produces []string, logger *slog.Logger) func(http.ResponseWriter, *http.Request, Out) {
	capabilities := analyzeOutputCapabilities[Out]()
	bodyless := isBodylessStatus(defaultStatus)
	hasHeaders := capabilities&(outputPointerHeaderer|outputValueHeaderer) != 0
	hasResponder := capabilities&(outputPointerResponder|outputValueResponder) != 0
	return func(w http.ResponseWriter, req *http.Request, out Out) {
		var headers http.Header
		if hasHeaders {
			headers = outputHeaders(&out, capabilities)
			if err := validateHeaders(headers); err != nil {
				if logger != nil {
					logger.Error("invalid response headers",
						"method", req.Method, "path", req.URL.Path, "error", err)
				}
				WriteProblem(w, req, Internal(err), nil)
				return
			}
		}
		if hasResponder {
			if responder, ok := outputResponder(&out, capabilities); ok {
				if isNilInterface(responder) {
					WriteProblem(w, req, Internal(errors.New("typed responder output is nil")), logger.Error)
					return
				}
				writeResponder(w, req, responder, produces, headers, logger)
				return
			}
		}

		if bodyless {
			applyHeaders(w.Header(), headers)
			setNoSniffHeader(w.Header())
			w.WriteHeader(defaultStatus)
			return
		}

		body, err := jsonx.MarshalResponse(&out)
		if err != nil {
			if logger != nil {
				logger.Error("encode JSON response",
					"method", req.Method, "path", req.URL.Path, "error", err)
			}
			WriteProblem(w, req, Internal(err), nil)
			return
		}

		applyHeaders(w.Header(), headers)
		setJSONHeaders(w.Header())
		w.WriteHeader(defaultStatus)
		if err := writeBody(w, body); err != nil && logger != nil {
			logger.Error("write JSON response",
				"method", req.Method, "path", req.URL.Path, "error", err)
		}
	}
}

func outputHeaders[Out any](out *Out, capabilities outputCapabilities) http.Header {
	var headerer Headerer
	var ok bool
	switch {
	case capabilities&outputPointerHeaderer != 0:
		headerer, ok = any(out).(Headerer)
	case capabilities&outputValueHeaderer != 0:
		headerer, ok = any(*out).(Headerer)
	}
	if !ok || isNilInterface(headerer) {
		return nil
	}
	return headerer.ResponseHeaders()
}

func outputResponder[Out any](out *Out, capabilities outputCapabilities) (Responder, bool) {
	switch {
	case capabilities&outputPointerResponder != 0:
		responder, ok := any(out).(Responder)
		return responder, ok
	case capabilities&outputValueResponder != 0:
		responder, ok := any(*out).(Responder)
		return responder, ok
	default:
		return nil, false
	}
}

func applyHeaders(dst, src http.Header) {
	for key, values := range src {
		dst[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
}

func validateHeaders(headers http.Header) error {
	seen := make(map[string]string, len(headers))
	for key, values := range headers {
		if !validHeaderName(key) {
			return fmt.Errorf("invalid response header name %q", key)
		}
		canonical := http.CanonicalHeaderKey(key)
		if reservedResponseHeader(canonical) {
			return fmt.Errorf("response header %q is controlled by the HTTP server", canonical)
		}
		if len(values) == 0 {
			return fmt.Errorf("response header %q has no values", key)
		}
		if previous, ok := seen[canonical]; ok {
			return fmt.Errorf("duplicate response header %q also written as %q", canonical, previous)
		}
		seen[canonical] = key
		for _, value := range values {
			if !validHeaderValue(value) {
				return fmt.Errorf("invalid value for response header %q", canonical)
			}
		}
	}
	return nil
}

func reservedResponseHeader(name string) bool {
	switch name {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length":
		return true
	default:
		return false
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch ch := name[i]; {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '!', ch == '#', ch == '$', ch == '%', ch == '&', ch == '\'', ch == '*', ch == '+', ch == '-', ch == '.', ch == '^', ch == '_', ch == '`', ch == '|', ch == '~':
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if ch := value[i]; ch < ' ' && ch != '\t' || ch == 0x7f {
			return false
		}
	}
	return true
}

func resetHeaders(dst, src http.Header) {
	clear(dst)
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

func isBodylessStatus(status int) bool {
	switch {
	case status >= 100 && status < 200:
		return true
	case status == http.StatusNoContent, status == http.StatusResetContent, status == http.StatusNotModified:
		return true
	default:
		return false
	}
}

func writeResponder(w http.ResponseWriter, req *http.Request, responder Responder, produces []string, headers http.Header, logger *slog.Logger) {
	baseHeaders := w.Header().Clone()
	applyHeaders(w.Header(), headers)
	setNoSniffHeader(w.Header())

	guard := &contentTypeGuard{w: w, req: req, produces: produces, logger: logger, baseHeaders: baseHeaders}
	if err := responder.Respond(guard, req); err != nil {
		if !guard.decided {
			guard.fail(err, "responder failed")
		} else if !guard.failed && logger != nil {
			logger.Error("responder failed after response started",
				"method", req.Method, "path", req.URL.Path, "error", err)
		}
		return
	}
	if !guard.decided {
		guard.WriteHeader(http.StatusOK)
	}
}

func decodeErrorMessage(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var syntaxV2 *jsontext.SyntacticError
	var semanticV2 *jsonv2.SemanticError
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return "request body is required"
	case errors.As(err, &maxBytesErr):
		return fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit)
	case errors.Is(err, jsonx.ErrNull):
		return "must not be null"
	case errors.Is(err, jsonx.ErrMultipleValues):
		return "must contain exactly one JSON value"
	case errors.Is(err, jsontext.ErrDuplicateName):
		return "must not contain duplicate object properties"
	case errors.Is(err, jsonv2.ErrUnknownName):
		return "contains an unknown property"
	case errors.As(err, &syntax):
		return "malformed JSON"
	case errors.As(err, &syntaxV2):
		return "malformed JSON"
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %s must be %s", typeErr.Field, typeErr.Type)
	case errors.As(err, &semanticV2):
		if pointer := string(semanticV2.JSONPointer); pointer != "" {
			if semanticV2.GoType != nil {
				return fmt.Sprintf("property %s must be %s", pointer, semanticV2.GoType)
			}
			return fmt.Sprintf("property %s has an invalid value", pointer)
		}
		return "JSON value has an invalid type"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of JSON input"
	default:
		return err.Error()
	}
}

func validationOrHTTP(err error) error {
	var httpErr *HTTPError
	var validationErr *ValidationError
	if errors.As(err, &httpErr) || errors.As(err, &validationErr) {
		return err
	}
	return Unprocessable(err.Error())
}

func operationID(method, pattern string) string {
	replacer := strings.NewReplacer("{", "by-", "}", "", "/", "-", "_", "-")
	return strings.Trim(strings.ToLower(method)+"-"+replacer.Replace(pattern), "-")
}

type operationContextKey struct{}

// OperationFromContext returns read-only metadata for the matched route. The
// router attaches it before application middleware runs.
func OperationFromContext(ctx context.Context) (OperationMeta, bool) {
	meta, ok := ctx.Value(operationContextKey{}).(OperationMeta)
	return meta, ok
}

func inputPermissions(t reflect.Type) []string {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !isMetaField(field) {
			continue
		}
		value, ok := field.Tag.Lookup("authz")
		if !ok {
			continue
		}
		return normalizePermissions(strings.Split(value, ","), "authz tag on "+typeLabel(t))
	}
	return nil
}

func normalizePermissions(values []string, source string) []string {
	if len(values) == 0 {
		return nil
	}
	permissions := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		permission := strings.TrimSpace(value)
		if permission == "" {
			panic(fmt.Sprintf("kernel: %s contains an empty permission", source))
		}
		if !validPermission(permission) {
			panic(fmt.Sprintf("kernel: %s has invalid permission %q", source, permission))
		}
		if seen[permission] {
			continue
		}
		seen[permission] = true
		permissions = append(permissions, permission)
	}
	return permissions
}

func validPermission(permission string) bool {
	if permission == "" || len(permission) > 128 {
		return false
	}
	for i := 0; i < len(permission); i++ {
		char := permission[i]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '-', '_', '.', ':', '/', '*':
			continue
		default:
			return false
		}
	}
	return true
}
