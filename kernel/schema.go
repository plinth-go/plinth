package kernel

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	timeType            = reflect.TypeFor[time.Time]()
	durationType        = reflect.TypeFor[time.Duration]()
	rawMessageType      = reflect.TypeFor[json.RawMessage]()
	numberType          = reflect.TypeFor[json.Number]()
	textMarshalerType   = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshalerType   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	oneofPattern        = regexp.MustCompile(`'[^']*'|\S+`)
	goDurationSyntax    = `^[-+]?(?:0|(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:ns|us|µs|μs|ms|s|m|h))+)$`
)

type validationRules struct {
	required bool
	min      *float64
	max      *float64
	length   *float64
	gte      *float64
	lte      *float64
	gt       *float64
	lt       *float64
	oneof    []string
	email    bool
	url      bool
	uuid     bool
	unique   bool
	eq       string
	hasEq    bool
	ne       string
	hasNe    bool
	contains []string
	starts   []string
	ends     []string
}

type schemaMode uint8

const (
	schemaRequest schemaMode = iota
	schemaResponse
	schemaParameter
)

type schemaBuilder struct {
	fieldDocs   map[string]string
	stack       map[reflect.Type]bool
	mode        schemaMode
	addressable bool
}

func newSchemaBuilder(fieldDocs map[string]string, mode schemaMode) *schemaBuilder {
	return &schemaBuilder{
		fieldDocs:   fieldDocs,
		stack:       map[reflect.Type]bool{},
		mode:        mode,
		addressable: mode != schemaResponse,
	}
}

func (b *schemaBuilder) withAddressable(addressable bool) *schemaBuilder {
	clone := *b
	clone.addressable = addressable
	return &clone
}

func indirectType(t reflect.Type) reflect.Type {
	seen := map[reflect.Type]bool{}
	for t.Kind() == reflect.Pointer {
		if seen[t] {
			panic(fmt.Sprintf("kernel: recursive pointer type %s is not supported", t))
		}
		seen[t] = true
		t = t.Elem()
	}
	return t
}

func (b *schemaBuilder) schema(t reflect.Type, tag reflect.StructTag) map[string]any {
	return b.schemaWithValidation(t, tag, tag.Get("validate"))
}

