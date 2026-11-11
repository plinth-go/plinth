package kernel

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogDefaultConfigPreservesLegacyRecord(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := RequestID()(AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})))

	request := httptest.NewRequest(http.MethodGet, "/items?include=meta", nil)
	request.Header.Set("X-Request-ID", "request-123")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1: %q", len(entries), logs.String())
	}
	entry := entries[0]
	if entry["level"] != "INFO" || entry["msg"] != "http request" {
		t.Fatalf("level/message = %v/%v", entry["level"], entry["msg"])
	}
	if entry["request_id"] != "request-123" || entry["method"] != http.MethodGet || entry["path"] != "/items" {
		t.Fatalf("request fields = %#v", entry)
	}
	if entry["status"] != float64(http.StatusCreated) || entry["bytes"] != float64(2) {
		t.Fatalf("status/bytes = %v/%v", entry["status"], entry["bytes"])
	}
	if _, ok := entry["duration"]; !ok {
		t.Fatalf("missing duration: %#v", entry)
	}
	if _, ok := entry["panic"]; ok {
		t.Fatalf("unexpected panic field: %#v", entry)
	}

	raw := logs.String()
	assertLogFieldBefore(t, raw, `"msg":"http request"`, `"request_id"`)
	assertLogFieldBefore(t, raw, `"request_id"`, `"method"`)
	assertLogFieldBefore(t, raw, `"method"`, `"path"`)
	assertLogFieldBefore(t, raw, `"path"`, `"status"`)
	assertLogFieldBefore(t, raw, `"status"`, `"bytes"`)
	assertLogFieldBefore(t, raw, `"bytes"`, `"duration"`)
}

func TestAccessLogWithConfigLevel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   AccessLogConfig
		level string
	}{
		{name: "default", cfg: AccessLogConfig{}, level: "INFO"},
		{name: "explicit info", cfg: AccessLogConfig{Level: slog.LevelInfo}, level: "INFO"},
		{name: "warn", cfg: AccessLogConfig{Level: slog.LevelWarn}, level: "WARN"},
		{name: "debug", cfg: AccessLogConfig{Level: slog.LevelDebug}, level: "DEBUG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			handler := AccessLogWithConfig(logger, tc.cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/level", nil))

			entries := accessLogEntries(t, logs.Bytes())
			if len(entries) != 1 || entries[0]["level"] != tc.level {
				t.Fatalf("entries = %#v, want level %s", entries, tc.level)
			}
		})
	}
}

