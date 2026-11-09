# 最小 typed HTTP

目标：注册一个 typed endpoint，让 Plinth 负责绑定、校验、Problem 响应和 OpenAPI。

> 伪代码；省略 imports 和部分错误处理，框架类型保持真实。

## 输入与输出

```go
type CreateItemInput struct {
    kernel.Meta `route:"POST /v1/items"`

    Tenant string     `header:"X-Tenant" validate:"required"`
    Body   CreateBody `body:",required"`
}

type CreateBody struct {
    Name  string   `json:"name" validate:"required,min=1,max=80"`
    Tags  []string `json:"tags" validate:"max=10,dive,min=1,max=24"`
}

type CreateItemOutput struct {
    ID string `json:"id"`
}
```

关键点：

- `kernel.Meta` 是路由元数据载体，必须嵌入输入结构体。
- `route`、`path`、`query`、`header`、`body` 和 `validate` 都在注册期分析。
- JSON body 是严格的；重复键、未知字段、`null` 和尾随值都会失败。
- 输出结构体不需要手写 JSON 响应，也无需自己设置 `Content-Type`。

## Handler

```go
func createItem(ctx context.Context, in CreateItemInput) (CreateItemOutput, error) {
    item, err := service.Create(ctx, in.Tenant, in.Body.Name, in.Body.Tags)
    if errors.Is(err, ErrAlreadyExists) {
        return CreateItemOutput{}, kernel.Conflict("item already exists")
    }
    if err != nil {
        return CreateItemOutput{}, kernel.Internal(err)
    }
    return CreateItemOutput{ID: item.ID}, nil
}
```

预期内的 4xx 使用 `kernel.BadRequest`、`kernel.NotFound`、`kernel.Conflict`、
`kernel.Forbidden`、`kernel.Unprocessable` 等 helper。内部错误用 `kernel.Internal`，
框架会隐藏 5xx detail 并记录原始错误。

## Path 与 Query

```go
type GetItemInput struct {
    kernel.Meta `route:"GET /v1/items/{id}"`

    ID      string `path:"id" validate:"required"`
    Include string `query:"include" validate:"omitempty,oneof=meta history"`
}
```

不要为每个参数手写 `switch` 或 `strconv`。绑定、缺失值、格式错误和校验错误都由框架
统一转换成 Problem。

## 注册

```go
func routes(logger *slog.Logger) *kernel.Router {
    router := kernel.NewRouter(logger, "My API", "1.0.0")
    router.Use(
        kernel.RequestID(),
        kernel.AccessLog(logger),
        kernel.Recover(logger),
        kernel.BodyLimit(1 << 20),
    )
    router.Handle(createItem)
    router.Handle(getItem)
    return router
}
```

能用 `router.Handle` 表达的资源不要退回 raw handler。SSE、WebSocket、文件流或必须接管
`http.ResponseWriter` 的端点才使用 `router.Raw`，并显式声明 OpenAPI 选项。