func (b *schemaBuilder) schemaWithValidation(t reflect.Type, tag reflect.StructTag, validateTag string) map[string]any {
	declared := t
	t = indirectType(t)

	if t == timeType {
		return b.finishSchema(map[string]any{"type": "string", "format": "date-time"}, t, tag)
	}
	if b.mode == schemaParameter && t == durationType {
		return b.finishSchema(map[string]any{"type": "string", "pattern": goDurationSyntax}, t, tag)
	}
	if b.mode == schemaParameter && implementsTextUnmarshaler(t) {
		return b.finishSchema(map[string]any{"type": "string"}, t, tag)
	}
	if t == rawMessageType {
		return b.finishSchema(map[string]any{}, t, tag)
	}
	if t == numberType && b.mode != schemaParameter {
		return b.finishSchema(map[string]any{"type": "number"}, t, tag)
	}
	if schema := b.customWireSchema(declared); schema != nil {
		return b.finishSchema(schema, t, tag)
	}

	validateTag, elementTag, _ := splitDive(validateTag)
	if hasValidationTag(validateTag, "keys") && t.Kind() != reflect.Map {
		panic(fmt.Sprintf("kernel: validate tag keys is only supported on maps, got %s", t))
	}
	rules := parseValidation(validateTag)
	recursive := false
	switch t.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		if b.stack[t] {
			recursive = true
		} else {
			b.stack[t] = true
			defer delete(b.stack, t)
		}
	}

	var schema map[string]any
	switch t.Kind() {
	case reflect.String:
		schema = map[string]any{"type": "string"}
		applySizeRules(schema, rules, "minLength", "maxLength")
		if rules.email {
			schema["format"] = "email"
		}
		if rules.url {
			schema["format"] = "uri"
		}
		if rules.uuid {
			schema["format"] = "uuid"
		}
		applyStringPatterns(schema, rules)
	case reflect.Bool:
		schema = map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		schema = map[string]any{"type": "integer"}
		applyNumberRules(schema, rules)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		schema = map[string]any{"type": "integer", "minimum": 0}
		applyNumberRules(schema, rules)
	case reflect.Float32, reflect.Float64:
		schema = map[string]any{"type": "number"}
		applyNumberRules(schema, rules)
	case reflect.Slice, reflect.Array:
		if recursive {
			schema = map[string]any{"type": "array"}
			break
		}
		elementAddressable := b.addressable
		if t.Kind() == reflect.Slice {
			elementAddressable = true
		}
		items := b.withAddressable(elementAddressable).schemaWithValidation(t.Elem(), reflect.StructTag(""), elementTag)
		schema = map[string]any{
			"type":  "array",
			"items": items,
		}
		applySizeRules(schema, rules, "minItems", "maxItems")
		applyExactSize(schema, rules, "minItems", "maxItems")
	case reflect.Map:
		if recursive {
			schema = map[string]any{"type": "object"}
			break
		}
		keyTag, valueTag, keyed := splitMapKeys(elementTag)
		if keyed && t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("kernel: map %s uses dive,keys, but schema generation supports string keys only", t))
		}
		values := b.withAddressable(false).schemaWithValidation(t.Elem(), reflect.StructTag(""), valueTag)
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": values,
		}
		if keyed && strings.TrimSpace(keyTag) != "" {
			schema["propertyNames"] = b.schemaWithValidation(t.Key(), reflect.StructTag(""), keyTag)
		}
		applySizeRules(schema, rules, "minProperties", "maxProperties")
		applyExactSize(schema, rules, "minProperties", "maxProperties")
	case reflect.Struct:
		if recursive {
			schema = map[string]any{"type": "object"}
			break
		}

		properties := map[string]any{}
		required := make([]string, 0)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if isMetaField(field) {
				continue
			}
			if field.Anonymous {
				name, explicit := jsonTagName(field)
				if field.PkgPath != "" || !explicit {
					if name != "-" {
						panic(fmt.Sprintf(
							"kernel: %s embeds %s without an explicit json name; embedded struct fields are not supported by the schema generator, use a named field",
							t, field.Name,
						))
					}
				}
			}
			if field.PkgPath != "" {
				continue
			}
			name, jsonOptional := jsonField(field)
			if name == "" || name == "-" {
				continue
			}
			validateTag, _, _ := splitDive(field.Tag.Get("validate"))
			fieldRules := parseValidation(validateTag)
			prop := b.withAddressable(b.addressable).schema(field.Type, field.Tag)
			if desc := b.fieldDescription(t, field); desc != "" {
				prop["description"] = desc
			}
			rule, ok, err := parseSlugTag(field.Tag.Get("slug"))
			if err != nil {
				panic(fmt.Sprintf("kernel: %s.%s slug tag: %v", t, field.Name, err))
			}
			if ok {
				slugDescription := "Generated from " + rule.source + " when empty."
				if rule.overwrite {
					slugDescription = "Generated from " + rule.source + ", overwriting any supplied value."
				}
				if rule.max > 0 {
					slugDescription = strings.TrimSuffix(slugDescription, ".") + fmt.Sprintf(", max %d characters.", rule.max)
				}
				if desc, _ := prop["description"].(string); desc != "" {
					slugDescription = desc + " " + slugDescription
				}
				prop["description"] = slugDescription
				prop["x-plinth-slug-source"] = rule.source
			}
			if _, exists := properties[name]; exists {
				panic(fmt.Sprintf("kernel: duplicate json field %q in %s", name, t))
			}
			properties[name] = prop
			switch b.mode {
			case schemaRequest:
				if fieldRules.required {
					required = append(required, name)
				}
			case schemaResponse:
				if !jsonOptional {
					required = append(required, name)
				}
			}
		}
		schema = map[string]any{
			"type":                 "object",
			"properties":           properties,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
	case reflect.Interface:
		schema = map[string]any{}
	default:
		panic(fmt.Sprintf("kernel: type %s cannot be represented in JSON", t))
	}
	if enum := enumValues(t, rules.oneof); len(enum) > 0 {
		schema["enum"] = enum
	}
	if rules.unique && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
		schema["uniqueItems"] = true
	}
	if rules.hasEq {
		applyScalarEqual(schema, t, rules.eq)
	}
	if rules.hasNe {
		applyNotEqual(schema, t, rules.ne)
	}

	return b.finishSchema(schema, t, tag)
}

func (b *schemaBuilder) finishSchema(schema map[string]any, t reflect.Type, tag reflect.StructTag) map[string]any {
	if desc := tag.Get("doc"); desc != "" {
		schema["description"] = desc
	}
	if example, ok := tag.Lookup("example"); ok {
		value, err := schemaExampleValue(schema, t, example, b.mode)
		if err != nil {
			panic(fmt.Sprintf("kernel: invalid example %q for %s: %v", example, t, err))
		}
		schema["example"] = value
	}
	return schema
}

