package kernel

import (
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/textproto"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/plinth-go/plinth/utils"
)

var (
	signedDecimalConstraint   = regexp.MustCompile(`^[-+]?(?:0|[1-9][0-9]*)$`)
	unsignedDecimalConstraint = regexp.MustCompile(`^\+?(?:0|[1-9][0-9]*)$`)
)

// Validate checks invariants that cannot be expressed by the registration
// types alone. Applications should call it from their route-check command.
func (r *Router) Validate() error {
	var errs []error
	seenRoutes := make(map[string]string)
	seenOperationIDs := make(map[string]string)

	for _, meta := range r.operationSnapshot() {
		route := meta.Method + " " + meta.Path
		if meta.Method == "" || meta.Method != strings.ToUpper(meta.Method) || !validHTTPToken(meta.Method) {
			errs = append(errs, fmt.Errorf("%s must use an uppercase HTTP token", route))
		}
		if meta.Status < 100 || meta.Status > 599 {
			errs = append(errs, fmt.Errorf("%s has invalid response status %d", route, meta.Status))
		}
		if meta.OperationID == "" {
			errs = append(errs, fmt.Errorf("%s has an empty operationId", route))
		}
		for _, permission := range meta.RequiredPermissions {
			if !validPermission(permission) {
				errs = append(errs, fmt.Errorf("%s has invalid required permission %q", route, permission))
			}
		}
		routeKey := meta.Method + "\x00" + meta.Path
		if previous, ok := seenRoutes[routeKey]; ok {
			errs = append(errs, fmt.Errorf("%s duplicates %s", route, previous))
		} else {
			seenRoutes[routeKey] = route
		}

		if previous, ok := seenOperationIDs[meta.OperationID]; ok {
			errs = append(errs, fmt.Errorf("%s operationId %q duplicates %s", route, meta.OperationID, previous))
		} else {
			seenOperationIDs[meta.OperationID] = route
		}

		seenParams := make(map[string]string)
		for _, param := range meta.Params {
			name := param.Name
			if param.In == "header" {
				if !validHTTPToken(name) {
					errs = append(errs, fmt.Errorf("%s has invalid header parameter name %q", route, name))
					continue
				}
				name = textproto.CanonicalMIMEHeaderKey(name)
			}
			key := param.In + "\x00" + name
			if previous, ok := seenParams[key]; ok {
				errs = append(errs, fmt.Errorf("%s has duplicate %s parameter %q (already declared at %s)", route, param.In, param.Name, previous))
			} else {
				seenParams[key] = route
			}
		}

		for _, contentType := range meta.Produces {
			if _, _, err := mime.ParseMediaType(contentType); err != nil {
				errs = append(errs, fmt.Errorf("%s has invalid produces media type %q: %w", route, contentType, err))
			}
		}

		if meta.raw {
			if !meta.Hidden && len(meta.Produces) == 0 && meta.Status != http.StatusSwitchingProtocols {
				errs = append(errs, fmt.Errorf(
					"%s is a visible raw route without kernel.Produces or kernel.Hidden; use kernel.Handle for typed responses",
					route,
				))
			}
			continue
		}

		if meta.Status < http.StatusOK {
			errs = append(errs, fmt.Errorf(
				"%s uses informational status %d on a typed route; typed routes require a final response status, so use Raw",
				route, meta.Status,
			))
		}

		if meta.inputType != nil {
			if err := checkInputFields(meta.inputType); err != nil {
				errs = append(errs, fmt.Errorf("%s has invalid input fields: %w", route, err))
			}
			if err := r.validateTags(meta.inputType); err != nil {
				errs = append(errs, fmt.Errorf("%s has invalid validate tags: %w", route, err))
			}
		}
		for _, check := range meta.defaults {
			if err := checkDefault(r.validate, check); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", route, err))
			}
		}

		if meta.responder {
			if meta.Status != http.StatusOK {
				errs = append(errs, fmt.Errorf(
					"%s has a Responder output with status %d; typed Responder outputs must use 200 and own the response status, so use Raw for other statuses",
					route, meta.Status,
				))
			}
			if meta.Status != http.StatusNoContent && meta.Status != http.StatusNotModified && len(meta.Produces) == 0 {
				errs = append(errs, fmt.Errorf(
					"%s has a Responder output but no produces metadata; add an embedded kernel.Meta with a produces tag",
					route,
				))
			}
		} else if len(meta.Produces) > 0 {
			errs = append(errs, fmt.Errorf(
				"%s declares produces metadata but its output does not implement Responder",
				route,
			))
		}

		if err := validatePathParams(meta); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("route validation failed: %w", errors.Join(errs...))
}

