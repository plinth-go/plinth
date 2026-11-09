package kernel

import (
	"context"
	"encoding/json"
	"net/netip"
	"reflect"
	"regexp"
	"testing"
	"time"
)

type textWire struct {
	Value string
}

func (textWire) MarshalText() ([]byte, error) { return []byte("wire"), nil }
func (*textWire) UnmarshalText([]byte) error  { return nil }

type jsonWire struct {
	Value string
}

func (jsonWire) MarshalJSON() ([]byte, error) { return []byte(`"wire"`), nil }
func (*jsonWire) UnmarshalJSON([]byte) error  { return nil }

type marshaledOnlyMeta struct {
	Meta
}

func (marshaledOnlyMeta) MarshalJSON() ([]byte, error) { return []byte(`"wire"`), nil }

type pointerWire struct {
	Value string `json:"value"`
}

func (*pointerWire) MarshalJSON() ([]byte, error) { return []byte(`"wire"`), nil }

func TestSchemaRequiredUsesValidateOnly(t *testing.T) {
	type body struct {
		Required string  `json:"required,omitempty" validate:"required"`
		Optional string  `json:"optional,omitempty"`
		Pointer  *string `json:"pointer"`
	}

	schema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	required, _ := schema["required"].([]string)
	if !reflect.DeepEqual(required, []string{"required"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestSchemaResponseRequiredUsesJSONOmit(t *testing.T) {
	type body struct {
		Present       string `json:"present"`
		OmitEmpty     string `json:"omit_empty,omitempty"`
		OmitZero      string `json:"omit_zero,omitzero"`
		ValidatorRule string `json:"validator_rule,omitempty" validate:"required"`
		NotSerialized string `json:"-"`
	}

	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	required, _ := schema["required"].([]string)
	if !reflect.DeepEqual(required, []string{"present"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestSchemaResponseRequiredMatchesEncodingJSONOmitEmpty(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type body struct {
		StructOmitEmpty nested    `json:"struct_omit_empty,omitempty"`
		StructOmitZero  nested    `json:"struct_omit_zero,omitzero"`
		TimeOmitEmpty   time.Time `json:"time_omit_empty,omitempty"`
		TimeOmitZero    time.Time `json:"time_omit_zero,omitzero"`
		Pointer         *nested   `json:"pointer,omitempty"`
	}

	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	required, _ := schema["required"].([]string)
	want := []string{"struct_omit_empty", "time_omit_empty"}
	if !reflect.DeepEqual(required, want) {
		t.Fatalf("required = %#v, want %#v", required, want)
	}
}

func TestSchemaStructsDisallowUnknownProperties(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type body struct {
		Nested nested `json:"nested"`
	}

	schema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %#v", schema["additionalProperties"])
	}
	nestedSchema := schemaProperties(t, schema)["nested"].(map[string]any)
	if nestedSchema["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %#v", nestedSchema["additionalProperties"])
	}
}

func TestSchemaUsesTextWireRepresentation(t *testing.T) {
	type body struct {
		Value textWire   `json:"value"`
		Addr  netip.Addr `json:"addr"`
	}
	for name, mode := range map[string]schemaMode{
		"request":  schemaRequest,
		"response": schemaResponse,
	} {
		t.Run(name, func(t *testing.T) {
			schema := newSchemaBuilder(nil, mode).schema(reflect.TypeFor[body](), reflect.StructTag(""))
			properties := schemaProperties(t, schema)
			for _, field := range []string{"value", "addr"} {
				prop := properties[field].(map[string]any)
				if prop["type"] != "string" {
					t.Fatalf("%s schema = %#v", field, prop)
				}
			}
		})
	}
}

func TestSchemaJSONNumberUsesNumberWireType(t *testing.T) {
	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[json.Number](), reflect.StructTag(""))
	if schema["type"] != "number" {
		t.Fatalf("json.Number schema = %#v", schema)
	}
	parameter := newSchemaBuilder(nil, schemaParameter).schema(reflect.TypeFor[json.Number](), reflect.StructTag(""))
	if parameter["type"] != "string" {
		t.Fatalf("json.Number parameter schema = %#v", parameter)
	}
}

func TestSchemaCustomJSONIsOpaque(t *testing.T) {
	type body struct {
		Value jsonWire `json:"value"`
	}
	for name, mode := range map[string]schemaMode{
		"request":  schemaRequest,
		"response": schemaResponse,
	} {
		t.Run(name, func(t *testing.T) {
			schema := newSchemaBuilder(nil, mode).schema(reflect.TypeFor[body](), reflect.StructTag(""))
			prop := schemaProperties(t, schema)["value"].(map[string]any)
			if len(prop) != 0 {
				t.Fatalf("custom JSON schema = %#v, want an unconstrained schema", prop)
			}
		})
	}
}

func TestSchemaPointerMarshalerFollowsEncodingAddressability(t *testing.T) {
	type body struct {
		Slice []pointerWire          `json:"slice"`
		Array [1]pointerWire         `json:"array"`
		Map   map[string]pointerWire `json:"map"`
	}
	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	properties := schemaProperties(t, schema)

	sliceItems := properties["slice"].(map[string]any)["items"].(map[string]any)
	if len(sliceItems) != 0 {
		t.Fatalf("slice item schema = %#v, want the pointer marshaler shape", sliceItems)
	}
	mapValues := properties["map"].(map[string]any)["additionalProperties"].(map[string]any)
	if mapValues["type"] != "object" {
		t.Fatalf("map value schema uses pointer marshaler: %#v", mapValues)
	}
	arrayItems := properties["array"].(map[string]any)["items"].(map[string]any)
	if arrayItems["type"] != "object" {
		t.Fatalf("array item schema uses pointer marshaler: %#v", arrayItems)
	}

	addressable := newSchemaBuilder(nil, schemaResponse).withAddressable(true).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	arrayItems = schemaProperties(t, addressable)["array"].(map[string]any)["items"].(map[string]any)
	if len(arrayItems) != 0 {
		t.Fatalf("addressable array item schema = %#v, want the pointer marshaler shape", arrayItems)
	}
}

func TestSchemaExamplesUseWireTypes(t *testing.T) {
	type body struct {
		Name    string            `json:"name" example:"hello"`
		Count   int               `json:"count" example:"42"`
		Offset  uint              `json:"offset" example:"7"`
		Ratio   float64           `json:"ratio" example:"1.5"`
		Enabled bool              `json:"enabled" example:"true"`
		Tags    []string          `json:"tags" example:"[1,2]"`
		Labels  map[string]string `json:"labels" example:"{\"k\":1}"`
		Number  json.Number       `json:"number" example:"1.25"`
		Text    textWire          `json:"text" example:"wire-example"`
		When    time.Time         `json:"when" example:"2026-11-07T10:00:00Z"`
	}

	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[body](), reflect.StructTag(""))
	properties := schemaProperties(t, schema)
	expectExample := func(field string, want any) {
		t.Helper()
		prop := properties[field].(map[string]any)
		if !reflect.DeepEqual(prop["example"], want) {
			t.Fatalf("%s example = %#v, want %#v", field, prop["example"], want)
		}
	}
	expectExample("name", "hello")
	expectExample("count", int64(42))
	expectExample("offset", uint64(7))
	expectExample("ratio", 1.5)
	expectExample("enabled", true)
	expectExample("tags", []any{json.Number("1"), json.Number("2")})
	expectExample("labels", map[string]any{"k": json.Number("1")})
	expectExample("number", json.Number("1.25"))
	expectExample("text", "wire-example")
	expectExample("when", "2026-11-07T10:00:00Z")
}

func TestSchemaExamplesValidateWireType(t *testing.T) {
	tests := []struct {
		name      string
		fieldType reflect.Type
		example   string
	}{
		{name: "integer", fieldType: reflect.TypeFor[int](), example: "1.5"},
		{name: "unsigned", fieldType: reflect.TypeFor[uint](), example: "-1"},
		{name: "boolean", fieldType: reflect.TypeFor[bool](), example: "yes"},
		{name: "number", fieldType: reflect.TypeFor[float64](), example: `"1.5"`},
		{name: "array", fieldType: reflect.TypeFor[[]string](), example: "{}"},
		{name: "object", fieldType: reflect.TypeFor[map[string]string](), example: "[]"},
		{name: "time", fieldType: reflect.TypeFor[time.Time](), example: "tomorrow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tag := reflect.StructTag(`example:"` + test.example + `"`)
			assertPanicsWith(t, "invalid example", func() {
				newSchemaBuilder(nil, schemaRequest).schema(test.fieldType, tag)
			})
		})
	}

	duration := newSchemaBuilder(nil, schemaParameter).schema(
		reflect.TypeFor[time.Duration](),
		reflect.StructTag(`example:"1m"`),
	)
	if duration["example"] != "1m" {
		t.Fatalf("duration example = %#v", duration["example"])
	}
	assertPanicsWith(t, "invalid example", func() {
		newSchemaBuilder(nil, schemaParameter).schema(
			reflect.TypeFor[time.Duration](),
			reflect.StructTag(`example:"soon"`),
		)
	})
}

func TestSchemaRejectsUnrepresentableJSONTypes(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[chan int](),
		reflect.TypeFor[func()](),
		reflect.TypeFor[complex128](),
	} {
		assertPanicsWith(t, "cannot be represented in JSON", func() {
			newSchemaBuilder(nil, schemaRequest).schema(typ, reflect.StructTag(""))
		})
	}
}

