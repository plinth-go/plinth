package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
)

type validInput struct {
	Meta `route:"GET /items/{id}"`
	ID   string `path:"id" validate:"required"`
}

type validOutput struct {
	ID string `json:"id"`
}

type responderOutput struct{}

func (responderOutput) Respond(http.ResponseWriter, *http.Request) error { return nil }

type nonResponderCSVOutput struct {
	Meta `produces:"text/csv"`
	Rows []string `json:"rows"`
}

type createdResponderOutput struct {
	Meta `produces:"text/csv" status:"201"`
}

func (createdResponderOutput) Respond(http.ResponseWriter, *http.Request) error { return nil }

type informationalOutput struct {
	Meta `status:"103"`
}

type encodedParameter struct {
	Value string
}

func (p *encodedParameter) UnmarshalText(data []byte) error {
	p.Value = "decoded:" + string(data)
	return nil
}

type encodedSliceParameter []string

func (p *encodedSliceParameter) UnmarshalText(data []byte) error {
	*p = strings.Split(string(data), ",")
	return nil
}

func TestValidateAcceptsCanonicalRoutes(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Handle(func(_ context.Context, in validInput) (validOutput, error) {
		return validOutput{ID: in.ID}, nil
	})
	router.Raw("GET", "/events", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Produces("text/event-stream"))
	router.Raw("GET", "/internal", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden())

	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsVisibleRawWithoutResponseMetadata(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Raw("GET", "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "visible raw route") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsDuplicateOperationIDs(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Raw("GET", "/first", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden(), OperationID("duplicate"))
	router.Raw("GET", "/second", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden(), OperationID("duplicate"))

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsPathPlaceholderMismatch(t *testing.T) {
	type input struct {
		Meta `route:"GET /items/{id}"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "path placeholders") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsInvalidProduces(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Raw("GET", "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Produces("invalid/type; charset"))

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid produces media type") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsResponderWithoutProduces(t *testing.T) {
	type input struct {
		Meta `route:"GET /download"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (responderOutput, error) {
		return responderOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "Responder output but no produces metadata") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsProducesWithoutResponder(t *testing.T) {
	type input struct {
		Meta `route:"GET /export"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (nonResponderCSVOutput, error) {
		return nonResponderCSVOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not implement Responder") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsResponderWithNonOKStatus(t *testing.T) {
	type input struct {
		Meta `route:"POST /export"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (createdResponderOutput, error) {
		return createdResponderOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "Responder output with status 201") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsTypedInformationalStatus(t *testing.T) {
	type input struct {
		Meta `route:"GET /early-hints"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (informationalOutput, error) {
		return informationalOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "informational status 103") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsInvalidResponseStatus(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	router.Raw("GET", "/raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Hidden(), Status(99))

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid response status 99") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsDuplicateParameters(t *testing.T) {
	type input struct {
		Meta  `route:"GET /search"`
		First string `query:"q"`
		Last  string `query:"q"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate query parameter") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsCaseInsensitiveDuplicateHeaders(t *testing.T) {
	type input struct {
		Meta  `route:"GET /headers"`
		First string `header:"X-Token"`
		Last  string `header:"x-token"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate header parameter") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsInvalidHeaderName(t *testing.T) {
	type input struct {
		Meta  `route:"GET /headers"`
		Token string `header:"Bad Header"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid header parameter name") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsUnknownValidationTag(t *testing.T) {
	type body struct {
		Email string `json:"email" validate:"required,emial"`
	}
	type input struct {
		Meta `route:"POST /items"`
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "emial") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckSchemaRuleSemantics(t *testing.T) {
	valid := []struct {
		name string
		typ  reflect.Type
		tag  string
	}{
		{name: "integer bounds", typ: reflect.TypeFor[int](), tag: "min=-1,max=10,gt=0,lt=10,len=2"},
		{name: "duration", typ: reflect.TypeFor[time.Duration](), tag: "omitempty,min=1s,max=2h,eq=500ms"},
		{name: "float", typ: reflect.TypeFor[float64](), tag: "min=1.5,max=2e3,len=1.5,eq=1.5,ne=-1"},
		{name: "string oneof", typ: reflect.TypeFor[string](), tag: "oneof=json csv 'tab separated'"},
		{name: "integer oneof", typ: reflect.TypeFor[int](), tag: "oneof=-1 0 2"},
		{name: "slice dive", typ: reflect.TypeFor[[]string](), tag: "min=1,max=3,dive,min=2"},
		{name: "map keys", typ: reflect.TypeFor[map[string]int](), tag: "dive,keys,min=2,endkeys,min=0"},
		{name: "boolean equality", typ: reflect.TypeFor[bool](), tag: "eq=true,ne=false"},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkSchemaRuleSemantics(tc.typ, tc.tag); err != nil {
				t.Fatalf("valid rule rejected: %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		typ  reflect.Type
		tag  string
		want string
	}{
		{name: "malformed min", typ: reflect.TypeFor[int](), tag: "omitempty,min=abc", want: "min=abc"},
		{name: "malformed eq", typ: reflect.TypeFor[int](), tag: "omitempty,eq=abc", want: "eq=abc"},
		{name: "partial integer oneof", typ: reflect.TypeFor[int](), tag: "omitempty,oneof=1 abc", want: "oneof"},
		{name: "float oneof", typ: reflect.TypeFor[float64](), tag: "omitempty,oneof=1 2", want: "oneof is not supported"},
		{name: "decimal length", typ: reflect.TypeFor[string](), tag: "omitempty,min=1.5", want: "base-10 integer"},
		{name: "hex length", typ: reflect.TypeFor[string](), tag: "omitempty,min=0x10", want: "base-10 integer"},
		{name: "leading zero length", typ: reflect.TypeFor[string](), tag: "omitempty,min=01", want: "base-10 integer"},
		{name: "negative length", typ: reflect.TypeFor[string](), tag: "omitempty,min=-1", want: "non-negative"},
		{name: "missing parameter", typ: reflect.TypeFor[string](), tag: "min", want: "requires a parameter"},
		{name: "contains non-string", typ: reflect.TypeFor[int](), tag: "contains=1", want: "only supported on strings"},
		{name: "keys on slice", typ: reflect.TypeFor[[]string](), tag: "dive,keys,min=1,endkeys", want: "only supported on map values"},
		{name: "integer map keys", typ: reflect.TypeFor[map[int]string](), tag: "dive,keys,min=1,endkeys", want: "require string keys"},
		{name: "NaN bound", typ: reflect.TypeFor[float64](), tag: "min=NaN", want: "finite number"},
		{name: "infinite bound", typ: reflect.TypeFor[float64](), tag: "max=+Inf", want: "finite number"},
		{name: "boolean oneof", typ: reflect.TypeFor[bool](), tag: "oneof=true false", want: "oneof is not supported"},
		{name: "malformed duration", typ: reflect.TypeFor[time.Duration](), tag: "min=later", want: "Go duration"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSchemaRuleSemantics(tc.typ, tc.tag)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsMalformedSchemaRuleDespiteOmitEmpty(t *testing.T) {
	type body struct {
		Count int `json:"count" validate:"omitempty,eq=abc"`
	}
	type input struct {
		Meta `route:"POST /items"`
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "eq=abc") || !strings.Contains(err.Error(), "base-10 integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckInputFieldsParameterTypes(t *testing.T) {
	structWith := func(tag string, typ reflect.Type) reflect.Type {
		return reflect.StructOf([]reflect.StructField{{
			Name: "Value",
			Type: typ,
			Tag:  reflect.StructTag(tag),
		}})
	}

	valid := []struct {
		name string
		typ  reflect.Type
	}{
		{name: "query slice", typ: structWith(`query:"value"`, reflect.TypeFor[[]string]())},
		{name: "query pointer slice", typ: structWith(`query:"value"`, reflect.TypeFor[*[]int]())},
		{name: "query duration slice", typ: structWith(`query:"value"`, reflect.TypeFor[[]time.Duration]())},
		{name: "query text unmarshaler slice", typ: structWith(`query:"value"`, reflect.TypeFor[[]encodedParameter]())},
		{name: "query text unmarshaler", typ: structWith(`query:"value"`, reflect.TypeFor[encodedParameter]())},
		{name: "query text unmarshaler collection", typ: structWith(`query:"value"`, reflect.TypeFor[encodedSliceParameter]())},
		{name: "header text unmarshaler", typ: structWith(`header:"X-Value"`, reflect.TypeFor[encodedParameter]())},
		{name: "path time", typ: structWith(`path:"value"`, reflect.TypeFor[time.Time]())},
		{name: "body struct", typ: structWith(`body:",required"`, reflect.TypeFor[struct{ Value string }]())},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkInputFields(tc.typ); err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{name: "path slice", typ: structWith(`path:"value"`, reflect.TypeFor[[]string]()), want: "path parameter"},
		{name: "header slice", typ: structWith(`header:"X-Value"`, reflect.TypeFor[[]string]()), want: "header parameter"},
		{name: "query array", typ: structWith(`query:"value"`, reflect.TypeFor[[2]string]()), want: "query parameter"},
		{name: "query map", typ: structWith(`query:"value"`, reflect.TypeFor[map[string]string]()), want: "query parameter"},
		{name: "query struct", typ: structWith(`query:"value"`, reflect.TypeFor[struct{ Value string }]()), want: "query parameter"},
		{name: "raw message", typ: structWith(`query:"value"`, reflect.TypeFor[json.RawMessage]()), want: "not supported"},
		{name: "pointer raw message", typ: structWith(`query:"value"`, reflect.TypeFor[*json.RawMessage]()), want: "not supported"},
		{name: "nested query slice", typ: structWith(`query:"value"`, reflect.TypeFor[[][]string]()), want: "slice elements"},
		{name: "multiple locations", typ: structWith(`path:"value" query:"value"`, reflect.TypeFor[string]()), want: "multiple parameter locations"},
		{name: "missing location", typ: structWith("", reflect.TypeFor[string]()), want: "must have one of"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInputFields(tc.typ)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsUnsupportedParameterType(t *testing.T) {
	type input struct {
		Meta `route:"GET /items/{tags}"`
		Tags []string `path:"tags"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "path parameter") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsInvalidDiveTag(t *testing.T) {
	type body struct {
		Name string `json:"name" validate:"dive"`
	}
	type input struct {
		Meta `route:"POST /items"`
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "dive") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsRequiredWithOmitRule(t *testing.T) {
	for _, tag := range []string{
		"omitempty,required",
		"required,omitempty",
		"dive,omitzero,required",
		"required,dive,required,omitnil",
	} {
		t.Run(tag, func(t *testing.T) {
			if err := checkValidationSemantics(tag); err == nil {
				t.Fatal("ambiguous validate tag was accepted")
			}
		})
	}
	if err := checkValidationSemantics("required,dive,omitempty"); err != nil {
		t.Fatalf("requirements on different dive levels were rejected: %v", err)
	}
}

func TestValidateReportsAmbiguousRequiredTag(t *testing.T) {
	type body struct {
		Value string `json:"value" validate:"omitempty,required"`
	}
	type input struct {
		Meta `route:"POST /items"`
		Body body `body:",required"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "combines required with omitempty") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsDefaultThatViolatesRules(t *testing.T) {
	type input struct {
		Meta  `route:"GET /items"`
		Limit int `query:"limit" default:"0" validate:"min=1"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), `default "0"`) || !strings.Contains(err.Error(), "min=1") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateChecksDefaultsWithoutOmissionRules(t *testing.T) {
	type input struct {
		Meta      `route:"GET /items"`
		OmitEmpty int `query:"omitempty" default:"0" validate:"omitempty,min=1"`
		OmitZero  int `query:"omit-zero" default:"0" validate:"omitzero,min=1"`
		OmitNil   int `query:"omit-nil" default:"0" validate:"omitnil,min=1"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "after defaulting") {
		t.Fatalf("error = %v", err)
	}
	for _, field := range []string{"omitempty", "omit-zero", "omit-nil"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("error does not report field %q: %v", field, err)
		}
	}
}

func TestValidateAcceptsDefaultThatSatisfiesRulesWithoutOmission(t *testing.T) {
	type input struct {
		Meta  `route:"GET /items"`
		Limit int `query:"limit" default:"2" validate:"omitempty,min=1"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStripOmissionRulesPreservesRuleParameters(t *testing.T) {
	got := stripOmissionRules("omitempty,eq=omitempty,dive,omitzero,min=1")
	if want := "eq=omitempty,dive,min=1"; got != want {
		t.Fatalf("stripOmissionRules = %q, want %q", got, want)
	}
}

func TestValidateAcceptsDefaultThatSatisfiesRules(t *testing.T) {
	type input struct {
		Meta   `route:"GET /items"`
		Limit  int    `query:"limit" default:"10" validate:"min=1,max=100"`
		Format string `query:"format" default:"json" validate:"oneof=json csv"`
	}
	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateChecksDefaultWithCustomRule(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	if err := router.RegisterValidation("even", func(field validator.FieldLevel) bool {
		return field.Field().Int()%2 == 0
	}); err != nil {
		t.Fatal(err)
	}
	type input struct {
		Meta   `route:"GET /items"`
		Number int `query:"number" default:"3" validate:"even"`
	}
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), `default "3"`) || !strings.Contains(err.Error(), "even") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateReportsPanickingCustomRule(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	if err := router.RegisterValidation("explode", func(validator.FieldLevel) bool {
		panic("custom rule boom")
	}); err != nil {
		t.Fatal(err)
	}
	type input struct {
		Meta  `route:"GET /items"`
		Value string `query:"value" validate:"explode"`
	}
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), "validator panicked") || !strings.Contains(err.Error(), "custom rule boom") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateReportsPanickingDefaultRule(t *testing.T) {
	router := NewRouter(nil, "test", "1")
	if err := router.RegisterValidation("explode", func(validator.FieldLevel) bool {
		panic("default rule boom")
	}); err != nil {
		t.Fatal(err)
	}
	type input struct {
		Meta  `route:"GET /items"`
		Value string `query:"value" default:"default" validate:"explode"`
	}
	router.Handle(func(context.Context, input) (validOutput, error) {
		return validOutput{}, nil
	})

	err := router.Validate()
	if err == nil || !strings.Contains(err.Error(), `default "default"`) || !strings.Contains(err.Error(), "validator panicked") {
		t.Fatalf("error = %v", err)
	}
}
