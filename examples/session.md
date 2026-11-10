# Session / Cookie

适用：服务端保存登录状态，浏览器通过 cookie 持有 session token。

```go
store := session.NewMemoryStore() // 多实例：redissession.NewStore(redisClient, "myapp:session:")
sessions, _ := session.New(store, session.Config{
    Lifetime:    24 * time.Hour,
    IdleTimeout: 30 * time.Minute,
    Secure:      true,
})
router.Use(sessions.Middleware)
```

`sessions` 由应用注入 handler；修改会在 handler 返回后自动保存：

```go
func login(ctx context.Context, in LoginInput) (struct{}, error) {
    sessions.Put(ctx, "user_id", in.UserID)
    _ = sessions.RenewToken(ctx) // 登录成功后更换 token
    return struct{}{}, nil
}

func currentUser(ctx context.Context, _ CurrentUserInput) (CurrentUserOutput, error) {
    return CurrentUserOutput{UserID: sessions.GetString(ctx, "user_id")}, nil
}
```

- 登出调用 `sessions.Destroy(ctx)`。
- Cookie 始终 `HttpOnly`；HTTPS 部署必须 `Secure`。
- 默认 `SameSite=Lax`；`None` 需要 `Secure`，`Partitioned` 也需要 `Secure`。
- 内存 store 只适合单实例；多实例使用 `platform/redissession`。
- 只把 `sessions.Middleware` 挂到需要 session 的路由组。
- Cookie 认证的状态变更接口仍需 CSRF 防护。
