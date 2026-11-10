# CSRF

适用：浏览器 Cookie/session 认证，需要保护 `POST`、`PUT`、`PATCH`、`DELETE`。

## 接入

```go
store := csrf.NewMemoryStore() // 多实例：rediscsrf.NewStore(redisClient, "myapp:csrf:")
protect, _ := csrf.New(csrf.Config{
    Store:  store,
    Secure: true,
})
router.Use(protect)
```

安全方法会自动签发或复用 token。增加一个 bootstrap 端点交给前端：

```go
type CSRFTokenInput struct {
    kernel.Meta `route:"GET /v1/csrf-token"`
}

type CSRFTokenOutput struct {
    Token string `json:"token"`
}

func csrfToken(ctx context.Context, _ CSRFTokenInput) (CSRFTokenOutput, error) {
    return CSRFTokenOutput{Token: csrf.TokenFromContext(ctx)}, nil
}

router.Handle(csrfToken)
```

状态变更请求必须同时携带 Cookie 和 header：

```http
GET /v1/csrf-token
=> Set-Cookie: plinth_csrf=...
=> {"token":"..."}

POST /v1/items
Cookie: plinth_csrf=...
X-CSRF-Token: ...
```

默认 cookie 为 `plinth_csrf`，header 为 `X-CSRF-Token`，TTL 为 1 小时。
多实例部署只需替换 store：

```go
store, _ := rediscsrf.NewStore(redisClient, "myapp:csrf:")
```

`SameSite=None` 必须同时启用 `Secure`。token 只通过响应体交付时，可以设置
`HttpOnly: true`。CSRF 不是认证，也不是 CORS。