func validHTTPToken(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		char := name[i]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func (r *Router) validateTags(t reflect.Type) error {
	return checkValidationTags(r.validate, t, map[reflect.Type]bool{})
}

func checkInputFields(t reflect.Type) error {
	t = indirectType(t)
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("input must be a struct, got %s", t)
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" || isMetaField(field) {
			continue
		}
		location := ""
		locations := make([]string, 0, 4)
		for _, candidate := range []string{"path", "query", "header", "body"} {
			if _, ok := field.Tag.Lookup(candidate); ok {
				locations = append(locations, candidate)
				location = utils.IfEmptyElse(location, candidate)
			}
		}
		switch len(locations) {
		case 0:
			return fmt.Errorf("%s.%s must have one of path/query/header/body tags", t, field.Name)
		case 1:
		default:
			return fmt.Errorf("%s.%s has multiple parameter locations %v; use exactly one", t, field.Name, locations)
		}
		if location == "body" {
			continue
		}
		if err := checkParameterType(location, field.Type); err != nil {
			return fmt.Errorf("%s.%s %s parameter: %w", t, field.Name, location, err)
		}
	}
	return nil
}

func checkParameterType(location string, t reflect.Type) error {
	if implementsTextUnmarshaler(t) {
		return nil
	}
	t = indirectType(t)
	if t == rawMessageType {
		return fmt.Errorf("type %s is not supported in %s parameters", t, location)
	}
	if t == timeType || t == durationType || isScalarParameterKind(t.Kind()) {
		return nil
	}
	if location == "query" && t.Kind() == reflect.Slice {
		return checkParameterElementType(t.Elem())
	}
	return fmt.Errorf("type %s is not supported in %s parameters", t, location)
}

func checkParameterElementType(t reflect.Type) error {
	if implementsTextUnmarshaler(t) {
		return nil
	}
	t = indirectType(t)
	if t == rawMessageType {
		return fmt.Errorf("type %s is not supported in query parameters", t)
	}
	if t == timeType || t == durationType || isScalarParameterKind(t.Kind()) {
		return nil
	}
	return fmt.Errorf("query parameter slice elements must be scalar or encoding.TextUnmarshaler, got %s", t)
}

func isScalarParameterKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func checkValidationTags(v *validator.Validate, t reflect.Type, seen map[reflect.Type]bool) error {
	t = indirectType(t)
	if t == timeType || t == rawMessageType {
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return checkValidationTags(v, t.Elem(), seen)
	case reflect.Map:
		if err := checkValidationTags(v, t.Key(), seen); err != nil {
			return err
		}
		return checkValidationTags(v, t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return nil
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || isMetaField(field) {
				continue
			}
			if err := checkValidationSemantics(field.Tag.Get("validate")); err != nil {
				return fmt.Errorf("%s.%s: %w", t, field.Name, err)
			}
			if err := checkSchemaRuleSemantics(field.Type, field.Tag.Get("validate")); err != nil {
				return fmt.Errorf("%s.%s: %w", t, field.Name, err)
			}
			if err := checkValidationTags(v, field.Type, seen); err != nil {
				return err
			}
		}
		if err := callValidator(v, reflect.New(t).Elem().Interface()); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	case reflect.Interface:
		return nil
	}
	return nil
}