func TestSchemaUintptrUsesIntegerWireType(t *testing.T) {
	schema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[uintptr](), reflect.StructTag(""))
	if schema["type"] != "integer" || schema["minimum"] != 0 {
		t.Fatalf("uintptr schema = %#v", schema)
	}
}

func TestCustomMarshaledOutputIsNotBodyless(t *testing.T) {
	schema, empty := analyzeOutput[marshaledOnlyMeta](nil)
	if empty || schema == nil {
		t.Fatalf("custom marshaled output treated as bodyless: schema=%#v empty=%v", schema, empty)
	}
}

func TestSchemaRejectsUnsupportedJSONOptions(t *testing.T) {
	for _, tag := range []string{`json:"value,string"`, `json:"value,omitempty,string"`, `json:"value,typographical"`} {
		t.Run(tag, func(t *testing.T) {
			assertPanicsWith(t, "json tag option", func() {
				bodyType := reflect.StructOf([]reflect.StructField{{
					Name: "Value",
					Type: reflect.TypeFor[string](),
					Tag:  reflect.StructTag(tag),
				}})
				newSchemaBuilder(nil, schemaRequest).schema(bodyType, reflect.StructTag(""))
			})
		})
	}
}

func TestSchemaRejectsUnnamedAnonymousFields(t *testing.T) {
	type Base struct {
		ID string `json:"id"`
	}
	type embedded struct {
		Base
	}
	assertPanicsWith(t, "embedded struct fields are not supported", func() {
		newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[embedded](), reflect.StructTag(""))
	})

	type named struct {
		Base `json:"base"`
	}
	schema := newSchemaBuilder(nil, schemaResponse).schema(reflect.TypeFor[named](), reflect.StructTag(""))
	properties := schemaProperties(t, schema)
	if _, ok := properties["base"].(map[string]any); !ok {
		t.Fatalf("base property = %#v", properties["base"])
	}
}

