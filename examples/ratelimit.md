# 限流

适用：限制单实例或集群的请求速率。

单实例：

```go
limiter, _ := ratelimit.NewMemory(120, time.Minute)
throttle, _ := ratelimit.New(ratelimit.Config{
    Limiter: limiter,
    Logger:  logger,
})
router.Use(throttle)
```

多实例把 limiter 换成 Redis，HTTP 接线不变：

```go
limiter, _ := ratelimitredis.New(redisClient, ratelimitredis.Config{
    Prefix:       "myapp:ratelimit:",
    Limit:        600,
    Window:       time.Minute,
    Strategy:     ratelimitredis.StrategyCommands, // EVAL 被禁用时
    RedisVersion: "7.2.4",
})
```

- 默认 key 是 operation ID 加直接连接地址，不信任 `X-Forwarded-For`。
- 代理部署或按用户限流时，显式提供 `ratelimit.Config.Key`。
- 超限返回 429 Problem 和 `RateLimit-*`/`Retry-After` header。
- limiter 故障 fail closed 为 500；固定窗口不是 DDoS 防护。
