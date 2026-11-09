# 可选平台能力

Plinth 核心是 HTTP 内核。Postgres、Redis、jobs、migration、对象存储、session/CSRF
的 Redis store 等都在可选子包中。jobs 与 migration 的核心契约不绑定具体数据库；
PostgreSQL 是内置适配器。

> 伪代码；展示 import 与生命周期边界，不包含具体业务队列或表结构。

## 选择能力

| 需要 | import |
|---|---|
| PostgreSQL pool | `platform/postgres` |
| Redis client | `platform/redis` |
| durable jobs（PostgreSQL） | `platform/jobs` + `platform/jobs/postgres` |
| durable jobs（其他数据库） | `platform/jobs` + 应用自己的 Store |
| migrations（PostgreSQL） | `platform/migrate` + `platform/migrate/postgres` |
| migrations（其他数据库） | `platform/migrate` + 应用 import 的 `database/sql` driver |
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

`platform/jobs` 定义数据库无关的 `Job`、`Lease`、`Schedule` 与 Store 接口，import 它
不会带入 pgx。内置 PostgreSQL 适配器可以这样接线：

```go
store := jobspostgres.NewStore(pg.Pool())
worker := jobs.NewWorker(store, registry, logger, cfg.Worker)
scheduler := jobs.NewScheduler(store, logger, cfg.Scheduler,
    jobs.WithRegistry(registry))
```

其他数据库实现 `jobs.Store`（或 worker/scheduler 需要的窄接口），并复用
`platform/jobs/jobutil` 的严格 payload、标识符、租约和错误规范化：

```go
type jobStore struct{ db *sql.DB }

var _ jobs.Store = (*jobStore)(nil)
```

自定义 Store 必须在数据库层保证原子 claim、lease fencing、stale recovery 和 schedule
单 tick 原子分发。任务 payload 与 HTTP body 共用严格 JSON 规则；不要把 store、worker、
scheduler 塞回核心包，也不要为所有业务引入全局队列单例。

## 数据库迁移

PostgreSQL 使用便捷构造器：

```go
migrator := migratepostgres.New(cfg.DatabaseURL, cfg.AutoMigrate, logger,
    migrate.Migration{Name: "app", FS: appmigrations.FS()},
)
```

其他数据库通过 `migrate.NewWithConfig` 选择 driver、Goose dialect 和标识符上限。应用
负责匿名 import 实际使用的 `database/sql` driver：

```go
migrator := migrate.NewWithConfig(migrate.Config{
    URL:             cfg.DatabaseURL,
    Enabled:         cfg.AutoMigrate,
    Logger:          logger,
    Driver:          "mysql",
    Dialect:         goose.DialectMySQL,
    IdentifierLimit: 64,
}, migrate.Migration{Name: "app", FS: appmigrations.FS()})
```

## 对象存储

先依赖 `platform/objectstore` 的窄接口，再在部署层选择 `localfs` 或 `s3`。业务代码不要
直接依赖 AWS SDK 类型；这样测试可以注入 fake，非 S3 部署也不会链接 SDK。