func schemaExampleValue(schema map[string]any, t reflect.Type, example string, mode schemaMode) (any, error) {
	t = indirectType(t)
	if t == timeType {
		if _, err := time.Parse(time.RFC3339, example); err != nil {
			return nil, fmt.Errorf("must be an RFC3339 timestamp")
		}
		return example, nil
	}
	if mode == schemaParameter && t == durationType {
		if _, err := time.ParseDuration(example); err != nil {
			return nil, fmt.Errorf("must be a Go duration")
		}
		return example, nil
	}

	wireType, _ := schema["type"].(string)
	switch wireType {
	case "string":
		return example, nil
	case "boolean":
		var value bool
		if err := json.Unmarshal([]byte(example), &value); err != nil {
			return nil, fmt.Errorf("must be a JSON boolean")
		}
		return value, nil
	case "integer":
		switch t.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			var value int64
			if err := json.Unmarshal([]byte(example), &value); err != nil {
				return nil, fmt.Errorf("must be a JSON integer")
			}
			return value, nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			var value uint64
			if err := json.Unmarshal([]byte(example), &value); err != nil {
				return nil, fmt.Errorf("must be a non-negative JSON integer")
			}
			return value, nil
		default:
			return nil, fmt.Errorf("integer schema has unsupported Go type %s", t)
		}
	case "number":
		if t == numberType {
			value, err := decodeJSONExample(example)
			if err != nil {
				return nil, fmt.Errorf("must be a JSON number")
			}
			number, ok := value.(json.Number)
			if !ok {
				return nil, fmt.Errorf("must be a JSON number")
			}
			return number, nil
		}
		var value float64
		if err := json.Unmarshal([]byte(example), &value); err != nil {
			return nil, fmt.Errorf("must be a JSON number")
		}
		return value, nil
	case "array", "object", "":
		return schemaJSONExample(example, wireType)
	default:
		return nil, fmt.Errorf("unsupported schema type %q", wireType)
	}
}

func schemaJSONExample(example, wireType string) (any, error) {
	value, err := decodeJSONExample(example)
	if err != nil {
		return nil, fmt.Errorf("must be a JSON %s", wireTypeLabel(wireType))
	}
	switch wireType {
	case "array":
		if _, ok := value.([]any); !ok {
			return nil, fmt.Errorf("must be a JSON array")
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return nil, fmt.Errorf("must be a JSON object")
		}
	}
	return value, nil
}

func decodeJSONExample(example string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(example))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("must contain exactly one JSON value")
	}
	return value, nil
}

func wireTypeLabel(wireType string) string {
	if wireType == "" {
		return "value"
	}
	return wireType
}

func (b *schemaBuilder) customWireSchema(declared reflect.Type) map[string]any {
	t := indirectType(declared)
	switch b.mode {
	case schemaRequest:
		if implementsTextUnmarshaler(t) {
			return map[string]any{"type": "string"}
		}
		if implementsInterface(t, jsonUnmarshalerType) {
			return map[string]any{}
		}
	case schemaResponse:
		if b.implementsResponseMarshaler(declared, textMarshalerType) {
			return map[string]any{"type": "string"}
		}
		if b.implementsResponseMarshaler(declared, jsonMarshalerType) {
			return map[string]any{}
		}
	}
	return nil
}

func (b *schemaBuilder) implementsResponseMarshaler(t, iface reflect.Type) bool {
	if t.Implements(iface) {
		return true
	}
	return b.addressable && t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface)
}

func implementsInterface(t, iface reflect.Type) bool {
	t = indirectType(t)
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}

func (b *schemaBuilder) fieldDescription(t reflect.Type, field reflect.StructField) string {
	if desc := field.Tag.Get("doc"); desc != "" {
		return desc
	}
	if b.fieldDocs == nil {
		return ""
	}
	return b.fieldDocs[fieldDocKey(t, field.Name)]
}

