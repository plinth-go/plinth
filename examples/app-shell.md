# 应用外壳、配置和子命令

目标：让应用只保留一份配置加载和进程装配路径，同时获得 `serve`、`check`、`env`、
`help` 与自定义命令。

> 伪代码；用于展示依赖顺序，不要求逐行编译。

## 配置

```go
const EnvPrefix = "MYAPP"

type Config struct {
    HTTPAddr string `env:"HTTP_ADDR,default=:8080" validate:"required"`
    LogLevel string `env:"LOG_LEVEL,default=info" validate:"oneof=debug info warn error"`
    LogFormat string `env:"LOG_FORMAT,default=json" validate:"oneof=json text"`

    Ops kernel.OpsSettings `env:""`
}
```

根前缀只在 `app.LoadEnv` / `Application.EnvPrefix` 配置一次。嵌入的框架配置片段用
`env:""`，例如 `Ops` 自动得到 `OPS_*` segment，最终 key 是
`MYAPP_OPS_OPENAPI_ENABLED`。不要在子结构体里重复写 `MYAPP_`。

## 启动流程

```go
func main() {
    app.Main(app.Application{
        Name:        "myapp",
        Config:      &Config{},
        EnvPrefix:   EnvPrefix,
        BuildRoutes: routes,
        Serve:       run,
        Commands: []*app.Command{
            {
                Name:  "digest",
                Usage: "Run a one-off digest",
                Action: func(ctx context.Context, cmd *cli.Command) error {
                    if cmd.Args().Len() != 0 {
                        return errors.New("digest takes no arguments")
                    }
                    _, _ = fmt.Fprintln(cmd.Root().Writer, "digest placeholder")
                    return nil
                },
            },
        },
    })
}
```

`BuildRoutes` 供 `check` 和 OpenAPI 预检使用。它应尽量不要求数据库连接、Redis 或启动
后台 goroutine；真实运行时由 `Serve` 读取配置并构造同一套路由。

## Serve

```go
func run(ctx context.Context) error {
    var cfg Config
    _ = app.LoadEnv(ctx, &cfg, app.WithEnvPrefix(EnvPrefix))

    logger, _ := logging.New(logging.Config{
        Level: cfg.LogLevel, Format: cfg.LogFormat,
    })
    defer logger.Close()

    router := routes(logger.Slog())
    router.RegisterOps(cfg.Ops.Apply(kernel.OpsConfig{Logger: logger.Slog()}))

    httpComponent := runtime.NewHTTP(cfg.HTTPAddr, router, nil, logger.Slog())
    rt := runtime.New(logger.Slog(), 10*time.Second)
    rt.Add(
        // database component, cache component, workers...
        httpComponent,
    )
    return rt.Run(ctx)
}
```

组件按 `Add` 顺序启动、逆序关闭。HTTP 通常最后启动；worker、scheduler 等异步组件根据
依赖关系放在它前面。

## 命令边界

- `serve`：真正启动运行时。
- `check`：验证配置 schema、路由契约、OpenAPI 和可选 `CheckWiring`。
- `env`：从同一份配置结构生成环境模板。
- `help`：只打印帮助，不读取配置、不启动服务。
- 自定义命令：只用于应用自己的运维/一次性任务，不复制框架内置命令。