func checkSchemaRuleSemantics(t reflect.Type, tag string) error {
	t = indirectType(t)
	outer, element, hasDive := splitDive(tag)
	if err := checkRuleParameters(t, outer); err != nil {
		return err
	}
	if !hasDive {
		return nil
	}

	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if hasValidationTag(element, "keys") {
			return fmt.Errorf("validate rule keys is only supported on map values")
		}
		return checkSchemaRuleSemantics(t.Elem(), element)
	case reflect.Map:
		keyTag, valueTag, keyed := splitMapKeys(element)
		if keyed {
			if t.Key().Kind() != reflect.String {
				return fmt.Errorf("validate rules for map keys require string keys, got %s", t.Key())
			}
			if err := checkSchemaRuleSemantics(t.Key(), keyTag); err != nil {
				return err
			}
		}
		return checkSchemaRuleSemantics(t.Elem(), valueTag)
	default:
		return nil
	}
}

func checkRuleParameters(t reflect.Type, tag string) error {
	for _, raw := range strings.Split(tag, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		name, param, hasParam := strings.Cut(part, "=")
		switch name {
		case "min", "max", "len", "gte", "lte", "gt", "lt":
			if !hasParam {
				return fmt.Errorf("validate rule %s requires a parameter", name)
			}
			if err := checkNumericRuleParameter(t, name, param); err != nil {
				return err
			}
		case "eq", "ne":
			if !hasParam {
				return fmt.Errorf("validate rule %s requires a parameter", name)
			}
			if err := checkEqualityRuleParameter(t, name, param); err != nil {
				return err
			}
		case "oneof":
			if !hasParam {
				return errors.New("validate rule oneof requires a parameter")
			}
			if err := checkOneOfRuleParameter(t, param); err != nil {
				return err
			}
		case "contains", "startswith", "endswith":
			if !hasParam || param == "" {
				return fmt.Errorf("validate rule %s requires a non-empty parameter", name)
			}
			if t.Kind() != reflect.String {
				return fmt.Errorf("validate rule %s is only supported on strings, got %s", name, t)
			}
		case "email", "url", "uuid":
			if t.Kind() != reflect.String {
				return fmt.Errorf("validate rule %s is only supported on strings, got %s", name, t)
			}
		}
	}
	return nil
}

func checkNumericRuleParameter(t reflect.Type, rule, param string) error {
	if t == timeType {
		if rule == "len" {
			return fmt.Errorf("validate rule len is not supported on %s", t)
		}
		return nil
	}
	switch t.Kind() {
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
		if !unsignedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a non-negative base-10 integer", rule, param, t)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == durationType {
			return checkDurationOrIntegerParameter(rule, param)
		}
		if !signedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a base-10 integer", rule, param, t)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !unsignedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a non-negative base-10 integer", rule, param, t)
		}
	case reflect.Float32, reflect.Float64:
		if err := checkFiniteFloatParameter(rule, param); err != nil {
			return err
		}
	default:
		return fmt.Errorf("validate rule %s is not supported on %s", rule, t)
	}
	return nil
}

func checkEqualityRuleParameter(t reflect.Type, rule, param string) error {
	switch t.Kind() {
	case reflect.String:
		return nil
	case reflect.Bool:
		if _, err := strconv.ParseBool(param); err != nil {
			return fmt.Errorf("validate rule %s=%s on %s requires a boolean", rule, param, t)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if t == durationType {
			return checkDurationOrIntegerParameter(rule, param)
		}
		if !signedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a base-10 integer", rule, param, t)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !unsignedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a non-negative base-10 integer", rule, param, t)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		if !unsignedDecimalConstraint.MatchString(param) {
			return fmt.Errorf("validate rule %s=%s on %s requires a non-negative base-10 item count", rule, param, t)
		}
	case reflect.Float32, reflect.Float64:
		if err := checkFiniteFloatParameter(rule, param); err != nil {
			return err
		}
	default:
		return fmt.Errorf("validate rule %s is not supported on %s", rule, t)
	}
	return nil
}