func parseValidation(tag string) validationRules {
	var r validationRules
	tag, _, _ = splitDive(tag)
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case part == "required":
			r.required = true
		case part == "email":
			r.email = true
		case part == "url":
			r.url = true
		case part == "uuid":
			r.uuid = true
		case part == "unique":
			r.unique = true
		case strings.HasPrefix(part, "eq="):
			r.eq = strings.TrimPrefix(part, "eq=")
			r.hasEq = true
		case strings.HasPrefix(part, "ne="):
			r.ne = strings.TrimPrefix(part, "ne=")
			r.hasNe = true
		case strings.HasPrefix(part, "contains="):
			r.contains = append(r.contains, strings.TrimPrefix(part, "contains="))
		case strings.HasPrefix(part, "startswith="):
			r.starts = append(r.starts, strings.TrimPrefix(part, "startswith="))
		case strings.HasPrefix(part, "endswith="):
			r.ends = append(r.ends, strings.TrimPrefix(part, "endswith="))
		case strings.HasPrefix(part, "min="):
			r.min = parseFloat(strings.TrimPrefix(part, "min="))
		case strings.HasPrefix(part, "max="):
			r.max = parseFloat(strings.TrimPrefix(part, "max="))
		case strings.HasPrefix(part, "len="):
			r.length = parseFloat(strings.TrimPrefix(part, "len="))
		case strings.HasPrefix(part, "gte="):
			r.gte = parseFloat(strings.TrimPrefix(part, "gte="))
		case strings.HasPrefix(part, "lte="):
			r.lte = parseFloat(strings.TrimPrefix(part, "lte="))
		case strings.HasPrefix(part, "gt="):
			r.gt = parseFloat(strings.TrimPrefix(part, "gt="))
		case strings.HasPrefix(part, "lt="):
			r.lt = parseFloat(strings.TrimPrefix(part, "lt="))
		case strings.HasPrefix(part, "oneof="):
			raw := strings.TrimPrefix(part, "oneof=")
			for _, value := range oneofPattern.FindAllString(raw, -1) {
				r.oneof = append(r.oneof, strings.ReplaceAll(value, "'", ""))
			}
		}
	}
	return r
}

func splitDive(tag string) (outer, element string, ok bool) {
	parts := strings.Split(tag, ",")
	for i, part := range parts {
		if strings.TrimSpace(part) != "dive" {
			continue
		}
		return strings.Join(parts[:i], ","), strings.Join(parts[i+1:], ","), true
	}
	return tag, "", false
}

func splitMapKeys(tag string) (key, value string, ok bool) {
	parts := strings.Split(tag, ",")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) != "keys" {
		for _, part := range parts {
			if strings.TrimSpace(part) == "keys" {
				panic("kernel: validate tag keys must immediately follow dive")
			}
		}
		return "", tag, false
	}
	for i := 1; i < len(parts); i++ {
		if strings.TrimSpace(parts[i]) == "endkeys" {
			return strings.Join(parts[1:i], ","), strings.Join(parts[i+1:], ","), true
		}
	}
	return strings.Join(parts[1:], ","), "", true
}

