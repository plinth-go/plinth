// Package authz provides declarative route permission checks for Plinth. It
// depends on kernel operation metadata only; applications inject how effective
// permissions are obtained for the current request.
package authz

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/utils"
)

// PermissionSource returns the effective permissions for the current request.
// It commonly reads kernel/auth Principal claims or an application policy
// service. Return an error only when policy cannot be evaluated; a missing
// identity should normally yield an empty permission set.
type PermissionSource interface {
	Permissions(context.Context) ([]string, error)
}

type PermissionSourceFunc func(context.Context) ([]string, error)

func (f PermissionSourceFunc) Permissions(ctx context.Context) ([]string, error) {
	return f(ctx)
}

// Config controls denial responses and operational logging.
type Config struct {
	Logger *slog.Logger
	// DeniedDetail is exposed on 403 responses. Empty selects "access denied".
	DeniedDetail string
}

// New builds middleware that enforces kernel.Meta authz tags and
// kernel.RequirePermissions options. All declared permissions are required;
// an effective permission of "*" satisfies every requirement.
func New(source PermissionSource, cfg Config) (func(http.Handler) http.Handler, error) {
	if isNil(source) {
		return nil, errors.New("authz permission source is nil")
	}
	logger := utils.IfNilElse(cfg.Logger, slog.Default())
	detail := utils.IfEmptyElse(strings.TrimSpace(cfg.DeniedDetail), "access denied")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			operation, ok := kernel.OperationFromContext(r.Context())
			if !ok {
				err := errors.New("authz middleware ran outside a matched Plinth route")
				logger.Error("authorization metadata missing", "method", r.Method, "path", r.URL.Path)
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			if len(operation.RequiredPermissions) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			granted, err := source.Permissions(r.Context())
			if err != nil {
				logger.Error("authorization policy failed",
					"operation_id", operation.OperationID,
					"method", operation.Method,
					"path", operation.Path,
					"error", err,
				)
				kernel.WriteProblem(w, r, kernel.Internal(err), logger.Error)
				return
			}
			if !allows(granted, operation.RequiredPermissions) {
				logger.Warn("authorization denied",
					"operation_id", operation.OperationID,
					"method", operation.Method,
					"path", operation.Path,
					"required_permissions", operation.RequiredPermissions,
				)
				kernel.WriteProblem(w, r, kernel.Forbidden(detail), logger.Error)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

func allows(granted, required []string) bool {
	effective := make(map[string]bool, len(granted))
	for _, permission := range granted {
		permission = strings.TrimSpace(permission)
		if permission != "" {
			effective[permission] = true
		}
	}
	if effective["*"] {
		return true
	}
	for _, permission := range required {
		if !effective[permission] {
			return false
		}
	}
	return true
}

func isNil(source PermissionSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
