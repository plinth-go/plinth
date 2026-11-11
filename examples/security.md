# 安全中间件组合顺序

目标：只说明组合顺序；每项能力的接入看对应单篇。

| 需要 | 看 |
|---|---|
| CSRF | [csrf.md](csrf.md) |
| CORS | [cors.md](cors.md) |
| 限流 | [ratelimit.md](ratelimit.md) |
| Session / Cookie | [session.md](session.md) |
| JWT + 数据库权限 | [jwt-postgres-authz.md](jwt-postgres-authz.md) |
| 权限 tag / 自定义权限源 | [authz.md](authz.md) |

## 推荐顺序

```go
router.Use(
    kernel.RequestID(),
    kernel.AccessLog(logger),
    kernel.Recover(logger),
    kernel.BodyLimit(1 << 20),
)
router.Use(
    rateLimitMiddleware,
    sessions.Middleware,
    csrfMiddleware,
    authenticate,
    authorize,
)
registerRoutes(router)

handler := corsMiddleware(router) // CORS 在 router 外层
```

- CORS 必须在 router 外层，允许的 preflight 不进入业务路由。
- `authorize` 必须在 `authenticate` 之后，因为它依赖已注入的 Principal。
- session/CSRF 在依赖 cookie 的业务之前。
- 按 IP 限流放在认证前；按用户限流放在认证后。
- `Recover` 要覆盖后续中间件和 handler；`AccessLog` 应看到最终状态。
- 需要跳过健康检查、调整级别或附加租户等字段时，用
  `kernel.AccessLogWithConfig`：`SkipPaths` 只做精确路径匹配，前缀、状态码等动态
  条件写在 `Skip` 中；路由或 Group 可用 `kernel.SkipAccessLog()` /
  `group.SkipAccessLog()` 声明式跳过。`Hidden()` 只影响 OpenAPI，不等于跳过访问日志。
- Ops 端点默认不记录访问日志；需要时设置 `OpsConfig.LogAccess=true` 或
  `OPS_ACCESS_LOG_ENABLED=true`，但 `Group.SkipAccessLog` 仍然优先。
- 按需启用，不要因为“安全”把所有中间件无条件加到所有路由。