func TestSchemaRecursiveCompositesUseGenericPlaceholders(t *testing.T) {
	type node struct {
		Next *node `json:"next"`
	}
	nodeSchema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[node](), reflect.StructTag(""))
	next, ok := schemaProperties(t, nodeSchema)["next"].(map[string]any)
	if !ok || next["type"] != "object" {
		t.Fatalf("recursive struct schema = %#v", next)
	}

	type list []list
	listSchema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[list](), reflect.StructTag(""))
	items, ok := listSchema["items"].(map[string]any)
	if !ok || items["type"] != "array" {
		t.Fatalf("recursive slice schema = %#v", listSchema)
	}

	type lookup map[string]lookup
	mapSchema := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[lookup](), reflect.StructTag(""))
	values, ok := mapSchema["additionalProperties"].(map[string]any)
	if !ok || values["type"] != "object" {
		t.Fatalf("recursive map schema = %#v", mapSchema)
	}
}

func TestSchemaRejectsRecursivePointerAlias(t *testing.T) {
	type recursivePointer *recursivePointer
	assertPanicsWith(t, "recursive pointer type", func() {
		newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[recursivePointer](), reflect.StructTag(""))
	})
	assertPanicsWith(t, "recursive pointer type", func() {
		responseTags[recursivePointer]()
	})
	assertPanicsWith(t, "recursive pointer type", func() {
		analyzeOutput[recursivePointer](nil)
	})
}

