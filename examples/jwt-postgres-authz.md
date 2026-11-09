# JWT + Postgres 权限鉴权

适用：Bearer JWT 确认身份，Postgres 读取权限，接口通过 `authz` tag 做 all-of 校验。

## 配置

```go
type Config struct {
    HTTPAddr    string `env:"HTTP_ADDR,default=:8080"`
    JWTSecret   string `env:"JWT_SECRET,required"`
    DatabaseURL string `env:"DATABASE_URL,required"`
}
```

`app.LoadEnv(ctx, &cfg, app.WithEnvPrefix("MYAPP"))` 读取 `MYAPP_JWT_SECRET` 和
`MYAPP_DATABASE_URL`。HMAC secret 至少 32 字节。

## 鉴权链路

```go
func run(ctx context.Context) error {
    var cfg Config
    _ = app.LoadEnv(ctx, &cfg, app.WithEnvPrefix("MYAPP"))
    logger := slog.Default()

    pg, _ := postgres.New(ctx, cfg.DatabaseURL)
    verifier, _ := jwt.NewVerifier(jwt.Config{
        Secret:     []byte(cfg.JWTSecret),
        Algorithms: []string{"HS256"},
    })
    authenticate, _ := auth.New(verifier, auth.Config{}, logger)
    authorize, _ := authz.New(authz.PermissionSourceFunc(func(ctx context.Context) ([]string, error) {
        principal, _ := auth.FromContext(ctx)
        return loadPermissions(ctx, pg.Pool(), principal.Subject)
    }), authz.Config{Logger: logger})

    router := kernel.NewRouter(logger, "My API", "1.0.0")
    v1 := router.Group("/v1", authenticate, authorize) // 认证必须在授权之前
    v1.Handle(getItem)

    rt := runtime.New(logger, 10*time.Second)
    rt.Add(pg, runtime.NewHTTP(cfg.HTTPAddr, router, nil, logger)) // Postgres 先启动，HTTP 最后启动
    return rt.Run(ctx)
}
```

## 权限查询

```go
func loadPermissions(ctx context.Context, pool *pgxpool.Pool, subject string) ([]string, error) {
    rows, _ := pool.Query(ctx, `SELECT permission FROM principal_permissions WHERE principal_id = $1`, subject)
    return pgx.CollectRows(rows, pgx.RowTo[string])
}
```

SQL 和表结构由应用定义，这里只展示权限来源的接法。

## 接口声明

```go
type GetItemInput struct {
    kernel.Meta `route:"GET /items/{id}" authz:"items:read"`
    ID          string `path:"id" validate:"required"`
}
```

- 多个逗号分隔权限是 all-of；权限源返回 `"*"` 表示全权。
- JWT 缺失或无效返回 401；权限不足返回 403；权限源失败返回 500。
- 权限会进入 OpenAPI 的 `x-required-permissions`。
- 只有受保护接口放进这个 group；公开接口使用不带认证的 group。
