# 可选平台能力

Plinth 核心是 HTTP 内核。Postgres、Redis、jobs、对象存储、session/CSRF 的 Redis
store 等都在可选子包中。

> 伪代码；展示 import 与生命周期边界，不包含具体业务队列或表结构。

## 选择能力

| 需要 | import |
|---|---|
| PostgreSQL pool | `platform/postgres` |
| Redis client | `platform/redis` |
| durable jobs | `platform/jobs` + pgx pool |
| Redis lock | `platform/redislock` |
| Redis 限流 | `platform/ratelimit/redis` |
| Redis CSRF/session store | `platform/csrf/redis`、`platform/session/redis` |
| 对象存储 | `platform/objectstore` + `localfs` 或 `s3` |
| JWT/OIDC | `platform/jwt`、`platform/oidc` |

只 import 自己需要的能力。核心 `app`、`kernel`、`runtime`、`logging`、`utils`、`webui`
不会反向 import 这些适配器。

## 生命周期

```go
func run(ctx context.Context) error {
    var cfg Config
    _ = app.LoadEnv(ctx, &cfg, app.WithEnvPrefix("MYAPP"))

    pg, _ := postgres.New(ctx, cfg.DatabaseURL)
    cache, _ := redis.New(cfg.RedisURL)

    logger := newLogger(cfg)
    router := routes(logger, pg.Pool(), cache.Client())
    httpComponent := runtime.NewHTTP(cfg.HTTPAddr, router, nil, logger)

    rt := runtime.New(logger, cfg.ShutdownTimeout)
    rt.Add(
        // migrations, workers, scheduler...
        pg,
        cache,
        httpComponent,
    )
    return rt.Run(ctx)
}
```

组件按 `Add` 顺序启动，逆序关闭。数据库/缓存通常先于 HTTP；worker/scheduler 依赖数据库，
应按真实依赖排序。异步故障通过组件的 `FailureReporter` 交给 runtime，不要另建第二套
生命周期管理器。

## Redis 兼容性

- `platform/redislock` 默认 Lua，禁用 EVAL 时使用 `StrategyCommands`。
- `platform/ratelimit/redis` 同样支持 Lua/命令策略。
- 两者都可在 `Config.RedisVersion` 中显式声明服务端版本，启动期拒绝不兼容部署。
- CSRF/session 的 Redis store 使用简单命令，不依赖 Lua。

## Jobs

`platform/jobs` 是 Postgres-backed 队列，import 它本身就会带入 pgx。任务 payload 和
HTTP body 共用严格 JSON 规则。不要把 jobs 的 store、worker、scheduler 塞回核心包，也
不要为所有业务引入一个全局队列单例。

## 对象存储

先依赖 `platform/objectstore` 的窄接口，再在部署层选择 `localfs` 或 `s3`。业务代码不要
直接依赖 AWS SDK 类型；这样测试可以注入 fake，非 S3 部署也不会链接 SDK。
