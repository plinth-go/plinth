# CORS

适用：浏览器从其他 origin 调用 API。网关已经处理 CORS 时不要重复配置。

```go
crossOrigin, _ := cors.New(cors.Config{
    AllowedOrigins:   []string{"https://app.example"},
    AllowCredentials: true,
    MaxAge:           10 * time.Minute,
})

handler := crossOrigin(router) // 必须包在整个 router 外层
httpComponent := runtime.NewHTTP(cfg.HTTPAddr, handler, nil, logger)
```

- `AllowedOrigins` 不能为空，不会隐式变成 allow-all。
- `*` 只能在不带 credentials 时使用。
- method/header 不接受 `*`；默认只放行常用 method 和 header。
- 允许的 preflight 返回 204；不允许的 origin/method/header 返回 403。
- CORS 不是认证、授权或 CSRF。