func hasValidationTag(tag, name string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

func parseFloat(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func applyNumberRules(schema map[string]any, rules validationRules) {
	lower, upper := ruleBounds(rules)
	if lower != nil {
		schema["minimum"] = *lower
	}
	if upper != nil {
		schema["maximum"] = *upper
	}
	if rules.gt != nil {
		schema["exclusiveMinimum"] = *rules.gt
	}
	if rules.lt != nil {
		schema["exclusiveMaximum"] = *rules.lt
	}
}

func applySizeRules(schema map[string]any, rules validationRules, minKey, maxKey string) {
	lower, upper := sizeRuleBounds(rules)
	if lower != nil {
		schema[minKey] = int(*lower)
	}
	if upper != nil {
		schema[maxKey] = int(*upper)
	}
}

func applyExactSize(schema map[string]any, rules validationRules, minKey, maxKey string) {
	if !rules.hasEq {
		return
	}
	size, err := strconv.ParseFloat(rules.eq, 64)
	if err != nil {
		return
	}
	schema[minKey] = int(size)
	schema[maxKey] = int(size)
}

func applyScalarEqual(schema map[string]any, t reflect.Type, eq string) {
	switch t.Kind() {
	case reflect.String:
		schema["const"] = eq
	case reflect.Bool:
		value, err := strconv.ParseBool(eq)
		if err == nil {
			schema["const"] = value
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(eq, 10, 64)
		if err == nil {
			schema["const"] = value
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(eq, 10, 64)
		if err == nil {
			schema["const"] = value
		}
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(eq, 64)
		if err == nil {
			schema["const"] = value
		}
	}
}

func applyNotEqual(schema map[string]any, t reflect.Type, ne string) {
	clause := map[string]any{}
	switch t.Kind() {
	case reflect.String:
		clause["const"] = ne
	case reflect.Bool:
		if value, err := strconv.ParseBool(ne); err == nil {
			clause["const"] = value
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value, err := strconv.ParseInt(ne, 10, 64); err == nil {
			clause["const"] = value
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value, err := strconv.ParseUint(ne, 10, 64); err == nil {
			clause["const"] = value
		}
	case reflect.Float32, reflect.Float64:
		if value, err := strconv.ParseFloat(ne, 64); err == nil {
			clause["const"] = value
		}
	case reflect.Slice, reflect.Array:
		if size, err := strconv.ParseFloat(ne, 64); err == nil {
			clause["minItems"] = int(size)
			clause["maxItems"] = int(size)
		}
	case reflect.Map:
		if size, err := strconv.ParseFloat(ne, 64); err == nil {
			clause["minProperties"] = int(size)
			clause["maxProperties"] = int(size)
		}
	}
	if len(clause) > 0 {
		schema["not"] = clause
	}
}

func applyStringPatterns(schema map[string]any, rules validationRules) {
	patterns := make([]string, 0, len(rules.contains)+len(rules.starts)+len(rules.ends))
	for _, value := range rules.starts {
		if value != "" {
			patterns = append(patterns, "^"+regexp.QuoteMeta(value))
		}
	}
	for _, value := range rules.ends {
		if value != "" {
			patterns = append(patterns, regexp.QuoteMeta(value)+"$")
		}
	}
	for _, value := range rules.contains {
		if value != "" {
			patterns = append(patterns, regexp.QuoteMeta(value))
		}
	}
	switch len(patterns) {
	case 0:
		return
	case 1:
		schema["pattern"] = patterns[0]
	default:
		allOf := make([]any, 0, len(patterns))
		for _, pattern := range patterns {
			allOf = append(allOf, map[string]any{"pattern": pattern})
		}
		schema["allOf"] = allOf
	}
}

func sizeRuleBounds(rules validationRules) (lower, upper *float64) {
	values := []*float64{rules.min, rules.gte, rules.length}
	if rules.gt != nil {
		value := *rules.gt + 1
		values = append(values, &value)
	}
	for _, value := range values {
		if value != nil && (lower == nil || *value > *lower) {
			lower = value
		}
	}
	values = []*float64{rules.max, rules.lte, rules.length}
	if rules.lt != nil {
		value := *rules.lt - 1
		values = append(values, &value)
	}
	for _, value := range values {
		if value != nil && (upper == nil || *value < *upper) {
			upper = value
		}
	}
	return lower, upper
}

func ruleBounds(rules validationRules) (lower, upper *float64) {
	for _, value := range []*float64{rules.min, rules.gte, rules.length} {
		if value != nil && (lower == nil || *value > *lower) {
			lower = value
		}
	}
	for _, value := range []*float64{rules.max, rules.lte, rules.length} {
		if value != nil && (upper == nil || *value < *upper) {
			upper = value
		}
	}
	return lower, upper
}

func enumValues(t reflect.Type, values []string) []any {
	if len(values) == 0 {
		return nil
	}
	enum := make([]any, 0, len(values))
	switch t.Kind() {
	case reflect.String:
		for _, value := range values {
			enum = append(enum, value)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		for _, value := range values {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil
			}
			enum = append(enum, number)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		for _, value := range values {
			number, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return nil
			}
			enum = append(enum, number)
		}
	default:
		return nil
	}
	return enum
}

func jsonName(field reflect.StructField) string {
	name, _ := jsonField(field)
	return name
}

func jsonTagName(field reflect.StructField) (name string, explicit bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return field.Name, false
	}
	name, _, _ = strings.Cut(tag, ",")
	return name, name != ""
}

func jsonField(field reflect.StructField) (name string, optional bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "-", false
	}
	name, options, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	omitEmpty := false
	omitZero := false
	for _, option := range strings.Split(options, ",") {
		switch strings.TrimSpace(option) {
		case "":
		case "omitempty":
			omitEmpty = true
		case "omitzero":
			omitZero = true
		default:
			panic(fmt.Sprintf("kernel: field %s json tag option %q is not supported", field.Name, strings.TrimSpace(option)))
		}
	}
	return name, omitZero || (omitEmpty && jsonEmptyOmittable(field.Type))
}

func jsonEmptyOmittable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array:
		return t.Len() == 0
	case reflect.Map, reflect.Slice, reflect.String,
		reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Interface, reflect.Pointer:
		return true
	default:
		return false
	}
}
