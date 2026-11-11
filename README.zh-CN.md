# Plinth

[English](README.md) | [简体中文](README.zh-CN.md)

<p>
  <a href="https://github.com/plinth-go/plinth/actions/workflows/ci.yml"><img src="https://github.com/plinth-go/plinth/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/plinth-go/plinth"><img src="https://pkg.go.dev/badge/github.com/plinth-go/plinth.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License: Apache-2.0"></a>
  <a href="https://github.com/plinth-go/plinth/releases/latest"><img src="https://img.shields.io/github/v/release/plinth-go/plinth?display_name=tag&amp;sort=semver" alt="Latest release"></a>
</p>

## 项目描述

`plinth` /plɪnθ/ 是建筑里的“基座”。它对应本项目的定位：不替业务写墙和屋顶，只提供稳定、薄、可替换的底层装配面。

Plinth 是一个 **Go 1.27 应用内核**。它使用标准库 `net/http.ServeMux` 作为 HTTP 路由器，提供 typed handler、参数绑定、校验、Problem 响应、OpenAPI、进程生命周期和少量可选基础设施适配器。业务模型、权限策略、存储选择和部署拓扑留在应用自己的仓库。

Plinth 不是 Gin、Echo、Fiber 或 Huma 那类完整 Web 框架：

- 不接管业务代码结构，也不要求应用采用某套领域模型。
- 不隐藏 `net/http`、`go-playground/validator`、`pgx`、`goose` 或 `go-redis` 的既有语义。
- 可选集成全部放在独立子包中；未 import 的能力不会进入应用的依赖闭包。

## 功能概览

| 层 | 提供的内容 |
|---|---|
| HTTP | typed handler、`path`/`query`/`header`/`body` 绑定、validator 校验、slug、RFC 9457 Problem、动态 header、SSE、CSV/文件响应 |
| 文档 | Go 注释与 tag 驱动的 OpenAPI 3.1、内嵌 Swagger UI、启动期契约校验 |
| 运行时 | 组件按序启动/逆序关闭、HTTP server、TLS/HTTP2、readiness/liveness、结构化日志 |
| 应用外壳 | `app.Main`、`serve`/`check`/`env`/`help`、env 配置、自定义子命令、可替换静态文件 handler |
| 可选平台 | Bearer/JWT/OIDC、授权、CORS、CSRF、限流、session、幂等、上传、WebSocket、Postgres、Redis、任务队列、迁移、对象存储、Prometheus/OpenTelemetry |

需要外部基础设施的能力都在独立子包中：

- 认证与安全：`kernel/auth`、`kernel/authz`、`kernel/cors`、`platform/csrf`、`platform/ratelimit`、`platform/session`
- 数据与任务：`platform/pgdb`、`platform/jobs`、`platform/pgjobs`、`platform/migrate`、`platform/pgmigrate`、`platform/redisclient`
- 其它集成：`kernel/wshub`、`kernel/uploads`、`platform/objectstore`、`platform/observability`

核心包 `app`、`kernel`、`runtime`、`logging`、`utils`、`webui` 不反向依赖这些可选包。

## 示例

### 最小服务

```sh
go run ./examples/hello
curl http://127.0.0.1:8080/hello/Ada
# {"message":"hello, Ada"}
```

`examples/hello` 只依赖标准库和 Plinth 本身，是可直接运行的最小示例。

### typed endpoint

输入、输出和校验都写在普通 Go 类型上；绑定、Problem 响应和 OpenAPI 在注册期准备。

```go
type helloInput struct {
	kernel.Meta `route:"GET /hello/{name}"`

	Name string `path:"name" validate:"required,min=1,max=40"`
}

type helloOutput struct {
	Message string `json:"message"`
}

func hello(_ context.Context, in helloInput) (helloOutput, error) {
	return helloOutput{Message: "hello, " + in.Name}, nil
}

router := kernel.NewRouter(logger, "Plinth", "dev")
router.Use(
	kernel.RequestID(),
	kernel.AccessLog(logger),
	kernel.Recover(logger),
)
router.Handle(hello)
```

SSE、WebSocket、文件下载或需要直接控制 `http.ResponseWriter` 的端点使用 `kernel.Raw`，并显式声明对应的 OpenAPI 行为。

### 参考应用

```sh
docker compose up -d
cp .env.example .env
make run
```

- `http://localhost:8080/`：能力面板
- `http://localhost:8080/docs/`：Swagger UI
- `http://localhost:8080/openapi.json`：OpenAPI 3.1
- `http://localhost:8080/readyz`：依赖与后台组件就绪状态

### 示例索引

| 想了解 | 入口 |
|---|---|
| TLS + HTTP/2 部署 | [`examples/tls-http2`](examples/tls-http2) |
| typed binding、参数和错误响应 | [`examples/minimal-http.md`](examples/minimal-http.md) |
| 配置、日志、生命周期和子命令 | [`examples/app-shell.md`](examples/app-shell.md) |
| 安全中间件组合顺序 | [`examples/security.md`](examples/security.md) |
| Postgres、Redis、jobs、迁移、对象存储 | [`examples/optional-platform.md`](examples/optional-platform.md) |
| 测试业务 handler | [`examples/testing.md`](examples/testing.md) |
| 全部示例 | [`examples/README.md`](examples/README.md) |

## 开发与验证

```sh
make generate   # 刷新生成文件
make fmt
make check      # 文档/API/配置/路由契约
make test
make vet
make build
```

提交前确认公开 API 和依赖边界：

```sh
make api-check
make license-check
```

发布前运行完整门禁：

```sh
make release-check
```

## 提交与 PR

所有提交都必须包含与作者身份一致的 DCO `Signed-off-by`。首次 clone 后启用仓库内的自动补签钩子：

```sh
make hooks
```

重写历史后要重新补签，并运行与 CI 相同的检查：

```sh
git rebase --signoff origin/main
DCO_BASE=origin/main make dco-check
```

PR 应保持范围集中，说明问题、方案和兼容性影响，并至少通过 `Go 1.27.0 (minimum)`、macOS、Windows 和 `Release gate` 四项必需检查。完整规则见 [`CONTRIBUTING.md`](CONTRIBUTING.md) 与 PR 模板。

## Agent 指南

- `AGENTS.md` 提交到仓库，只放项目共性、长期有效的 AI 协作规则，不写个人偏好或机器路径。
- `AGENTS.override.md` 已加入 `.gitignore`，用于个人工具链、工作习惯和本机差异，不会提交。
- 同一目录下 `AGENTS.override.md` 会替代 `AGENTS.md`；需要两者同时生效时，把个人规则放到用户级 `~/.codex/AGENTS.md`，或把 override 放到更深层的工作目录。
- 两者都保持短小、可执行：写明确命令和边界，细节链接到 README、CONTRIBUTING 或示例，不复制长篇文档。

## 版本与许可

对外版本使用 Semantic Versioning 的 Go module tag；当前稳定版本为 **v1.2.0**。版本历史见 [`CHANGELOG.md`](CHANGELOG.md)，兼容性规则见 [`RELEASING.md`](RELEASING.md)，后续方向见 [`ROADMAP.md`](ROADMAP.md)。

安全漏洞请按 [`SECURITY.md`](SECURITY.md) 私密报告，不要提交公开 issue。

Plinth 采用 [Apache License 2.0](LICENSE)。版权与归属信息见 [NOTICE](NOTICE)，第三方包清单见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
