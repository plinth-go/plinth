# 声明式接口权限

适用：权限来源由应用注入，接口只声明“需要什么权限”。

```go
authorize, _ := authz.New(authz.PermissionSourceFunc(func(ctx context.Context) ([]string, error) {
    principal, _ := auth.FromContext(ctx)
    return loadPermissions(ctx, principal.Subject) // DB、LDAP 或策略服务
}), authz.Config{Logger: logger})

protected := router.Group("/v1", authenticate, authorize) // 认证必须在授权之前
```

路由声明权限，不在 handler 里写 `if !allowed`：

```go
type DeleteItemInput struct {
    kernel.Meta `route:"DELETE /items/{id}" authz:"items:delete"`
    ID          string `path:"id" validate:"required"`
}

protected.Handle(deleteItem)
protected.Raw("POST", "/admin", adminHandler,
    kernel.RequirePermissions("admin"), kernel.Hidden())
```

- 多个权限是 all-of；权限源返回 `"*"` 表示全权。
- 缺失权限返回 403；权限源失败返回 500。
- 权限会进入 OpenAPI 的 `x-required-permissions`。
- 没有权限声明的路由不会调用权限源。
- Postgres 权限源实现见 [jwt-postgres-authz.md](jwt-postgres-authz.md)。
