package kernel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Problem follows RFC 9457 Problem Details.
type Problem struct {
	Type     string       `json:"type,omitempty"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`
}

type FieldError struct {
	Location string `json:"location,omitempty"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

type HTTPError struct {
	Status int
	Title  string
	Detail string
	Err    error
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "kernel: nil HTTP error"
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.Status)
}

func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "kernel: nil validation error"
	}
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	return e.Errors[0].Message
}

func BadRequest(detail string) error {
	return &HTTPError{Status: http.StatusBadRequest, Title: "Bad Request", Detail: detail}
}

func NotFound(detail string) error {
	return &HTTPError{Status: http.StatusNotFound, Title: "Not Found", Detail: detail}
}

func Conflict(detail string) error {
	return &HTTPError{Status: http.StatusConflict, Title: "Conflict", Detail: detail}
}

func Forbidden(detail string) error {
	return &HTTPError{Status: http.StatusForbidden, Title: "Forbidden", Detail: detail}
}

func TooManyRequests(detail string) error {
	return &HTTPError{Status: http.StatusTooManyRequests, Title: http.StatusText(http.StatusTooManyRequests), Detail: detail}
}

func Unprocessable(detail string) error {
	return &HTTPError{Status: http.StatusUnprocessableEntity, Title: "Unprocessable Entity", Detail: detail}
}

func TooLarge(detail string) error {
	return &HTTPError{Status: http.StatusRequestEntityTooLarge, Title: http.StatusText(http.StatusRequestEntityTooLarge), Detail: detail}
}

func UnsupportedMediaType(detail string) error {
	return &HTTPError{Status: http.StatusUnsupportedMediaType, Title: http.StatusText(http.StatusUnsupportedMediaType), Detail: detail}
}

func Internal(err error) error {
	return &HTTPError{Status: http.StatusInternalServerError, Title: "Internal Server Error", Err: err}
}

// WriteProblem writes an RFC 9457 problem response. Details for every 5xx
// response are replaced with a generic message; the original detail is sent to
// logf. Lower-status HTTPError values may expose their detail or wrapped cause.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error, logf func(string, ...any)) {
	method, path := "", ""
	if r != nil {
		method = r.Method
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	if isNilInterface(w) {
		if logf != nil {
			logf("problem response writer is nil", "method", method, "path", path)
		}
		return
	}
	if isNilInterface(err) {
		err = errors.New("nil error value")
	}

	problem := &Problem{
		Title:  http.StatusText(http.StatusInternalServerError),
		Status: http.StatusInternalServerError,
	}

	var httpErr *HTTPError
	var validationErr *ValidationError
	isValidation := errors.As(err, &validationErr)
	isHTTP := errors.As(err, &httpErr)
	if (isValidation && validationErr == nil) || (isHTTP && httpErr == nil) {
		err = errors.New("nil typed error value")
		isValidation = false
		isHTTP = false
	}
	switch {
	case isValidation:
		problem.Status = http.StatusUnprocessableEntity
		problem.Title = http.StatusText(http.StatusUnprocessableEntity)
		problem.Detail = "request validation failed"
		problem.Errors = validationErr.Errors
	case isHTTP:
		status := httpErr.Status
		if status < http.StatusBadRequest || status > 599 {
			if logf != nil {
				logf("invalid HTTP error status", "status", status, "method", method, "path", path)
			}
			status = http.StatusInternalServerError
		}
		problem.Status = status
		problem.Title = httpErr.Title
		if status >= http.StatusInternalServerError {
			problem.Title = serverErrorTitle(status)
		} else if problem.Title == "" || status != httpErr.Status {
			problem.Title = http.StatusText(status)
		}
		problem.Detail = httpErr.Detail
		if status != httpErr.Status {
			problem.Detail = "internal server error"
		} else if status >= http.StatusInternalServerError {
			if httpErr.Detail != "" && logf != nil {
				logf("server error detail", "method", method, "path", path, "detail", httpErr.Detail)
			}
			problem.Detail = "internal server error"
		} else if problem.Detail == "" && httpErr.Err != nil && status < http.StatusInternalServerError {
			problem.Detail = httpErr.Err.Error()
		}
		if problem.Status >= http.StatusInternalServerError && problem.Detail == "" {
			problem.Detail = "internal server error"
		}
		if httpErr.Err != nil && logf != nil {
			logf("request failed", "method", method, "path", path, "error", httpErr.Err)
		}
	default:
		if logf != nil {
			logf("unhandled request error", "method", method, "path", path, "error", err)
		}
		problem.Detail = "internal server error"
	}

	if r != nil {
		if id := RequestIDFromContext(r.Context()); id != "" {
			problem.Instance = id
		}
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(problem); err != nil {
		if logf != nil {
			logf("encode problem response", "method", method, "path", path, "error", err)
		}
		return
	}
	setProblemHeaders(w.Header())
	w.WriteHeader(problem.Status)
	if err := writeBody(w, body.Bytes()); err != nil && logf != nil {
		logf("write problem response", "method", method, "path", path, "error", err)
	}
}

func serverErrorTitle(status int) string {
	if title := http.StatusText(status); title != "" {
		return title
	}
	return http.StatusText(http.StatusInternalServerError)
}

func validationMessage(tag, param string) string {
	switch tag {
	case "required":
		return "is required"
	case "min":
		return "must be at least " + param
	case "max":
		return "must be at most " + param
	case "len":
		return "must have length " + param
	case "gte":
		return "must be greater than or equal to " + param
	case "lte":
		return "must be less than or equal to " + param
	case "email":
		return "must be a valid email address"
	case "url":
		return "must be a valid URL"
	case "uuid":
		return "must be a valid UUID"
	case "oneof":
		return "must be one of: " + param
	default:
		if param != "" {
			return fmt.Sprintf("failed %s=%s validation", tag, param)
		}
		return "failed " + tag + " validation"
	}
}

func fieldLocation(field string, locations map[string]string) string {
	if loc, ok := locations[field]; ok {
		return loc
	}
	if i := strings.LastIndex(field, "."); i >= 0 {
		if loc, ok := locations[field[i+1:]]; ok {
			return loc
		}
	}
	return "body"
}