func TestAccessLogWithConfigDisabledLevelSkipsRequestSnapshot(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	skipCalled := false
	extraCalled := false
	handler := AccessLogWithConfig(logger, AccessLogConfig{
		Level: slog.LevelInfo,
		Skip: func(AccessLogInfo) bool {
			skipCalled = true
			return false
		},
		ExtraAttrs: func(AccessLogInfo) []slog.Attr {
			extraCalled = true
			return nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/filtered", nil))
	if logs.Len() != 0 || skipCalled || extraCalled {
		t.Fatalf("filtered access log did work: logs=%q skip=%v extra=%v", logs.String(), skipCalled, extraCalled)
	}
}

func TestAccessLogWithConfigSkipPathsAreExactAndIgnoreQuery(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := AccessLogWithConfig(logger, AccessLogConfig{SkipPaths: []string{"/skip"}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	for _, target := range []string{"/skip", "/skip?include=meta", "/skip/more", "/keep"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 2 {
		t.Fatalf("access log entries = %d, want 2: %q", len(entries), logs.String())
	}
	if entries[0]["path"] != "/skip/more" || entries[1]["path"] != "/keep" {
		t.Fatalf("logged paths = %v, %v", entries[0]["path"], entries[1]["path"])
	}
}

func TestAccessLogWithConfigSkipPathsUseFinalPath(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := AccessLogWithConfig(logger, AccessLogConfig{SkipPaths: []string{"/initial", "/final"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/initial":
			r.URL.Path = "/kept"
		case "/kept":
			r.URL.Path = "/final"
		}
	}))

	for _, path := range []string{"/initial", "/kept"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["path"] != "/kept" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestAccessLogWithConfigSkipSeesFinalRequestSnapshot(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var snapshots []AccessLogInfo
	router := NewRouter(logger, "test", "1")
	router.Use(RequestID(), AccessLogWithConfig(logger, AccessLogConfig{
		Skip: func(info AccessLogInfo) bool {
			snapshots = append(snapshots, info)
			return true
		},
	}))
	router.Raw(http.MethodGet, "/items/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("hello"))
	}), OperationID("get-item"), Status(http.StatusAccepted))

	request := httptest.NewRequest(http.MethodGet, "/items/42?include=meta", nil)
	request.Header.Set("X-Request-ID", "request-skip")
	router.ServeHTTP(httptest.NewRecorder(), request)

	if entries := accessLogEntries(t, logs.Bytes()); len(entries) != 0 {
		t.Fatalf("skipped request was logged: %q", logs.String())
	}
	if len(snapshots) != 1 {
		t.Fatalf("skip snapshots = %d, want 1", len(snapshots))
	}
	info := snapshots[0]
	if info.Request == nil || info.RequestID != "request-skip" || info.Method != http.MethodGet || info.Path != "/items/42" {
		t.Fatalf("snapshot request fields = %#v", info)
	}
	if info.Status != http.StatusAccepted || info.Bytes != 5 || info.Duration < 0 || info.Panic != nil {
		t.Fatalf("snapshot result fields = %#v", info)
	}
	if !info.HasOperation || info.Operation.OperationID != "get-item" || info.Operation.Path != "/items/{id}" {
		t.Fatalf("snapshot operation = %#v, has = %v", info.Operation, info.HasOperation)
	}
}

func TestAccessLogWithConfigExtraAttrsAreAppendedAndSkippedRequestsDoNotCallThem(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var skippedInfo, extraInfo AccessLogInfo
	extraCalls := 0
	handler := AccessLogWithConfig(logger, AccessLogConfig{
		Skip: func(info AccessLogInfo) bool {
			skippedInfo = info
			return info.Path == "/skipped"
		},
		ExtraAttrs: func(info AccessLogInfo) []slog.Attr {
			extraCalls++
			extraInfo = info
			return []slog.Attr{slog.String("tenant", "acme")}
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/kept", nil))
	if extraCalls != 1 {
		t.Fatalf("extra attrs calls = %d, want 1", extraCalls)
	}
	if skippedInfo.Status != extraInfo.Status || skippedInfo.Bytes != extraInfo.Bytes ||
		skippedInfo.Duration != extraInfo.Duration || skippedInfo.RequestID != extraInfo.RequestID ||
		skippedInfo.Path != extraInfo.Path || skippedInfo.HasOperation != extraInfo.HasOperation {
		t.Fatalf("Skip/ExtraAttrs snapshots differ:\nskip=%#v\nextra=%#v", skippedInfo, extraInfo)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/skipped", nil))
	if extraCalls != 1 {
		t.Fatalf("skipped request called ExtraAttrs: calls = %d", extraCalls)
	}

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["tenant"] != "acme" {
		t.Fatalf("entries = %#v", entries)
	}
	assertLogFieldBefore(t, logs.String(), `"duration"`, `"tenant"`)
}

func TestAccessLogWithConfigHonorsOperationSkipAndNotHidden(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := NewRouter(logger, "test", "1")
	router.Use(AccessLogWithConfig(logger, AccessLogConfig{}))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	router.Raw(http.MethodGet, "/skipped", ok, SkipAccessLog())
	router.Raw(http.MethodGet, "/hidden", ok, Hidden())

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/skipped", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hidden", nil))

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["path"] != "/hidden" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestAccessLogWithConfigRecordsUnmatchedNotFound(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var snapshot AccessLogInfo
	router := NewRouter(logger, "test", "1")
	handler := AccessLogWithConfig(logger, AccessLogConfig{
		Skip: func(info AccessLogInfo) bool {
			snapshot = info
			return false
		},
	})(router)
	router.Raw(http.MethodGet, "/exists", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["status"] != float64(http.StatusNotFound) {
		t.Fatalf("entries = %#v", entries)
	}
	if snapshot.HasOperation || snapshot.Operation.OperationID != "" {
		t.Fatalf("404 snapshot operation = %#v, has = %v", snapshot.Operation, snapshot.HasOperation)
	}
}

func TestGroupSkipAccessLogCoversRoutesNestedGroupsAndOps(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := NewRouter(logger, "test", "1")
	router.Use(AccessLogWithConfig(logger, AccessLogConfig{}))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	group := router.Group("/group")
	group.SkipAccessLog()
	group.Raw(http.MethodGet, "/direct", ok, OperationID("group-direct"))
	nested := group.Group("/nested")
	nested.Raw(http.MethodGet, "/route", ok, OperationID("group-nested"))
	group.RegisterOps(OpsConfig{DisableReady: true, DisableOpenAPI: true, LogAccess: true})
	router.Raw(http.MethodGet, "/normal", ok)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/normal", nil))
	for _, path := range []string{"/group/direct", "/group/nested/route", "/group/livez"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["path"] != "/normal" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestGroupSkipAccessLogRegistrationOrder(t *testing.T) {
	raw := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	t.Run("after direct route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		group := router.Group("/group")
		group.Raw(http.MethodGet, "/direct", raw)
		assertPanicsWith(t, "Group.SkipAccessLog", func() {
			group.SkipAccessLog()
		})
	})

	t.Run("parent after child route", func(t *testing.T) {
		router := NewRouter(nil, "test", "1")
		parent := router.Group("/group")
		child := parent.Group("/nested")
		child.Raw(http.MethodGet, "/route", raw)
		assertPanicsWith(t, "Group.SkipAccessLog", func() {
			parent.SkipAccessLog()
		})
	})
}

func TestOpsAccessLogPolicy(t *testing.T) {
	newRouter := func(logs *bytes.Buffer) *Router {
		logger := slog.New(slog.NewJSONHandler(logs, nil))
		router := NewRouter(logger, "test", "1")
		router.Use(RequestID(), AccessLogWithConfig(logger, AccessLogConfig{}))
		return router
	}
	send := func(router *Router, path string) {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	t.Run("default suppresses custom ops paths", func(t *testing.T) {
		var logs bytes.Buffer
		router := newRouter(&logs)
		router.RegisterOps(OpsConfig{
			LivePath:    "/ops/live",
			ReadyPath:   "/ops/ready",
			OpenAPIPath: "/ops/spec",
			DocsPath:    "/ops/docs/",
		})
		for _, path := range []string{"/ops/live", "/ops/ready", "/ops/spec", "/ops/docs/"} {
			send(router, path)
		}
		if entries := accessLogEntries(t, logs.Bytes()); len(entries) != 0 {
			t.Fatalf("default ops requests were logged: %q", logs.String())
		}
	})

	t.Run("log access records ops requests", func(t *testing.T) {
		var logs bytes.Buffer
		router := newRouter(&logs)
		router.RegisterOps(OpsConfig{
			LivePath:       "/ops/live",
			DisableReady:   true,
			DisableOpenAPI: true,
			LogAccess:      true,
		})
		send(router, "/ops/live")
		entries := accessLogEntries(t, logs.Bytes())
		if len(entries) != 1 || entries[0]["path"] != "/ops/live" {
			t.Fatalf("entries = %#v", entries)
		}
	})

	t.Run("group skip wins over log access", func(t *testing.T) {
		var logs bytes.Buffer
		router := newRouter(&logs)
		group := router.Group("/internal")
		group.SkipAccessLog()
		group.RegisterOps(OpsConfig{
			DisableReady:   true,
			DisableOpenAPI: true,
			LogAccess:      true,
		})
		send(router, "/internal/livez")
		if entries := accessLogEntries(t, logs.Bytes()); len(entries) != 0 {
			t.Fatalf("group-skipped ops request was logged: %q", logs.String())
		}
	})
}

func TestAccessLogWithConfigRecordsAndRepanics(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := AccessLogWithConfig(logger, AccessLogConfig{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	if recovered != "boom" {
		t.Fatalf("recovered panic = %v, want boom", recovered)
	}

	entries := accessLogEntries(t, logs.Bytes())
	if len(entries) != 1 || entries[0]["status"] != float64(http.StatusInternalServerError) || entries[0]["panic"] != "boom" {
		t.Fatalf("entries = %#v", entries)
	}
}

func accessLogEntries(t *testing.T, logs []byte) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if entry["msg"] == "http request" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func assertLogFieldBefore(t *testing.T, log, first, second string) {
	t.Helper()
	firstIndex := strings.Index(log, first)
	secondIndex := strings.Index(log, second)
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("field %s does not precede %s in %q", first, second, log)
	}
}