func TestOpenAPIRecursiveOutputUsesPlaceholder(t *testing.T) {
	type node struct {
		Value string `json:"value"`
		Next  *node  `json:"next"`
	}
	type input struct {
		Meta `route:"GET /tree"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (node, error) {
		return node{}, nil
	})
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/tree"]["get"]
	schema := operation.Responses["200"].Content["application/json"].Schema
	next, ok := schemaProperties(t, schema)["next"].(map[string]any)
	if !ok || next["type"] != "object" {
		t.Fatalf("next schema = %#v", next)
	}
}

func TestOpenAPIResponseRequiredUsesJSONOmit(t *testing.T) {
	type input struct {
		Meta `route:"GET /"`
	}
	type output struct {
		Present       string `json:"present"`
		Optional      string `json:"optional,omitempty"`
		ValidatorRule string `json:"validator_rule,omitempty" validate:"required"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (output, error) {
		return output{}, nil
	})
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/"]["get"]
	schema := operation.Responses["200"].Content["application/json"].Schema
	required, _ := schema["required"].([]any)
	if !reflect.DeepEqual(required, []any{"present"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestOpenAPIRequiredUsesValidateOnly(t *testing.T) {
	type body struct {
		Required string `json:"required,omitempty" validate:"required"`
		Optional string `json:"optional,omitempty"`
	}
	type input struct {
		Meta `route:"POST /items"`
		Body body `body:",required"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (map[string]string, error) {
		return map[string]string{"ok": "true"}, nil
	})
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/items"]["post"]
	if operation.RequestBody == nil {
		t.Fatal("request body missing")
	}
	schema := operation.RequestBody.Content["application/json"].Schema
	required, _ := schema["required"].([]any)
	if !reflect.DeepEqual(required, []any{"required"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestSchemaAppliesMostRestrictiveBounds(t *testing.T) {
	type body struct {
		Text      string  `json:"text" validate:"min=2,max=8,gte=3,lte=6"`
		Exact     string  `json:"exact" validate:"min=2,max=8,len=4"`
		Count     int     `json:"count" validate:"min=-5,max=10,gte=-4,lte=9"`
		Ratio     float64 `json:"ratio" validate:"len=1.5"`
		Exclusive int     `json:"exclusive" validate:"gt=0,lt=10"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["text"], map[string]any{
		"minLength": 3,
		"maxLength": 6,
	})
	assertSchemaValues(t, properties["exact"], map[string]any{
		"minLength": 4,
		"maxLength": 4,
	})
	assertSchemaValues(t, properties["count"], map[string]any{
		"minimum": float64(-4),
		"maximum": float64(9),
	})
	assertSchemaValues(t, properties["ratio"], map[string]any{
		"minimum": float64(1.5),
		"maximum": float64(1.5),
	})
	assertSchemaValues(t, properties["exclusive"], map[string]any{
		"exclusiveMinimum": float64(0),
		"exclusiveMaximum": float64(10),
	})
}

func TestSchemaTranslatesExclusiveSizeRules(t *testing.T) {
	type body struct {
		Text   string            `json:"text" validate:"min=1,gt=2,max=8,lt=7"`
		Tags   []string          `json:"tags" validate:"gt=1,lt=4"`
		Labels map[string]string `json:"labels" validate:"gt=0,lt=3"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["text"], map[string]any{"minLength": 3, "maxLength": 6})
	assertSchemaValues(t, properties["tags"], map[string]any{"minItems": 2, "maxItems": 3})
	assertSchemaValues(t, properties["labels"], map[string]any{"minProperties": 1, "maxProperties": 2})
}

func TestSchemaMapsUniqueToUniqueItems(t *testing.T) {
	type body struct {
		Values  []string          `json:"values" validate:"unique"`
		Numbers [3]int            `json:"numbers" validate:"unique"`
		Ignored map[string]string `json:"ignored" validate:"unique"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["values"], map[string]any{"uniqueItems": true})
	assertSchemaValues(t, properties["numbers"], map[string]any{"uniqueItems": true})
	if _, ok := properties["ignored"].(map[string]any)["uniqueItems"]; ok {
		t.Fatalf("map schema = %#v", properties["ignored"])
	}
}

func TestSchemaMapsEqualToScalarAndExactSizes(t *testing.T) {
	type body struct {
		Code   string            `json:"code" validate:"eq=ready"`
		Flag   bool              `json:"flag" validate:"eq=true"`
		Count  int               `json:"count" validate:"eq=3"`
		Ratio  float64           `json:"ratio" validate:"eq=1.5"`
		Tags   []string          `json:"tags" validate:"eq=2"`
		Labels map[string]string `json:"labels" validate:"eq=1"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["code"], map[string]any{"const": "ready"})
	assertSchemaValues(t, properties["flag"], map[string]any{"const": true})
	assertSchemaValues(t, properties["count"], map[string]any{"const": int64(3)})
	assertSchemaValues(t, properties["ratio"], map[string]any{"const": float64(1.5)})
	assertSchemaValues(t, properties["tags"], map[string]any{"minItems": 2, "maxItems": 2})
	assertSchemaValues(t, properties["labels"], map[string]any{"minProperties": 1, "maxProperties": 1})
}

func TestSchemaMapsMapKeyEqualToPropertyNameConst(t *testing.T) {
	type body struct {
		Values map[string]string `json:"values" validate:"dive,keys,eq=known,endkeys,min=1"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	values := properties["values"].(map[string]any)
	assertSchemaValues(t, values["propertyNames"], map[string]any{"const": "known"})
	assertSchemaValues(t, values["additionalProperties"], map[string]any{"minLength": 1})
}

func TestSchemaMapsStringPredicatesToPatterns(t *testing.T) {
	type body struct {
		Start    string `json:"start" validate:"startswith=pre"`
		End      string `json:"end" validate:"endswith=.json"`
		Contains string `json:"contains" validate:"contains=a.b"`
		Combined string `json:"combined" validate:"startswith=pre,endswith=.json,contains=needle"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["start"], map[string]any{"pattern": "^pre"})
	assertSchemaValues(t, properties["end"], map[string]any{"pattern": `\.json$`})
	assertSchemaValues(t, properties["contains"], map[string]any{"pattern": `a\.b`})
	wantAllOf := []any{
		map[string]any{"pattern": "^pre"},
		map[string]any{"pattern": `\.json$`},
		map[string]any{"pattern": "needle"},
	}
	if got := properties["combined"].(map[string]any)["allOf"]; !reflect.DeepEqual(got, wantAllOf) {
		t.Fatalf("combined allOf = %#v", got)
	}
}

func TestSchemaDurationUsesParameterWireType(t *testing.T) {
	parameter := newSchemaBuilder(nil, schemaParameter).schema(reflect.TypeFor[time.Duration](), reflect.StructTag(""))
	assertSchemaValues(t, parameter, map[string]any{"type": "string", "pattern": goDurationSyntax})

	body := newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[time.Duration](), reflect.StructTag(""))
	assertSchemaValues(t, body, map[string]any{"type": "integer"})
}

func TestDurationSyntaxMatchesParseDuration(t *testing.T) {
	pattern := regexp.MustCompile(goDurationSyntax)
	valid := []string{
		"0", "+0", "-0", "5s", "-1.5h", "2h45m", "300ms", "1.5h", "1.s", ".5s", "1us", "1µs", "1μs",
	}
	for _, value := range valid {
		if _, err := time.ParseDuration(value); err != nil {
			t.Fatalf("test value %q is not accepted by time.ParseDuration: %v", value, err)
		}
		if !pattern.MatchString(value) {
			t.Errorf("pattern rejects valid Go duration %q", value)
		}
	}

	invalid := []string{"", "5", "1d", "5 s", "s", ".s", "1.5", "1h 30m", "P1D"}
	for _, value := range invalid {
		if _, err := time.ParseDuration(value); err == nil {
			t.Fatalf("test value %q should be rejected by time.ParseDuration", value)
		}
		if pattern.MatchString(value) {
			t.Errorf("pattern accepts invalid Go duration %q", value)
		}
	}
}

func TestOpenAPIDurationParametersUseStringAndRawDefault(t *testing.T) {
	type input struct {
		Meta  `route:"GET /work"`
		Wait  time.Duration   `query:"wait" default:"5s"`
		Waits []time.Duration `query:"waits"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (struct{}, error) {
		return struct{}{}, nil
	})
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/work"]["get"]
	assertSchemaValues(t, operation.Parameters[0].Schema, map[string]any{
		"type":    "string",
		"pattern": goDurationSyntax,
		"default": "5s",
	})
	waits := operation.Parameters[1].Schema
	if waits["type"] != "array" {
		t.Fatalf("waits schema = %#v", waits)
	}
	assertSchemaValues(t, waits["items"], map[string]any{"type": "string", "pattern": goDurationSyntax})
}

func TestSchemaTextUnmarshalerParametersUseStringWireType(t *testing.T) {
	type input struct {
		Meta   `route:"GET /encoded"`
		Mode   encodedParameter      `query:"mode"`
		Modes  []encodedParameter    `query:"modes"`
		Values *[]int                `query:"values"`
		CSV    encodedSliceParameter `query:"csv"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (struct{}, error) {
		return struct{}{}, nil
	})
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/encoded"]["get"]
	assertSchemaValues(t, operation.Parameters[0].Schema, map[string]any{"type": "string"})
	modes := operation.Parameters[1].Schema
	if modes["type"] != "array" {
		t.Fatalf("modes schema = %#v", modes)
	}
	assertSchemaValues(t, modes["items"], map[string]any{"type": "string"})
	values := operation.Parameters[2].Schema
	if values["type"] != "array" {
		t.Fatalf("values schema = %#v", values)
	}
	assertSchemaValues(t, values["items"], map[string]any{"type": "integer"})
	assertSchemaValues(t, operation.Parameters[3].Schema, map[string]any{"type": "string"})
}

func TestOpenAPITextDefaultsUseRawWireValue(t *testing.T) {
	type input struct {
		Meta `route:"GET /defaults"`
		Mode encodedParameter `query:"mode" default:"fast"`
		At   time.Time        `query:"at" default:"2026-01-02T03:04:05Z"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (struct{}, error) {
		return struct{}{}, nil
	})
	if err := router.Validate(); err != nil {
		t.Fatal(err)
	}
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/defaults"]["get"]
	assertSchemaValues(t, operation.Parameters[0].Schema, map[string]any{
		"type":    "string",
		"default": "fast",
	})
	assertSchemaValues(t, operation.Parameters[1].Schema, map[string]any{
		"type":    "string",
		"format":  "date-time",
		"default": "2026-01-02T03:04:05Z",
	})
}

func TestSchemaMapsNotEqualToNotConstOrSize(t *testing.T) {
	type body struct {
		Code   string            `json:"code" validate:"ne=bad"`
		Flag   bool              `json:"flag" validate:"ne=true"`
		Count  int               `json:"count" validate:"ne=0"`
		Tags   []string          `json:"tags" validate:"ne=2"`
		Labels map[string]string `json:"labels" validate:"ne=1"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	assertSchemaValues(t, properties["code"], map[string]any{"not": map[string]any{"const": "bad"}})
	assertSchemaValues(t, properties["flag"], map[string]any{"not": map[string]any{"const": true}})
	assertSchemaValues(t, properties["count"], map[string]any{"not": map[string]any{"const": int64(0)}})
	assertSchemaValues(t, properties["tags"], map[string]any{"not": map[string]any{"minItems": 2, "maxItems": 2}})
	assertSchemaValues(t, properties["labels"], map[string]any{"not": map[string]any{"minProperties": 1, "maxProperties": 1}})
}

func TestSchemaMapsNotEqualOnMapKey(t *testing.T) {
	type body struct {
		Values map[string]string `json:"values" validate:"dive,keys,ne=bad,endkeys"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	values := properties["values"].(map[string]any)
	assertSchemaValues(t, values["propertyNames"], map[string]any{"not": map[string]any{"const": "bad"}})
}

func TestSchemaSeparatesContainerAndDiveRules(t *testing.T) {
	type body struct {
		Tags   []string          `json:"tags" validate:"min=2,max=3,dive,min=1,max=24"`
		Labels map[string]string `json:"labels" validate:"min=1,max=4,dive,min=2,max=6"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	tags := properties["tags"].(map[string]any)
	assertSchemaValues(t, tags, map[string]any{"minItems": 2, "maxItems": 3})
	assertSchemaValues(t, tags["items"], map[string]any{"minLength": 1, "maxLength": 24})

	labels := properties["labels"].(map[string]any)
	assertSchemaValues(t, labels, map[string]any{"minProperties": 1, "maxProperties": 4})
	assertSchemaValues(t, labels["additionalProperties"], map[string]any{"minLength": 2, "maxLength": 6})
}

func TestSchemaSeparatesMapKeyAndValueRules(t *testing.T) {
	type body struct {
		Labels map[string]string `json:"labels" validate:"min=1,max=3,dive,keys,min=2,max=8,endkeys,min=1,max=20"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	labels := properties["labels"].(map[string]any)
	assertSchemaValues(t, labels, map[string]any{"minProperties": 1, "maxProperties": 3})
	assertSchemaValues(t, labels["propertyNames"], map[string]any{"minLength": 2, "maxLength": 8})
	assertSchemaValues(t, labels["additionalProperties"], map[string]any{"minLength": 1, "maxLength": 20})
}

func TestSchemaMapKeysWithoutValueRules(t *testing.T) {
	type item struct {
		Value string `json:"value"`
	}
	type body struct {
		Items map[string]item `json:"items" validate:"dive,keys,min=2,endkeys"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	items := properties["items"].(map[string]any)
	assertSchemaValues(t, items["propertyNames"], map[string]any{"minLength": 2})
	valueSchema := items["additionalProperties"].(map[string]any)
	if valueSchema["additionalProperties"] != false {
		t.Fatalf("value schema = %#v", valueSchema)
	}
}

func TestSchemaRejectsUnsupportedMapKeyRules(t *testing.T) {
	type integerKeys struct {
		Values map[int]string `json:"values" validate:"dive,keys,min=1,endkeys"`
	}
	assertPanicsWith(t, "supports string keys only", func() {
		newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[integerKeys](), reflect.StructTag(""))
	})

	type nonMapKeys struct {
		Values []string `json:"values" validate:"dive,keys,min=1,endkeys"`
	}
	assertPanicsWith(t, "only supported on maps", func() {
		newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[nonMapKeys](), reflect.StructTag(""))
	})
}

func TestSchemaOneOfUsesFieldType(t *testing.T) {
	type body struct {
		Format string `json:"format" validate:"oneof=json csv 'tab separated'"`
		Level  int    `json:"level" validate:"oneof=1 2 3"`
	}

	properties := schemaProperties(t, newSchemaBuilder(nil, schemaRequest).schema(reflect.TypeFor[body](), reflect.StructTag("")))
	if got := properties["format"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"json", "csv", "tab separated"}) {
		t.Fatalf("format enum = %#v", got)
	}
	if got := properties["level"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{int64(1), int64(2), int64(3)}) {
		t.Fatalf("level enum = %#v", got)
	}
}

func TestSchemaDiveRequiredDoesNotMakeContainerRequired(t *testing.T) {
	type input struct {
		Meta  `route:"GET /search"`
		Terms []string `query:"term" validate:"dive,required"`
	}

	router := NewRouter(nil, "test", "1")
	router.Handle(func(context.Context, input) (struct{}, error) {
		return struct{}{}, nil
	})
	operation := decodeSpec(t, router.OpenAPIJSON()).Paths["/search"]["get"]
	if operation.Parameters[0].Required {
		t.Fatalf("parameter = %#v", operation.Parameters[0])
	}
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	return properties
}

func assertSchemaValues(t *testing.T, raw any, want map[string]any) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema = %#v", raw)
	}
	for key, expected := range want {
		if got := schema[key]; !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s = %#v, want %#v (schema %#v)", key, got, expected, schema)
		}
	}
}
