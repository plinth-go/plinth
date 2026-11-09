# 测试使用者自己的业务逻辑

目标：使用真实的 Plinth router 测试 HTTP 契约，同时让业务依赖保持 fake、快速、可控。

> 伪代码；`Store`、`Service` 是应用自己的窄接口。

## 推荐分层

1. 纯函数/handler 测试：直接调用 `func(ctx, in)`，验证业务结果。
2. HTTP 契约测试：真实 router + `httptest`，注入 fake 依赖，验证绑定、状态码、header、
   Problem 和副作用。
3. 基础设施集成测试：只有 SQL、Redis 命令、migration 或 worker 语义必须在真实服务上
   验证时才启动外部依赖；缺少依赖时 `t.Skip`。

不要为业务测试启动完整 runtime，也不要让每条 HTTP 测试依赖 Postgres/Redis。

## 用真实 router 测 HTTP 契约

```go
type fakeStore struct {
    created []domain.Record
    err     error
}

func (f *fakeStore) Create(_ context.Context, value domain.Record) error {
    f.created = append(f.created, value)
    return f.err
}

func newTestHandler(t *testing.T, store Store) http.Handler {
    t.Helper()
    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    router := kernel.NewRouter(logger, "test", "test")
    router.Use(kernel.RequestID(), kernel.Recover(logger), kernel.BodyLimit(1<<20))
    router.Handle(func(ctx context.Context, in CreateInput) (CreateOutput, error) {
        return createRecord(ctx, store, in)
    })
    return router
}
```

```go
func TestCreateRecordRejectsInvalidBody(t *testing.T) {
    handler := newTestHandler(t, &fakeStore{})
    request := httptest.NewRequest(http.MethodPost, "/v1/records",
        strings.NewReader(`{"name":""}`))
    request.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()

    handler.ServeHTTP(rec, request)

    if rec.Code != http.StatusUnprocessableEntity {
        t.Fatalf("status = %d", rec.Code)
    }
    var problem kernel.Problem
    if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
        t.Fatal(err)
    }
    if problem.Status != http.StatusUnprocessableEntity || len(problem.Errors) == 0 {
        t.Fatalf("problem = %#v", problem)
    }
}
```

## 测应用自己的组装函数

如果应用有 `api.New(api.Dependencies{...})` 这样的工厂，测试应调用它，而不是手工注册
一个缩略路由。这样测试覆盖真实的路由表、中间件和 OpenAPI 选项。

```go
func newTestAPI(t *testing.T) http.Handler {
    t.Helper()
    return api.New(api.Dependencies{
        Logger:  discardLogger(),
        Store:   &fakeStore{},
        Streams: kernel.NewStreamRegistry(),
        Static:  http.NotFoundHandler(),
    })
}
```

## 测可选中间件

- CSRF/限流/session 优先使用内存 store 做路由级测试。
- 断言 403/429/302 或 response header，而不是重新测试 SCS、go-redis 或 crypto 算法。
- 权限测试使用固定 `PermissionSourceFunc`，不要为框架测试启动真实策略服务。
- CORS 测试应直接包 handler，验证 preflight 不会进入业务路由。

## 不要做什么

- 不要为测试引入第二套 router 或“测试专用 DSL”。
- 不要重复测试标准库 JSON、ServeMux、TLS、slog、SCS 序列化或 go-redis 命令语义。
- 不要用全局 `slog.Default()`、全局随机时钟或真实网络作为默认测试依赖。
- 不要把示例应用扩展成业务正确性测试集；示例只展示测试接缝。