func checkOneOfRuleParameter(t reflect.Type, param string) error {
	values := oneofPattern.FindAllString(param, -1)
	if len(values) == 0 {
		return errors.New("validate rule oneof requires at least one value")
	}
	for i := range values {
		values[i] = strings.ReplaceAll(values[i], "'", "")
	}
	switch t.Kind() {
	case reflect.String:
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		for _, value := range values {
			if !signedDecimalConstraint.MatchString(value) {
				return fmt.Errorf("validate rule oneof value %q on %s requires a base-10 integer", value, t)
			}
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		for _, value := range values {
			if !unsignedDecimalConstraint.MatchString(value) {
				return fmt.Errorf("validate rule oneof value %q on %s requires a non-negative base-10 integer", value, t)
			}
		}
	default:
		return fmt.Errorf("validate rule oneof is not supported on %s", t)
	}
	return nil
}

func checkDurationOrIntegerParameter(rule, param string) error {
	if _, err := time.ParseDuration(param); err == nil {
		return nil
	}
	if !signedDecimalConstraint.MatchString(param) {
		return fmt.Errorf("validate rule %s=%s on time.Duration requires a Go duration or base-10 integer", rule, param)
	}
	return nil
}

func checkFiniteFloatParameter(rule, param string) error {
	value, err := strconv.ParseFloat(param, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return fmt.Errorf("validate rule %s=%s requires a finite number", rule, param)
	}
	return nil
}

func checkValidationSemantics(tag string) error {
	segments := []string{""}
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == "dive" {
			segments = append(segments, "")
			continue
		}
		segments[len(segments)-1] += part + ","
	}
	for _, segment := range segments {
		required := false
		omitted := ""
		for _, part := range strings.Split(segment, ",") {
			switch strings.TrimSpace(part) {
			case "required":
				required = true
			case "omitempty", "omitzero", "omitnil":
				omitted = strings.TrimSpace(part)
			}
		}
		if required && omitted != "" {
			return fmt.Errorf("validate tag combines required with %s at the same level; required fields must not skip zero values", omitted)
		}
	}
	return nil
}

func callValidator(v *validator.Validate, value any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("validator panicked: %v", recovered)
		}
	}()
	if err := v.Struct(value); err != nil {
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) {
			return nil
		}
		var invalid *validator.InvalidValidationError
		if errors.As(err, &invalid) {
			return nil
		}
		return err
	}
	return nil
}

func checkDefault(v *validator.Validate, check defaultCheck) (err error) {
	if strings.TrimSpace(check.validateTag) == "" {
		return nil
	}
	effectiveTag := stripOmissionRules(check.validateTag)
	if strings.TrimSpace(effectiveTag) == "" {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("check default %q for %s parameter %q: validator panicked: %v", check.raw, check.in, check.name, recovered)
		}
	}()
	if err := v.Var(check.value, effectiveTag); err != nil {
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) && len(validationErrors) > 0 {
			fieldErr := validationErrors[0]
			return fmt.Errorf(
				"default %q for %s parameter %q fails validate:%q after defaulting: %s",
				check.raw, check.in, check.name, check.validateTag,
				validationMessage(fieldErr.Tag(), fieldErr.Param()),
			)
		}
		var invalid *validator.InvalidValidationError
		if errors.As(err, &invalid) {
			return nil
		}
		return fmt.Errorf("check default %q for %s parameter %q: %w", check.raw, check.in, check.name, err)
	}
	return nil
}

func stripOmissionRules(tag string) string {
	parts := strings.Split(tag, ",")
	filtered := parts[:0]
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "omitempty", "omitzero", "omitnil":
			continue
		default:
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, ",")
}

func validatePathParams(meta OperationMeta) error {
	placeholders := pathPlaceholders(meta.Path)
	params := make([]string, 0, len(meta.Params))
	for _, param := range meta.Params {
		if param.In == "path" {
			params = append(params, param.Name)
		}
	}
	sort.Strings(placeholders)
	sort.Strings(params)
	if strings.Join(placeholders, "\x00") == strings.Join(params, "\x00") {
		return nil
	}
	return fmt.Errorf(
		"%s %s path placeholders %v do not match path parameters %v",
		meta.Method, meta.Path, placeholders, params,
	)
}

func pathPlaceholders(path string) []string {
	var names []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i+1:], '}')
		if end < 0 {
			break
		}
		name := path[i+1 : i+1+end]
		i += end + 1
		if name == "$" {
			continue
		}
		names = append(names, strings.TrimSuffix(name, "..."))
	}
	return names
}
