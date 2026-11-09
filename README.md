# Plinth

`plinth` /plɪnθ/ 是建筑里的“基座”。这个名字对应本项目的定位：不替业务写墙和屋顶，只提供一个稳定、薄、可替换的底层装配面。

它不是 Gin/Huma 那类完整 Web 框架，而是一个由小型公开包组成的 **Go 1.27 应用内核**。应用通过正常的 `go import` 引入 `kernel`、`runtime`、`app` 等核心薄层，并按需引入 WebSocket、Postgres、Redis 或迁移子包，再在自己的仓库里组织业务代码。

## 快速入口

| 想做什么 | 先看 |
|---|---|
| 5 分钟写一个 typed endpoint | [`examples/minimal-http.md`](examples/minimal-http.md) |
| 配置、日志、`app.Main`、自定义子命令 | [`examples/app-shell.md`](examples/app-shell.md) |
| JWT 认证 + 数据库权限 + 接口鉴权 | [`examples/jwt-postgres-authz.md`](examples/jwt-postgres-authz.md) |
| 只用声明式权限 tag | [`examples/authz.md`](examples/authz.md) |
| 只接入 CSRF | [`examples/csrf.md`](examples/csrf.md) |
| 只接入 CORS | [`examples/cors.md`](examples/cors.md) |
| 只接入限流 | [`examples/ratelimit.md`](examples/ratelimit.md) |
| 只接入 session/cookie | [`examples/session.md`](examples/session.md) |
| 安全中间件组合顺序 | [`examples/security.md`](examples/security.md) |
| 测试自己的业务 handler | [`examples/testing.md`](examples/testing.md) |
| 选择 Postgres/Redis/jobs/对象存储 | [`examples/optional-platform.md`](examples/optional-platform.md) |
| 让 AI/代码代理快速定位资料 | [`llms.txt`](llms.txt) |

`examples/` 是**文档型示例**，允许伪代码，不是可运行工程，也不包含业务模型。它们跟随框架
API 同版本演进；可运行的真实业务应用不放进框架模块，避免示例变成另一套需要维护的产品。
除非专门演示错误处理，示例默认返回的 error 为 nil，并用 `_` 接收。

## 已实现

- `net/http.ServeMux` HTTP 服务，不使用 Gin、Echo、Fiber。
- Go 1.27 泛型路由：输入输出类型直接写在 handler 签名里，由 `Handle` 统一注册。
- 启动时一次反射分析，请求期执行预编译绑定闭包，不在每个请求里扫描类型。
- `path` / `query` / `header` / `body` 参数绑定，`validate` tag 校验。
- 统一 RFC 9457 `application/problem+json` 错误响应。
- 自动 slug：`slug:"title,max=80"`，绑定后、校验前生成。
- Go 注释自动进入 OpenAPI/Swagger：不手写注解，不运行时读源码。
- OpenAPI 3.1 文档与内嵌 Swagger UI。
- OpenAPI 文档按路由注册版本自动失效；先取文档、后注册路由时，下一次取文档会包含新路由。
- Postgres 持久化后台任务：领取、并发、重试、退避、stale 恢复、优雅停机。
- 持久化周期调度声明：代码声明、Postgres 保存下一次执行时间。
- 可选的 Postgres `pgxpool`、Redis `go-redis` 生命周期子包。
- 可选的 Goose 嵌入式 SQL migration 子包。
- 可选的 Redis 分布式锁（Lua/WATCH 双策略、版本约束）和幂等键中间件。
- 可选的 Bearer auth 契约、JWT/OIDC verifier、流式 multipart 上传。
- 可选的 localfs/S3 对象存储、failed-job 运维 handler、独立 migration CLI。
- 可选的 Prometheus 指标、OpenTelemetry tracing 和 jobs observer 钩子。
- SSE、WebSocket、CSV、附件、单文件下载 helper。
- 动态 response header。
- 内嵌 HTML/JS/CSS 文件系统，可用 `STATIC_DIR` 覆盖。
- 应用可注册自定义子命令，复用 `urfave/cli/v3` 的 flag、别名和嵌套命令。
- 显式 env 配置、启动顺序、逆序关闭、shutdown timeout、可配置 HTTP server policy、
  TLS 证书、HTTP/2、可注入 listener/server 回调、终端/文件结构化日志、readiness/liveness。

`internal/api` 与 `/demo/*` 是参考应用和能力验证，不承载业务模型。框架使用示例统一位于
[`examples/`](examples/README.md)。`plinth-example` 是独立的真实业务集成参考，用来验证
`replace`、完整启动链、Redis/PG 和流式能力；它当前暂未随框架发布，也不是官方学习入口，
不应被当作推荐的项目结构复制。

## 在另一个模块中使用

把本仓库保留为独立模块，在业务项目中直接 import：

```go
require github.com/plinth-go/plinth v1.0.0

replace github.com/plinth-go/plinth => ../plinth
```

```go
import (
	"github.com/plinth-go/plinth/app"
	"github.com/plinth-go/plinth/logging"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/runtime"
	"github.com/plinth-go/plinth/utils"
	"github.com/plinth-go/plinth/webui"
	// 按需引入：
	// "github.com/plinth-go/plinth/kernel/websocket"
	// "github.com/plinth-go/plinth/kernel/auth"
	// "github.com/plinth-go/plinth/kernel/authz"
	// "github.com/plinth-go/plinth/kernel/cors"
	// "github.com/plinth-go/plinth/kernel/uploads"
	// "github.com/plinth-go/plinth/platform/csrf"
	// "github.com/plinth-go/plinth/platform/csrf/redis"
	// "github.com/plinth-go/plinth/platform/jobs"
	// "github.com/plinth-go/plinth/platform/jobs/migrations"
	// "github.com/plinth-go/plinth/platform/jobs/admin"
	// "github.com/plinth-go/plinth/platform/idempotency"
	// "github.com/plinth-go/plinth/platform/jwt"
	// "github.com/plinth-go/plinth/platform/oidc"
	// "github.com/plinth-go/plinth/platform/objectstore/localfs"
	// "github.com/plinth-go/plinth/platform/objectstore/s3"
	// "github.com/plinth-go/plinth/platform/observability/prometheus"
	// "github.com/plinth-go/plinth/platform/observability/otel"
	// "github.com/plinth-go/plinth/platform/migrate"
	// "github.com/plinth-go/plinth/platform/postgres"
	// "github.com/plinth-go/plinth/platform/ratelimit"
	// "github.com/plinth-go/plinth/platform/ratelimit/redis"
	// "github.com/plinth-go/plinth/platform/redis"
	// "github.com/plinth-go/plinth/platform/redislock"
	// "github.com/plinth-go/plinth/platform/session"
	// "github.com/plinth-go/plinth/platform/session/redis"
)
```

## 可选子包与依赖边界

需要外部基础设施的集成放在同一个 Go module 的可选子包中；应用只用 import 自己需要的能力：

| 子包 | 编译时依赖 | 用途 |
|---|---|---|
| `kernel/websocket` | `gorilla/websocket` | WebSocket 升级、同源检查、连接登记 |
| `kernel/auth` | 无 | Bearer 认证中间件契约与 Principal context |
| `kernel/authz` | 无 | 路由权限声明、all-of 授权中间件与 403/500 Problem |
| `kernel/cors` | 无 | 显式 allowlist CORS 与 preflight |
| `kernel/uploads` | 无 | 有界、流式 multipart 文件解析与临时文件清理 |
| `platform/csrf` | 无 | CSRF 中间件与有界内存 store |
| `platform/csrf/redis` | `go-redis/v9` | 无 Lua 的 Redis CSRF token store |
| `platform/jobs` | `pgx/v5` | PostgreSQL 持久化任务、worker、scheduler |
| `platform/jobs/admin` | `pgx/v5`（经由 jobs） | failed job 查看、重试和删除 HTTP handler |
| `platform/jobs/migrations` | 无 | 随框架发布的任务表 SQL |
| `platform/idempotency` | `go-redis/v9` | 内存/Redis 幂等键存储与 HTTP 中间件 |
| `platform/jwt` | `golang-jwt/jwt/v5` | HMAC/RSA/ECDSA JWT verifier，适配 `kernel/auth` |
| `platform/oidc` | `go-oidc/v3`、`go-jose/v4` | OIDC discovery、JWKS 轮换与 ID token verifier |
| `platform/redislock` | `go-redis/v9` | 带租约 token 的 Redis 分布式锁，Lua/命令双策略 |
| `platform/objectstore` | 无 | 对象存储接口与 key 规范化 |
| `platform/objectstore/localfs` | 无 | 本地文件系统对象存储 |
| `platform/objectstore/s3` | `aws-sdk-go-v2/service/s3` | S3 或 S3-compatible 对象存储适配器 |
| `platform/observability/prometheus` | `prometheus/client_golang` | Prometheus HTTP 指标中间件 |
| `platform/observability/otel` | `go.opentelemetry.io/otel` | OpenTelemetry HTTP server span 中间件 |
| `platform/postgres` | `pgx/v5` | PostgreSQL 连接池生命周期与唯一键错误识别 |
| `platform/ratelimit` | 无 | 固定窗口内存 limiter 与 HTTP 中间件 |
| `platform/ratelimit/redis` | `go-redis/v9` | Redis 固定窗口 limiter，Lua/命令策略 |
| `platform/redis` | `go-redis/v9` | Redis 客户端生命周期 |
| `platform/session` | `alexedwards/scs/v2` | cookie session 中间件与内存 store |
| `platform/session/redis` | `alexedwards/scs/v2`、`go-redis/v9` | Redis session store |
| `platform/migrate` | `goose/v3`、`pgx/v5` | 多 source Goose migration |

核心包 `app`、`kernel`、`runtime`、`logging`、`utils`、`webui` 是纯 HTTP/进程装配层：`kernel` 只负责 HTTP 入参解析、参数绑定、校验、结构体响应、OpenAPI/Swagger 和流式 helper，不依赖数据库、Redis、任务队列或 Goose。未 import 的可选子包不会参与编译或链接；`internal/depscheck` 用 `go list -deps` 固定核心包与所有 `platform/*` 的边界。

本仓库的 `cmd/plinth` 和 `internal/api` 是带全部能力的参考应用，因此它们会 import 可选平台包；消费者不会 import `internal/*`，最终二进制只由消费者自己的 import 图决定。只 import `kernel`/`runtime` 的应用不会链接 SCS、pgx、Redis、gorilla 或 Goose。

这些子包仍在同一个 module 中，因此发布 tag 和 MVS 版本边界只有一个。Go 1.27 的 module graph pruning 会让最小消费者的 `go.mod`/`go.sum` 只记录实际 import 闭包所需的依赖；`go list -m all` 仍可能列出可选模块，但编译、链接和最终二进制不会包含未 import 的包。只有连模块下载图也要完全隐藏时才需要拆多个 module，这会引入多 tag、多版本兼容和跨模块测试成本，当前首版不值得这样做。`platform/jobs` 是 Postgres 持久化队列，import 它本身就会使用 pgx，这是它的实现前提；只使用 HTTP 核心的应用完全不会带入 pgx。

## 版本与发布

对外版本使用 Semantic Versioning 的 Go module tag；开发记录里的 `vNNN` 等编号只是内部
迭代号，不是 release version。首个稳定发布目标是 **`v1.0.0`**，公开 API 和兼容性规则见
[`RELEASING.md`](RELEASING.md)，版本历史见 [`CHANGELOG.md`](CHANGELOG.md)。

发布候选的标准验证入口是：

```sh
make generate
make fmt
make release-check
```

`make release-check` 包含文档/API 新鲜度、路由与 OpenAPI 契约、普通测试、vet、build、
完整 `-race` 测试和两个 fuzz 目标。发布还要求参考应用通过同一组 gate，并按
[`SECURITY.md`](SECURITY.md) 复核 TLS/代理、静态文件、上传、CORS、认证/授权、CSRF、
session、限流和 WebSocket 部署边界。

## 应用命令

`app.Main` 内置 `serve`、`check`、`env`、`help`。应用可以通过
`Application.Commands` 增加自己的子命令；命令解析、flag、别名和嵌套子命令由
轻量的 `github.com/urfave/cli/v3` 负责，框架只做注册校验和生命周期接线。

```go
func main() {
	app.Main(app.Application{
		Name:        "shelf",
		Config:      &config.Config{},
		BuildRoutes: api.Routes,
		Serve:       run,
		Commands: []*app.Command{{
			Name:  "seed",
			Usage: "Load demo data",
			Flags: []app.Flag{
				&cli.BoolFlag{Name: "dry-run", Usage: "validate without writing"},
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				if command.Args().Len() != 0 {
					return errors.New("seed takes no arguments")
				}
				_, err := fmt.Fprintln(command.Root().Writer, "seed dry-run:", command.Bool("dry-run"))
				return err
			},
		}},
	})
}
```

自定义命令名和别名不能和 `serve`、`check`、`env`、`help` 冲突，且必须唯一；注册
错误会在任何命令执行前返回。命令拿到的是带 SIGINT/SIGTERM 的 `context.Context`；
输出使用 `command.Root().Writer`，诊断使用 `command.Root().ErrWriter`。嵌入或测试时
用 `app.RunIO` 显式注入两个 writer。

`Application.CheckWiring` 是 v175 的路由装配校验缝：`check` 在路由契约和 OpenAPI
通过后，把同一个已校验的 `*kernel.Router` 交给它。框架不会扫描源码，也不会猜测
`Serve` closure 使用了哪个 router；能确定性验证自己装配关系的应用才实现这个钩子，
错误或 panic 都会让 `check` 失败。

## 测试业务逻辑

Plinth 不提供测试 DSL，也不要求启动完整 runtime。业务测试建议分三层：

1. 直接调用普通函数或 typed handler，验证业务规则和返回值。
2. 用 `kernel.NewRouter`（或应用的 `New` 工厂）加 `httptest.NewRequest` /
   `httptest.ResponseRecorder`，注入 fake repository、fake queue 和测试 logger，验证
   HTTP 契约、状态码、header、Problem 和事件副作用。
3. 只有需要验证 SQL、Redis、迁移或 worker 的真实行为时，才启动 Postgres/Redis 做
   集成测试；缺少外部依赖时应 `t.Skip`。

fake 依赖走应用自己的窄接口即可，例如：

```go
type links interface {
    Create(context.Context, domain.NewLink) (domain.Link, error)
}

type fakeLinks struct {
    created []domain.NewLink
}

func (f *fakeLinks) Create(_ context.Context, input domain.NewLink) (domain.Link, error) {
    f.created = append(f.created, input)
    return domain.Link{Slug: input.Slug, Title: input.Title, URL: input.URL}, nil
}
```

对于参考应用，`api.Routes()` 是 `check` 使用的无依赖路由工厂，只用于配置和 OpenAPI
预检；业务 HTTP 测试应使用 `api.New(api.Dependencies{...})`，并显式提供 fake
repository、job queue、event hub、stream registry、静态 handler 和 assets。这样测试
覆盖的是实际注册的完整路由，而不是一个省略业务依赖的缩略路由。

命令测试使用 `app.RunIO(ctx, application, args, stdout, stderr)` 注入 writer，不需要
重定向进程全局的 `os.Stdout`/`os.Stderr`。测试只验证框架注册、接线和业务行为；JSON
解析、HTTP 协议、TLS、WebSocket、slog 等标准库/三方库自身的正确性由它们的测试负责。

公开包的 API 基线保存在 `internal/apicheck/testdata/public-api.txt`。普通测试会检查
可导入的导出声明是否漂移；未导出辅助类型上的方法不属于公开 API。只有确认变更是有意的
兼容性调整时才运行 `make api-update`，并 review 生成的 diff。这个基线不替代语义版本
或人工兼容性判断。

`internal/depscheck` 另外用 `go list -deps` 验证依赖边界：核心包不能间接 import
SCS、gorilla、pgx、go-redis 或 goose，每个可选子包只能带入自己声明的三方依赖。它不是重复
测试三方库，而是防止一次普通 import 让所有应用重新背上未使用的集成依赖。

安全边界、框架负责的控制和应用/网关责任见 [`SECURITY.md`](SECURITY.md)。

框架自带两个 fuzz 入口，分别覆盖严格 JSON 绑定和任意路径 dispatch：

```sh
make fuzz
```

它们只断言框架响应不 panic、状态码有效以及成功响应仍是合法 JSON；不把标准库的解析、
路由匹配和协议协商正确性复制成第二套测试。

## 先运行

要求 Go 1.27.0 或更新；发布验证使用 Go 1.27.3。先确认 `go version` 与 `go.mod` 一致：

```sh
go version
docker compose up -d
cp .env.example .env
make run
```

打开：

- `http://localhost:8080/`：内嵌能力面板
- `http://localhost:8080/docs/`：Swagger UI
- `http://localhost:8080/openapi.json`：OpenAPI 3.1
- `http://localhost:8080/readyz`：Postgres、Redis、worker、scheduler 状态

常用命令：

```sh
make generate   # 从 Go 注释生成 OpenAPI 文档 manifest
make fmt
make check      # 文档/API 表面是否过期 + 配置/路由契约校验
make test
make api-check  # 公开 API 声明表面基线
make golden     # 仅在确认 OpenAPI 变更后刷新快照
make vet
make build
```

## 目录

```text
cmd/plinth/             应用入口：app.Main 命令分发 -> 配置 -> 组件 -> runtime
cmd/plinth-docgen/      AST 注释提取器，生成 docs manifest
kernel/                 泛型路由、绑定、校验、slug、OpenAPI、SSE/CSV
kernel/auth/            可选 Bearer 认证契约
kernel/authz/           可选声明式路由权限中间件
kernel/cors/            可选显式 allowlist CORS 中间件
kernel/uploads/         可选流式 multipart 解析
kernel/websocket/       可选 WebSocket 集成（gorilla/websocket）
platform/csrf/          可选 CSRF 中间件与内存/Redis store
platform/idempotency/   可选幂等键与 HTTP 中间件
platform/jobs/          可选 Postgres 任务队列、worker、scheduler（pgx）
platform/jobs/admin/    可选 failed-job 运维 handler
platform/jobs/migrations/ 任务表 SQL，随二进制嵌入
platform/jwt/           可选 JWT verifier（golang-jwt）
platform/oidc/          可选 OIDC/JWKS verifier（go-oidc）
platform/objectstore/   可选对象存储接口、localfs 与 S3 适配器
platform/observability/ 可选 Prometheus/OpenTelemetry 适配器
platform/postgres/      可选 PostgreSQL 生命周期组件（pgx）
platform/ratelimit/     可选固定窗口限流与内存/Redis limiter
platform/redis/         可选 Redis 生命周期组件（go-redis）
platform/redislock/     可选 Redis 分布式锁（go-redis）
platform/session/       可选 cookie session 与内存/Redis store（scs）
platform/migrate/       可选 Goose migration 与多源 migrator
runtime/                组件启动/停止与 HTTP server
logging/                终端/文件 slog 输出与生命周期
utils/                  无状态、确定性的通用小工具
webui/                  可替换的嵌入静态文件 handler
internal/api/           HTTP 路由装配与框架能力端点
internal/config/        env 显式配置
internal/generated/     go generate 产物，不手改
```

公开包只包含长期稳定的装配边界；参考应用内部结构仍放在 `internal/api`、`internal/config` 和 `internal/generated`，避免业务路由被外部模块错误复用。

## 选型边界

| 能力 | 采用 | 可选子包 | Plinth 只做什么 |
|---|---|---|---|
| HTTP | 标准库 `net/http` | `kernel` | 路由元数据、泛型绑定、统一响应 |
| 校验 | `go-playground/validator` | `kernel` | 从 tag 生成 schema，翻译错误 |
| OpenAPI | 自研窄生成器 | `kernel` | 只覆盖本项目支持的 tag 子集 |
| Swagger UI | `swaggest/swgui/v5emb` | `kernel` | 内嵌挂载 |
| Postgres | `jackc/pgx/v5` | `platform/postgres`、`platform/jobs` | 连接池组件、持久化任务 |
| Redis | `redis/go-redis/v9` | `platform/redis` | 客户端组件 |
| Redis lock | `redis/go-redis/v9` | `platform/redislock` | 租约、token 所有权、Lua/命令策略 |
| 迁移 | `pressly/goose/v3` | `platform/migrate` | 嵌入 SQL、启动时调度 |
| 后台任务 | PostgreSQL | `platform/jobs` | 队列表、领取、重试、调度、停机 |
| WebSocket | `gorilla/websocket` | `kernel/websocket` | 升级、连接登记、停机关闭 |
| 认证 | 标准库 + 应用 verifier | `kernel/auth`、`platform/jwt`、`platform/oidc` | Bearer 提取、401、Principal context |
| 授权 | 应用权限源 | `kernel/authz` | 读取路由权限声明、统一 403/500 与日志 |
| CORS | 标准库 | `kernel/cors` | 显式 origin/method/header allowlist、preflight |
| CSRF | 内存或 Redis | `platform/csrf` | token 生命周期、cookie/header 校验 |
| 限流 | 内存或 Redis | `platform/ratelimit` | 固定窗口、429 Problem、标准限流 header |
| Session | `alexedwards/scs/v2` | `platform/session` | cookie 策略、自动 load/save、内存/Redis store |
| 幂等 | 内存或 Redis | `platform/idempotency` | 请求指纹、pending/complete 回放 |
| 对象存储 | 应用选择 | `platform/objectstore`、`localfs`、`s3` | key 校验、原子本地写入、S3 适配 |
| 可观测性 | Prometheus/OTel | `platform/observability/*` | HTTP 指标与 server span 适配 |
| 上传 | 标准库 `mime/multipart` | `kernel/uploads` | 流式解析、大小限制、临时文件清理 |
| Slug | 内置规则 + `gosimple/unidecode` | `kernel` | tag 解析、音译与绑定阶段触发 |
| 配置 | `sethvargo/go-envconfig/v2` | `app` | 强类型 struct 与边界校验 |
| 命令 | `urfave/cli/v3` | `app` | 自定义子命令、flag、别名、嵌套命令 |

没有引入 Huma。不是 Huma 不好，而是它会接管路由、文档和请求生命周期的关键部分；本项目希望这些能力保持可拆、可替换、可读。OpenAPI 生成器目前只支持本文列出的 tag 子集，复杂场景可以单独替换这一个文件，而不动 HTTP 层。

## 写一个 typed endpoint

```go
type CreateInput struct {
	kernel.Meta `route:"POST /v1/items"`
	Body CreateBody `body:",required"`
}

type CreateBody struct {
	// 标题会用于生成 slug。
	Title string `json:"title" validate:"required,min=3,max=80"`
	// 留空时自动生成；传入自定义值则保留。
	Slug string `json:"slug" slug:"title,max=80" validate:"omitempty,max=80"`
}

type CreateOutput struct {
	Slug string `json:"slug"`
}

func createItem(ctx context.Context, in CreateInput) (CreateOutput, error) {
	return CreateOutput{Slug: in.Body.Slug}, nil
}

router.Handle(createItem, kernel.Tags("items"))
```

注册面只有三种：

- typed JSON、CSV、附件：输入 struct 声明 `kernel.Meta` 的 `route` tag，然后调用 `Handle`。
- SSE、WebSocket、静态文件等基础设施响应：调用 `Raw`；可见 raw route 必须声明 `Produces`，或用 `Hidden` 隐藏。
- 运维端点：调用 `RegisterOps`。

不提供 `GET`、`POST`、`JSON`、`RawFunc` 这类同义入口。路由写法选错时应当直接编译失败。
typed 的 `route` tag 和 `Raw` 的 method 必须是规范的大写 HTTP token；`get`、`Get`
或包含空白/非法字符的 method 会在注册期直接 panic，不会静默转换成另一条路由。
输入 `Meta` 只能声明 `route`，输出 `Meta` 只能声明 `status` / `produces`；把
`status`、`produces` 写到输入 Meta，或把 `route` 写到输出 Meta，都会在注册期失败，
不会被静默忽略。

注册顺序也是固定的：`SetDocs`、`Router.Use`、`Group.Use` 和 `Group.Tag` 必须在
相关路由注册前完成。路由注册后再追加中间件或 tag 会 panic，避免出现部分路由
带中间件/OpenAPI tag、部分没有的隐性行为。

注册期也会拒绝空 handler、空中间件和空 option：`Raw(nil)`、typed `Handle(nil)`、
`Use(nil)`、`Handle(..., nil)`，以及返回 nil（包括 typed-nil）的 middleware 都会
立即 panic。失败的 raw/typed 注册不会留下 operation 元数据，问题应在 `plinth check`
阶段暴露，而不是等到第一个请求。注册会先在独立的 `ServeMux` 上校验完整 pattern
和冲突，再提交到真实路由；失败不会冻结 Router 或 Group，修复配置后仍可继续注册。

中间件按声明顺序由外到内包裹。推荐顺序是
`RequestID -> AccessLog -> Recover -> BodyLimit`：这样 AccessLog 能看到 Recover
最终写出的 500 Problem 和准确字节数。AccessLog 放在 Recover 内也不会丢日志，但
panic 被外层恢复时，它记录的 `bytes` 只包含自己已经观察到的写入（通常为 0），
无法包含随后写出的 Problem 体，也无法观察最终 header。

中间件会转发 `100..199` 的 informational response，但不会把它当成最终状态；
`101 Switching Protocols` 例外，它本身就是最终响应。因此 103 Early Hints 之后的
201/500 仍会正常写出，AccessLog 记录最终状态与字节数，Recover 也能在 103 后写入 500。

`RequestID` 只在客户端恰好提供一个、长度不超过 128 字节、且全部为可打印
非空白 ASCII 的 `X-Request-ID` 时复用它；缺失、重复、超长或含控制字符的值都会
替换为新 ID。响应头、context、Problem 的 `instance` 和访问日志始终使用同一个最终值。

所有 RFC 9457 Problem 的状态码必须落在 `400..599`。如果自定义 `HTTPError`
写出了范围外状态码，`WriteProblem` 会统一降级为 500、使用标准 500 标题，并把
原始 detail 替换为 `internal server error`。所有 5xx 响应都只返回
标准状态标题和 `internal server error` detail；即使是手写的 `HTTPError.Title` /
`HTTPError.Detail` 也不会返回客户端，避免配置错误或内部异常泄露敏感信息。未知
5xx 状态码使用 `Internal Server Error` 标题兜底。

参数标签规则：

- `path:"id"` 永远必填，不需要再写 `validate:"required"`。
- `query:"page"`、`header:"Idempotency-Key"` 默认可选。
- 每个输入字段必须且只能有一个位置 tag。`path` / `header` 只接受标量；`query`
  还接受 slice，用重复参数表达，例如 `?tag=a&tag=b`。query array、map、嵌套 slice、
  未实现 `encoding.TextUnmarshaler` 的 struct，以及 `json.RawMessage` 会在 preflight
  失败，因为它们无法同时被 binder 和 OpenAPI schema 一致表达。
- 实现 `encoding.TextUnmarshaler` 的标量类型在 path/query/header 中按 string 绑定并
  生成 string schema；如果该类型本身是 slice 并实现了 `TextUnmarshaler`，整个参数
  按一个字符串解码，不会按重复参数展开。
- 只有不带 `TextUnmarshaler` 的 query slice 接受重复参数。标量 query 或 header 出现
  多个值时返回 422，不会静默选择第一个值；这避免 `?limit=1&limit=2` 这类请求产生
  不可预测的业务结果。
- path/query/header 的浮点参数与 JSON number 使用同一可表示性边界：`NaN`、`+Inf`
  和 `-Inf` 即使能被 `strconv.ParseFloat` 解析，也会作为非法值返回 422；`default`
  中出现这些值会在注册期直接失败。可选但非法的 `0`/零值仍应显式用
  `validate:"omitempty,min=1"` 等规则约束。
- header 参数名必须是合法 HTTP token，且重复检测按大小写不敏感执行；`X-Token` 和
  `x-token` 会被视为同一个参数，在路由校验阶段直接失败。
- `body:",required"` 表示请求体必填；省略则表示可选。
- body 解码拒绝重复 object key、未知字段、尾随 JSON 值，以及 payload 中任意层级的
  `null`。可选 body 的“未提供”会保留零值；可选字段用“省略”表示，不生成 nullable
  schema，也不会把 `null` 当成零值或空对象。字符串内容 `"null"` 不受影响。
  请求解码保持 `encoding/json` v1 语义（例如 `time.Duration` 仍按整数纳秒），
  仅收紧上述歧义输入。
- 只要 typed 请求实际携带 body，就必须使用 `Content-Type: application/json`；可以带
  `charset=utf-8`（大小写不敏感）；如果声明 charset，唯一允许的值是 `utf-8`，
  `utf8` 或其他字符集都返回 415。缺失或使用其他媒体类型同样返回 415，并在每个
  带 body 的 OpenAPI operation 中声明 415 Problem。重复的 `Content-Type` header 同样
  返回 415；`Content-Encoding` 只允许缺失或单个 `identity`，压缩或其他编码统一返回
  415，而不是让 JSON decoder 把编码后的字节误报成 malformed JSON。当前不接受
  `application/*+json` 作为请求 body 的替代类型，避免出现多个都“差不多正确”的入口。
- nil 或 typed-nil request body reader 按“无字节”处理：required body 返回 422，
  optional body 保留零值，不会把 nil reader 传给 JSON decoder 后在请求期 panic。
- 必填性只有一种写法：`validate:"required,min=2"`。`query:"q,required"`、
  `query:"q,omitempty"` 这类位置 tag 选项会在注册时直接失败。
- 值校验统一使用 `validate:"min=1,max=100,email,uuid,oneof=..."`，可选字段用
  `omitempty` 跳过零值。
- `validate` tag 会在 `Router.Validate` 阶段解析；拼错规则名、`dive` 用错位置等
  配置错误会在启动检查时失败，不会拖到第一个请求才变成 500。
- schema 会映射的规则参数还会按字段类型做 preflight：整数/长度参数必须是无歧义的
  十进制形式（`01`、`0x10`、给整数写 `0.5`、`NaN`、`Inf` 都会失败）；`eq` / `ne`
  值非法，或 `oneof` 中混入无法按字段类型解析的值时，即使字段带 `omitempty` 也会
  直接报错。`oneof` 只支持 string、整数和无符号整数；validator 本身不支持 float /
  bool 的 `oneof`，框架不会生成一个看似可用但运行时必然失败的 schema。
- 可选参数需要默认值时使用 `default`：
  `query:"limit" default:"20" validate:"min=1"`。不传得到 `20`，传 `0` 仍然
  校验失败。显式传入空值也算“已提供”，query 和 header 都不会回退到 default。
  `default` 只允许出现在 query/header 上，不能与 `validate:"required"`
  同时使用；`Router.Validate` 还会检查默认值本身是否满足同一字段的 `validate` 规则。
  检查默认值时 `omitempty` / `omitzero` / `omitnil` 会被忽略，因为默认值一定会被注入：
  `default:"0" validate:"omitempty,min=1"` 会直接注册失败，而不是在缺省时绕过 `min=1`。
  query slice 不支持 `default`；需要缺省时在 handler 中处理，或改用一个标量参数。
- 路由声明了 query 参数时，框架会显式解析 raw query；非法 percent-encoding 等语法错误
  返回 422，不会像 `URL.Query()` 那样静默丢弃参数。没有 query 参数的路由不解读
  额外 query，因此不会因无关查询串失败。
- 当 body 字段的零值（`0`、`false`、`""`）是合法值时，用 `*T` 区分缺省和显式零值；
  `omitempty` / `omitzero` 只影响序列化，不提供请求侧的 presence 信息。
- JSON 的 `omitempty` / `omitzero` 决定响应 schema 中哪些字段不是必填，但不会改变
  请求必填性。请求侧只看 `validate:"required"`；同一校验层同时写 `required` 与
  `omitempty` / `omitzero` / `omitnil` 会在 preflight 直接报错，避免 validator
  因 tag 顺序产生相反行为。
- 注意 `encoding/json` 不会用 `omitempty` 省略 struct 或定长数组；这类字段的响应
  schema 仍会标为必填。确实希望零值 struct 被省略时使用 `omitzero`。
- body 解码会拒绝未知 JSON 字段，因此生成的 object schema 使用
  `additionalProperties: false`。JSON tag 只支持 `omitempty` / `omitzero`；
  `,string` 和拼写错误的选项会在注册时失败，不会静默生成错误 schema。
- 输入/输出 payload 除 `kernel.Meta` 外不要匿名嵌入 struct。`encoding/json` 会把它
  扁平化，而窄 schema 生成器不会；因此注册期会拒绝这种写法，请改用具名字段，
  或显式给匿名字段一个 JSON 名称。
- JSON body 和响应 schema 优先遵循线格式接口：请求字段实现
  `encoding.TextUnmarshaler`、响应字段实现 `encoding.TextMarshaler` 时按 JSON string
  记录；只实现 `json.Unmarshaler` / `json.Marshaler` 的自定义 JSON 形状无法静态推断，
  schema 使用不设约束的 `{}`，不会错误地展开底层 Go 字段。`json.Number` 映射为
  OpenAPI `number`。func、chan、complex 和 unsafe pointer 等无法被 JSON 表示的类型
  会在注册期失败，而不是等到编码响应时返回 500。
- 字段 `example` tag 按最终 wire type 解析：string 使用 tag 原文，boolean/number/
  integer 使用 JSON 标量，array/object 和自定义 JSON 类型使用完整 JSON 字面量。
  类型不匹配、非法 JSON 或非单个 JSON 值会在注册期失败，不会生成 example 类型与
  schema 不一致的 OpenAPI。
- 递归 JSON 复合类型会在回环点截断：递归 struct/map 退化为泛化的
  `type: object`，递归 slice/array 退化为泛化的 `type: array`，避免 OpenAPI 生成
  无限展开。命名递归指针别名没有可表达的 JSON 形状，会在注册期直接失败。
- JSON body 无法区分“未传”和“显式零值”，所以 body 字段不接受 `default`；
  需要默认值时使用 `*T` 并在 handler 中处理缺省。
- 只用于请求的 DTO 不需要 `json:",omitempty"`；它只影响响应序列化。
  `validate:"omitempty,min=1"` 里的 `omitempty` 是 validator 的跳过规则，不是 JSON 的，
  两者作用不同，不是重复配置。
- JSON body 字段使用通用的 `go-playground/validator`，例如
  `validate:"required,min=1,max=100"`。
- OpenAPI 按 validator 的真实语义映射常见规则：字符串的 `min` / `max` / `len` /
  `gte` / `lte` 映射为长度，数组映射为 item 数量，map 映射为 property 数量，
  数字映射为值域；数字的 `gt` / `lt` 映射为 exclusive bound，长度类型则换算成
  等价的包含边界。`dive` 之后的规则只作用于元素 schema，不会覆盖容器自身的限制。
- map 的 `dive,keys,...,endkeys` 会把 key 约束写入 `propertyNames`，把 value
  约束写入 `additionalProperties`。schema 生成目前要求 map key 是 string；非 map
  上使用 `keys` 或使用非 string key 会在注册期失败。
- 数组和 slice 上不带参数的 `unique` 会映射为 OpenAPI `uniqueItems: true`。
  `unique=Field` 这类参数化唯一性不会自动推断进 schema。
- `eq` 对标量映射为 `const`，对 slice/array/map 映射为精确的 item/property 数量；
  用在 string map key 上时会进入 `propertyNames.const`。
- `ne` 映射为 `not` 下的 `const` 或精确集合大小。`contains`、`startswith`、
  `endswith` 映射为经过 regexp 转义的 `pattern`；多个字符串条件使用 `allOf`。
- path/query/header 的 `time.Duration` 使用 `time.ParseDuration` 接受的 Go duration
  字符串（如 `5s`、`1h30m`）。OpenAPI 使用 `type: string` 加 Go duration `pattern`，
  不使用 `format: duration`，因为后者表示 ISO 8601 duration（如 `P1D`），与运行时
  绑定规则不一致。JSON body 中的 `time.Duration` 仍按 `encoding/json` 表示为整数纳秒。
- 线上表示为字符串的参数，包括 `time.Duration`、`time.Time` 和
  `encoding.TextUnmarshaler` 类型，其 OpenAPI `default` 保留原始线格式，不会写成
  解码后的 Go 对象。
- `slug:"title"` 或 `slug:"source=title,max=80,overwrite"`。
- target 和 source 必须是同一个 struct 内的 string 字段；source 可用 Go 字段名或 JSON 名引用，不能指向自身，也不能再声明 slug。
- 未知、重复、空选项和非法 `max` 在注册期失败。默认保留已有 slug；`overwrite` 强制覆盖。

需要自定义校验规则时，只通过 Router 注册：

```go
router.RegisterValidation("even", func(field validator.FieldLevel) bool {
	return field.Field().Int()%2 == 0
})

type Input struct {
	Number int `query:"n" validate:"required,even"`
}
```

`RegisterValidation` 必须在任何路由注册、`Router.Validate` 和开始服务之前调用；
路由树开始构建后再注册会直接返回错误。自定义 tag 会参与 preflight 检查与请求校验，
但不会自动映射成 OpenAPI 约束；需要文档化约束时，应同时使用标准 `min`、`max`、
`oneof` 等 tag。

非 JSON 的 typed 响应（CSV、附件等）必须同时满足两条：

- 输出类型实现 `kernel.Responder`。
- 输出类型内嵌 `kernel.Meta` 并声明 `produces:"..."`。

只满足其中一条会被 `Router.Validate` 拒绝。typed `Responder` 固定使用 HTTP 200，
状态码由响应实现自己写入，不能再通过输出 Meta 声明 201/202 等状态；需要其他状态码时
使用 `Raw`。运行时还会检查实际写出的状态码和 `Content-Type`：声明多个 `produces`
时必须显式设置 Content-Type，且必须匹配其中一个声明；声明单个具体类型时缺失 Content-Type
会使用该声明。通配符声明（`*/*`、`application/*` 等）不能作为实际响应头自动写入，
Responder 必须给出具体 Content-Type，否则返回 500 Problem。`kernel.Produces(...)`
只用于 `Raw` 端点，不再作为 typed 端点的响应类型声明。
输出类型的动态值为 typed-nil `Responder` 时直接返回 500 Problem，不会调用 nil 方法；
typed-nil `Headerer` 视为没有附加 headers，避免可选接口把正常响应变成 panic。

不需要写 `binding`、`json` 解码、字段错误映射或 OpenAPI property。

## 注释自动变 Swagger

`cmd/plinth-docgen` 用标准库 AST 扫描：

- handler/method 的 doc comment -> operation summary/description。
- struct 字段 comment -> schema property description。
- 跳过隐藏目录、`vendor`、`testdata` 和带独立 `go.mod` 的嵌套子模块；`module`
  指令支持行尾注释和带引号的模块路径。
- 输出到 `internal/generated/docs_gen.go`。
- 输出先写同目录临时文件再原子替换，生成中断不会留下半截 Go 文件。
- 命令拒绝未定义的位置参数；`-h` 与未知 flag 使用明确的退出码，不会静默忽略输入。

运行时只用 `runtime.FuncForPC` 匹配 manifest，不读源文件、不依赖代码目录。新增或修改注释后执行：

```sh
make generate
```

第一段注释作为 summary，空行后作为 description。字段注释示例：

注释匹配依赖命名函数或命名方法；匿名闭包没有可扫描的 Go doc comment，此时仍可用 `kernel.Summary("...")` 显式覆盖。

```go
type Job struct {
	// Lifecycle state: pending, running, succeeded, or failed.
	Status string `json:"status"`
}
```

生成的 Swagger 中会直接出现字段说明。

## 运维端点

`kernel.Router.RegisterOps` 默认注册并隐藏四个标准端点：

- `GET /livez`
- `GET /readyz`
- `GET /openapi.json`
- `GET /docs/`

项目只需要提供 readiness 检查和 logger：

```go
router.RegisterOps(kernel.OpsConfig{
    Logger: logger,
    Checks: []kernel.ReadyCheck{
        {Name: "postgres", Check: postgres.Ping},
        {Name: "redis", Check: redis.Ping},
    },
})
```

需要由环境控制开关时，把 `kernel.OpsSettings` 嵌入应用配置，并在注册前调用
`Apply`：

```go
type Config struct {
    Ops kernel.OpsSettings `env:""`
}

router.RegisterOps(cfg.Ops.Apply(kernel.OpsConfig{
    Logger: logger,
    Checks: checks,
}))
```

端点路径也属于同一份配置，默认值为 `/livez`、`/readyz`、`/openapi.json` 和
`/docs/`，通过 `OPS_LIVENESS_PATH`、`OPS_READINESS_PATH`、`OPS_OPENAPI_PATH` 和
`OPS_DOCS_PATH` 修改。`Apply` 会用非空 settings 路径覆盖 `OpsConfig` 中的对应路径；
如果完全用代码注册、不走 `OpsSettings`，仍可直接设置 `OpsConfig` 的路径字段。

需要自定义路径或响应时，覆盖配置项即可：

```go
router.RegisterOps(kernel.OpsConfig{
    LivePath:    "/health",
    ReadyPath:   "/health/ready",
    OpenAPIPath: "/spec",
    DocsPath:    "/manual/",
    Timeout:     3 * time.Second,
    Logger:      logger,
    Checks:      checks,
    Ready:       customReadyHandler, // 可选，完全接管 /health/ready
})
```

`kernel.Livez`、`kernel.Readyz`、`router.OpenAPIHandler` 和 `kernel.WriteJSON` 都是公开的，
需要完全手写端点时可以直接组合。Swagger UI 默认由 `RegisterOps` 挂载，`Docs` 可以覆盖
默认 handler，`DocsPath` 可以修改路径。默认 livez、readyz、OpenAPI JSON 和 Swagger UI
都返回 `Cache-Control: no-store` 和 `X-Content-Type-Options: nosniff`；即使
替换了自定义 handler，框架也会在响应提交前强制这两个头，避免受保护 Group
上的响应被共享缓存保存后回放。

`kernel.WriteJSON` 会先完整编码再提交 header；状态码必须允许响应体（不能是 1xx、
204、205、304），编码失败不会留下半截 200 响应。body 写入会检测短写并返回
`io.ErrShortWrite`。typed JSON、Problem 和 OpenAPI 响应在提交后发生写失败时会记录
日志，但不会尝试追加第二个响应。

`OpsConfig` 的 `DisableLive`、`DisableReady`、`DisableOpenAPI` 是运行时注册开关，
零值保持启用。环境配置使用正向的 `LivenessEnabled`、`ReadinessEnabled` 和
`OpenAPIEnabled`，最终由 `Apply` 转换。例如生产环境完全关闭 readiness 和 OpenAPI：

```go
router.RegisterOps(kernel.OpsConfig{
    DisableReady:   true,
    DisableOpenAPI: true,
})
```

`RegisterOps` 会先预检本次启用路由的最终方法、路径和 `Docs` handler，再注册任何端点。
同一组配置里的重复 GET 路径、与既有路由冲突，或 typed-nil `Docs` 都会在注册期
直接 panic；失败时不会留下 livez、readyz、OpenAPI 或 Swagger UI 中的任何一部分。

如果生产环境仍需要 readiness，但只能通过内网鉴权访问，可以只把端点注册到受保护的 Group。
Group 的中间件会先于端点执行，最终路径是 `Group` 前缀与默认路径的组合：

```go
internal := router.Group("/internal", requireOpsAuth)
internal.RegisterOps(kernel.OpsConfig{
    DisableLive: true,
    Logger:      logger,
    Checks:      checks,
})
```

上面的配置提供 `GET /internal/readyz`、`GET /internal/openapi.json` 和
`GET /internal/docs/`，不会提供公开的 `GET /readyz`、`GET /openapi.json` 或
`GET /docs/`。`DisableOpenAPI` 会同时关闭 OpenAPI 和 Swagger UI。也可以直接使用
`kernel.Readyz` 和 `router.OpenAPIHandler` 配合 `Group.Raw` 手动注册。

readiness check 名称在去除首尾空白后必须唯一；未命名 check 会得到稳定的
`check_1`、`check_2` 等名称。重复名称会在 handler 构造时直接 panic，避免
`checks` map 中一个依赖结果静默覆盖另一个。每个检查独立执行并隔离 panic：
某个依赖检查 panic 只会得到 `down`，后续检查仍会继续；返回 typed-nil error
也会被转换成具体的失败原因，日志不会解引用 nil。

## 后台任务

PG 表结构在 `platform/jobs/migrations/00001_jobs.sql` 和后续增量 migration 中，任务状态：

```text
pending -> running -> succeeded
                    -> pending (retry)
                    -> failed  (max attempts)
```

关键行为：

- 领取使用 `FOR UPDATE SKIP LOCKED`，多实例安全。
- worker 启动时立即执行 stale recovery，运行期再按 `StaleAfter/2` 周期执行（限制在 100ms 到 1m 之间）；仍有 attempt 余额的任务回到 pending，已经耗尽 `max_attempts` 的任务直接进入 failed，绝不额外执行一次。这样单个实例崩溃后，无需等待其他进程重启也能回收遗留租约。`RecoverStale` 拒绝非正的 stale 时长，避免把全部 running 任务误判为过期。
- 任务完成、失败和 interrupted 都带租约围栏；每次 `Claim` 还会生成独立的 `lease_token`，终态写入必须同时匹配 job、worker、attempt 和 token。即使 interrupted 回退了 attempt、下一次领取复用了相同 attempt 数字，旧 worker 的迟到写入也只能得到 `ErrLeaseLost`。被 stale recovery 回收后的旧 worker 同样无法覆盖新 attempt。
- `WORKER_STALE_AFTER` 必须严格大于 `WORKER_TIMEOUT`，避免正常执行中的任务在自身超时前被重新领取。
- worker/scheduler 配置的零值使用框架默认值；负数或互相矛盾的配置会在构造期直接 panic，
  不做静默 clamp。`Enqueue` 的 max attempts 和 `DispatchDueSchedules` 的 limit 小于 1
  会返回错误。
- 失败采用指数退避，受 `WORKER_RETRY_BASE` / `WORKER_RETRY_MAX` 控制。
- worker 停机先停止 claim，再等待 in-flight；超过整体 shutdown deadline 才取消任务。
- 被取消的在途任务归还队列，不消耗一次 attempt。
- `Enqueue` / `Schedule` 的 nil payload 会持久化为 `{}`，不会写成被严格 decoder 拒绝的 `null`；scheduler 启动时先预检全部声明 payload，再开始写库。
- `Enqueue` / `Schedule` 在落库前还会用同一严格 JSON 策略检查序列化结果；嵌套 `null`、重复 object key 和尾随值会直接报错，不会留下 worker 必然无法解码的任务。
- `Enqueue` 遇到 nil option 会在触库前返回带位置索引的错误；`ReconcileSchedules` 拒绝重复的 schedule name，不会让同一批声明静默 last-wins。
- job kind、schedule 的 name/kind 和 worker ID 会去除首尾空白后再注册、查询、入队或持久化；`" task "` 与 `"task"` 始终是同一个标识符。标识符必须是合法 UTF-8、只含可打印字符且不超过 255 字节，纯空白、控制字符、换行和超长值都会在触库前报错。
- `Store.Get` 和终态租约只接受合法 UUID，job/worker 标识会先规范化再触库；`DisableSchedulesExcept` 同样会 trim、拒绝空名称和重复名称。`max_attempts` 必须落在 Postgres `integer` 范围内。
- worker/scheduler 的常驻循环 panic 会通过 `runtime.FailureReporter` 上报并让组件变为未就绪；普通存储错误、handler 失败和 `ErrLeaseLost` 不会被误判为组件崩溃。任务投递语义是 at-least-once，业务 handler 必须可重复执行。
- 终态 ack 带租约围栏，可安全小范围重试：临时数据库错误最多重试 3 次；`ErrLeaseLost` 立即停止，不与其他 worker 竞争已回收的任务。
- worker 与 scheduler 日志固定携带 `component=worker` / `component=scheduler`。每个 Worker 实例还会生成独立身份，并发 worker ID 形如 `jobs-worker-<instance-uuid>-1`，多进程日志不再混淆。终态 ack 的租约丢失、重试耗尽和最终失败都带 `job_id`、`kind`、`worker`、`attempt`、`max_attempts`；ack 重试耗尽不会把组件误判为崩溃，任务会在 stale recovery 后重新领取。
- 终态 ack 重试耗尽不会卡住 worker loop，也不会翻转 readiness；它以带完整上下文的 ERROR 日志暴露，随后由 stale recovery 重新领取。v1 不内置 Prometheus/metrics，生产告警应采集这类日志、running 任务年龄和 scheduler 延迟；metrics/OpenTelemetry 作为边缘组件接入。
- 终态确认对提交歧义是幂等的：数据库已经写成 `succeeded`、`pending` 或 `failed`，但客户端没收到响应而重试时，同一 job、attempt、lease token 和错误原因的第二次确认返回成功；不同 token、不同 attempt 或其他状态仍返回 `ErrLeaseLost`。
- `last_error` 与失败日志使用同一个 4096 字节上限；非法 UTF-8 会先替换为 `U+FFFD`，超长错误再按 UTF-8 边界截断并追加 `[truncated]`。handler 返回坏字节、超大错误或 panic stack 都不会无上限写入数据库和日志；截断是确定性的，因此同一失败的提交歧义重放仍可识别。
- scheduler 启动通过单个事务 reconcile 声明：先预检全部 payload，再一次性 upsert 并禁用旧声明；不会出现部分 schedule 已更新的中间状态。
- 到期 schedule 的分发在同一事务内完成行锁、job insert 和 `next_run_at` 推进；并发 dispatcher 对同一个 tick 只会产生一个 job。进程停机期间错过的多个 interval 不会补跑，恢复后从当前时间重新计时。
- 如果 `Commit` 的响应在网络故障中丢失，调用方无法仅凭错误判断事务是否已提交；该操作仍可安全重试：已提交的 tick 已推进 `next_run_at`，重试不会再插入 job；未提交的 tick 保持到期，下一次扫描会重试。scheduler 把该错误记录为 `dispatch schedules` 并继续运行，不会为不确定结果做第二次 insert。
- 同一进程同时运行 worker 时，用 `jobs.WithRegistry(registry)` 构造 scheduler。启动会在触库前确认每个声明的 kind 已注册，并用 handler 的精确 payload 类型执行同一套严格解码；未注册 kind 或类型不匹配会直接启动失败。只生产 schedule、不承载 handler 的进程可以省略该选项。
- HTTP 入队先调用 `jobs.Registry.ValidatePayload`，使用目标 handler 的类型和同一严格 decoder 预检 payload；结构错误立即返回 422，不会留下必然失败的 durable job。
- scheduler 只把到期 declaration 转成 job，不在进程内保存业务定时器。

投递语义是 **at-least-once**：任务完成与 `MarkSucceeded` 之间如果进程崩溃，重启后可能再次执行。业务 handler 必须按幂等设计，并尊重传入的 `context.Context`；框架不能强制终止一个忽略取消信号的函数。

注册一个 typed job：

```go
type ReportPayload struct {
	AccountID string `json:"account_id"`
}

jobs.MustRegister(registry, "report.generate", func(ctx context.Context, p ReportPayload) error {
	return generateReport(ctx, p.AccountID)
})

job, err := store.Enqueue(ctx, "report.generate", ReportPayload{AccountID: "a_123"})
```

`jobs.Registry` 的零值可直接使用：第一次注册会初始化内部索引。nil registry 的
`Register` / `ValidatePayload` 返回错误，`Handler` / `Kinds` 返回空结果，不会 panic。
`jobs.NewStore(nil)` 可用于只构建路由/文档的 `check` 路径；任何数据库方法在触库前
返回 `jobs.ErrStoreUnavailable`，不会产生 nil pool panic。

typed job payload 与 HTTP body 使用同一套严格 JSON 规则：重复 object key、未知字段、
尾随 JSON 值和任意层级的 `null` 都会被拒绝；`time.Duration` 等值语义仍保持
`encoding/json` v1 行为。不要为任务另写一套宽松 decoder。

`jobs.Register` 还会在注册期检查 payload 的 Go 类型是否可由 JSON 解码。`chan`、
`func`、`complex`、`unsafe.Pointer`、非法 map key、带方法的非空 interface，以及藏在结构体字段、slice/map/pointer
里的同类问题都会直接返回错误；实现 `json.Unmarshaler` 或 `encoding.TextUnmarshaler`
的类型按自定义 wire format 放行。注册失败不会留下半登记的 handler。

声明周期任务：

```go
err := scheduler.Declare(jobs.Schedule{
	Name:     "report-daily",
	Kind:     "report.generate",
	Payload:  ReportPayload{AccountID: "all"},
	Interval: 24 * time.Hour,
})
```

声明在启动时 upsert 到 Postgres；重启不会重置已经等待的 `next_run_at`。
调度间隔以 Postgres integer 秒存储，因此必须是整秒、至少 1 秒，且不能超过
Postgres int 上限；`500ms`、`1500ms` 和超大 interval 都会在 `Declare` /
`UpsertSchedule` 阶段直接报错，不会静默取整。

应用入口内置了一个纯诊断 kind `system.echo`，用于手工验证完整链路，不承载业务：

```sh
curl -sS -X POST http://localhost:8080/v1/jobs \
  -H 'content-type: application/json' \
  -d '{"kind":"system.echo","payload":{"message":"hello"}}'

curl -sS http://localhost:8080/v1/jobs/<返回的-id>
```

worker 日志会输出 `system echo job`，任务状态最终变为 `succeeded`。

## 流式与文件

SSE 和 WebSocket 共用 `StreamRegistry`。HTTP 关闭时先取消所有长连接，再执行 `http.Server.Shutdown`，避免 shutdown 被无限拖住。`StreamRegistry` 零值可直接使用；nil `*StreamRegistry` 按已关闭处理：`Add` 返回失败、`CloseStreams` 安全空操作，SSE/WebSocket 在提交流式响应前返回 503。零值注册表遇到 panic 的 closer 时使用默认 logger 记录，不会因 nil logger 二次 panic。真实 `net/http.Server` 集成测试覆盖 SSE 和 WebSocket 的 shutdown 路径：`CloseStreams` 必须先让回调退出或关闭升级连接，`Shutdown` 才能在给定 deadline 内返回。
每个 closer 独立执行并捕获 panic：单条坏连接不会阻止其余连接关闭或中断停机。

SSE/WebSocket callback 不能为 nil，框架会在写出流式响应或升级协议之前拒绝；已关闭的 registry 不再接受任何新连接。nil/typed-nil response writer、nil request、nil WebSocket hub 也会在写响应前返回错误，SSE 的 nil receiver 不会解引用；`kernel/websocket.NormalizeError` 把 typed-nil error 视为 nil。WebSocket 的 `Origin` 必须是单个、无 userinfo/path/query/fragment 的绝对 `http`/`https` origin，再按 `Host` 大小写不敏感比较；空值、多个值和非 HTTP(S) scheme 都会被拒绝，但不会信任 `X-Forwarded-*` 来推断 scheme。
WebSocket callback 的 panic 会在 hub 内隔离并记录 stack，连接使用 1011
（`CloseInternalServerErr`）关闭，`Serve` 返回具体错误；不会再被误报成正常关闭，也不会让
协议升级后的 handler 把 panic 传播回已经无法写 HTTP Problem 的通用响应链。

`SSE.Event` 会先在内存中构造完整事件再单次写出：JSON 序列化失败不会留下半条事件；
string / `[]byte` 数据里的 CR、LF 会规范化为多个 `data:` 行，不能借换行注入
`event:` 等其他 SSE 字段。`SSE.Comment` 使用同一套 CR/CRLF/LF 规范化规则，多行注释
会作为一个注释块输出，不会把合法换行错误拼接到上一行。

初始响应头 flush 以及每条 SSE event/comment 在支持 `http.ResponseController` 的 writer
上都会设置 10 秒写超时，完成后再清空 deadline；因此空闲长连接不会被误杀，慢客户端也
不能无限阻塞停机。deadline 清理失败会在本次写入本身成功时返回给 handler，不会留下
无法确信已解除的写超时。短写
会作为 `io.ErrShortWrite` 返回，flush 失败也会返回给 handler，让断开连接的事件循环
立即结束而不是继续空转；初始响应头 flush 失败时不会执行 callback。SSE 响应统一带
`X-Content-Type-Options: nosniff`。`AccessLog`、操作端点等响应包装器会继续透传
`FlushError`；底层不支持 flush/hijack 或 hijack 调用失败时不会被误判成响应已提交。
响应包装器也会透传 `io.ReaderFrom`，文件下载和 Swagger UI 静态资源仍可使用底层的
快速传输路径，同时 AccessLog 能正确统计字节数、操作端点能先写入安全响应头。
typed `Responder` 的响应守卫同样透传 flush 错误和 `io.ReaderFrom`，但明确拒绝
`Hijack`，避免流式/下载输出绕过 200 与 `produces` 契约。
`kernel.WriteFile` 在打开前先拒绝目录、FIFO 和其他非普通文件，再对已打开的句柄做
二次检查，避免特殊文件让 handler 阻塞。
操作端点的自定义 handler 即使只改 header 后直接返回，框架也会补一次受控 200
提交，确保 `no-store` 和 `nosniff` 不会被默认响应绕过；handler panic 后由
`Recover` 写出的 500 Problem 也会保留这两个安全头。

```go
router.Raw("GET", "/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	err := streams.ServeSSE(w, r, func(ctx context.Context, sse *kernel.SSE) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case now := <-ticker.C:
				if err := sse.Event("tick", now); err != nil {
					return err
				}
			}
		}
	})
}), kernel.Produces("text/event-stream"))
```

CSV：

```go
kernel.WriteCSV(w, "export.csv", []string{"id", "name"}, func(yield func([]string) error) error {
	return yield([]string{"1", "alpha"})
})
```

单文件：

```go
kernel.WriteFile(w, r, "/var/exports/report.pdf", "report.pdf")
```

`kernel.WriteFile` 只提供普通文件，并统一设置 `nosniff`；Range、
`Last-Modified` 和条件请求由 `http.ServeContent` 处理，目录或其他非普通文件返回 404。
成功响应默认使用 `Cache-Control: no-cache`，上游可以设置别的缓存策略；416/500 等
错误响应会强制改为 `no-store`，包括 `ServeContent` 自己生成的错误。文件名、文件类型和
文件本身会在修改任何响应头之前完成校验。
传输 helper 会拒绝 typed-nil response writer、reader、CSV destination 和 request；
这些错误发生在写 header 或执行回调之前，不依赖 `Recover` 把 nil 方法调用兜底成 500。
附件 `Content-Type` 必须能通过 `mime.ParseMediaType`；下载文件名禁止控制字符和
`/`、`\` 路径分隔符，任何非法输入都会在写 header 前返回错误。Unicode 文件名由
标准库按 RFC 5987 编码，不会把原始换行写进响应头。附件、CSV 和 `Copy` 默认带
`Cache-Control: no-store`，除非上游已经显式设置缓存策略。
附件状态码延迟到第一次实际写入 body 时提交：如果 writer 在写出任何字节前失败，
typed `Responder` 仍会变成完整的 500 Problem；一旦已有字节写出，就按流式响应处理，
后续错误只能记录日志。

内嵌静态站：应用把自己的 `go:embed` FS 传给 `webui.Handler`；设置 `STATIC_DIR=/path/to/dist` 后直接从磁盘提供，不需要重新编译。fallback `fs.FS` 为 nil 或 typed-nil 时会在构造期报错，不会等到首个请求。该 handler 只接受 `GET`/`HEAD`，所有响应都带 `X-Content-Type-Options: nosniff`；只提供普通文件，目录只有存在普通文件 `index.html` 时才会响应，FIFO、设备文件等特殊条目一律 404，也不会生成目录列表。文件只会打开一次，打开后再次确认是普通文件再交给 `http.ServeContent`；即使 `index.html` 在检查和打开之间消失，也只会返回 404，不会退回目录列表。`STATIC_DIR` 的路径解析被限制在该目录内，symlink 不能把文件访问带到目录外。成功响应默认使用 `Cache-Control: no-cache`，强制浏览器通过 `Last-Modified` 重新验证；上游中间件可以显式设置别的缓存策略，handler 会保留它。4xx/5xx 响应统一强制为 `Cache-Control: no-store`，包括 `ServeContent` 自己生成的 416/500，避免错误页被共享缓存保存。

## 动态 response header

返回类型实现 `kernel.Headerer` 即可：

```go
type Output struct {
	Headers kernel.Headers `json:"-"`
	ID      string         `json:"id"`
}

func (o Output) ResponseHeaders() http.Header { return http.Header(o.Headers) }
```

`Headerer` 只能补充响应头。typed JSON 的 `Content-Type` 始终由框架设置为
`application/json; charset=utf-8`，不能被 `Headerer` 覆盖。typed `Responder` 的
Content-Type 由响应实现负责，并会按上面的 `produces` 契约检查。

`kernel.WriteProblem` 会把 nil 或 typed-nil error 统一成通用 500 Problem；
`(*HTTPError)(nil)`、`(*ValidationError)(nil)`（包括被包装的情况）不会被解引用，
它们的 `Error`/`Unwrap` 也支持 nil receiver，错误链遍历本身不会 panic。helper
也接受 nil request 和 nil URL，不会为了记录 method/path 而 panic；nil response
writer 无法写响应，只记录一次错误后安全返回。Problem JSON 会先完整编码再提交；
提交后的短写或写失败通过传入的 logf 记录。

也可以在 raw handler 中调用 `kernel.SetHeaders(w, headers)`；它返回校验错误，调用方
可以决定如何响应。

动态 header 会在提交前校验：name 必须是 RFC token，value 不得含控制字符，每个 key
至少有一个 value，且大小写不敏感的同名 key 会被拒绝。由 net/http 控制的 framing
头和 hop-by-hop 头（`Content-Length`、`Transfer-Encoding`、`Trailer`、`Connection`、
`Keep-Alive`、`Proxy-Connection`、`TE`、`Upgrade`）不允许通过动态 header 设置；
确实要接管协议细节的端点使用 raw handler。
合法 key 会规范化为标准大小写。`Headerer` 返回非法 header 时，框架在应用任何
handler header 之前写 500 Problem。

## 生命周期与迁移

组件顺序：

```text
postgres -> redis -> migrator -> jobs worker -> scheduler -> http
```

关闭顺序完全相反。所有组件接收同一个 shutdown deadline。HTTP 的 `WriteTimeout` 固定为 0，SSE/WS 的有限停机由 `StreamRegistry` 负责，而不是靠一个全局写超时。`net/http` 自身的连接、握手和 TLS 错误通过 `http.Server.ErrorLog` 进入同一个 `slog` handler，不再绕过框架日志写到默认 stderr。HTTP 的 stream closer panic 会被记录为 Stop 错误，但 `baseCancel` 和 `Server.Shutdown` 仍会继续执行，监听器不会因为一个坏的 closer 残留。

`runtime.NewHTTP` 的可选 `runtime.WithHTTPConfig` 可以覆盖标准库 server policy：

```go
config := runtime.DefaultHTTPConfig()
config.ReadHeaderTimeout = 3 * time.Second
config.ReadTimeout = 2 * time.Minute
config.MaxHeaderBytes = 16 << 10
runtime.NewHTTP(addr, router, streams, logger, runtime.WithHTTPConfig(config))
```

默认值仍是 header 5s、完整请求读取 30s、idle keep-alive 90s、header 上限
`http.DefaultMaxHeaderBytes`。配置使用显式值：零 duration 会关闭对应超时，零
`MaxHeaderBytes` 使用 net/http 默认值；负数或 nil option 会在创建 listener 前由
`Start` 拒绝。`WriteTimeout` 不开放配置，避免误伤 SSE/WebSocket。真实 TCP 测试覆盖
慢 header 的读超时、空闲 keep-alive 回收和超限 header 的 431 响应。

TLS 与 HTTP/2 复用标准库实现，框架只负责把配置接到 `http.Server`：

```go
config := runtime.DefaultHTTPConfig()
config.TLSCertFile = "/etc/tls/tls.crt"
config.TLSKeyFile = "/etc/tls/tls.key"
config.EnableHTTP2 = true
runtime.NewHTTP(addr, router, streams, logger, runtime.WithHTTPConfig(config))
```

证书文件对和 `TLSConfig *tls.Config` 二选一，同时设置会在监听前失败。
`TLSConfig` 会被克隆，并且必须包含 `Certificates`、`GetCertificate` 或
`GetConfigForClient`。`EnableHTTP2` 需要 TLS；默认仍只启用 HTTP/1。证书加载、
握手、ALPN 协商和连接行为都由 `crypto/tls` / `net/http` 负责。

需要更细的协议组合时使用 `Protocols *http.Protocols`；它与 `EnableHTTP2` 互斥。
例如 h2c 用 `SetUnencryptedHTTP2(true)`，TLS HTTP/2 用 `SetHTTP1(true)` +
`SetHTTP2(true)` 并配置 TLS。空协议集、TLS HTTP/2 无 TLS、或同时设置
`EnableHTTP2`/`Protocols` 都会在监听前失败。

常用参数留在 `HTTPConfig`；不常见的标准库能力通过代码注入，不要求变成环境变量：

```go
config := runtime.DefaultHTTPConfig()
config.Listen = func(ctx context.Context, network, address string) (net.Listener, error) {
	return net.Listen(network, address) // 例如换成 Unix socket 或自定义 listener
}
config.ConfigureServer = func(server *http.Server) error {
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, traceKey{}, traceFrom(conn))
	}
	server.ConnState = observeConnState
	return nil
}
runtime.NewHTTP(addr, router, streams, logger, runtime.WithHTTPConfig(config))
```

`ConfigureServer` 在框架填好 server 后、创建 listener 前执行；返回错误会中止启动。
回调之后框架会重新写回 `Handler`、三个超时、`TLSConfig`、`Protocols`、`ErrorLog` 和
`BaseContext`，所以这些契约字段仍通过 `HTTPConfig` 的常用字段控制。`Listen` 返回
非 nil listener 和 error 时框架会关闭 listener 并拒绝启动。
单个组件的 `Stop` panic 会被记录并转成返回错误，其余组件仍按逆序继续关闭。

生命周期状态是确定的：`Runtime.Run` 不可重入，组件只能在 `Run` 前 `Add`；`Add` 会在注册期拒绝 nil 和 typed-nil 组件，且一次调用要么全部登记、要么保持原状。如果启动前或启动过程中已经收到取消信号，会停止已经启动的组件并按正常关闭返回，不会把取消误报成启动失败。组件 `Start` panic 会记录 stack 并转为启动错误，已启动组件照常逆序停止，后续组件不会启动；`Stop` panic 同样被隔离。`FailureReporter` 的 `Failed`/`Failure` 回调运行在受保护的 watcher goroutine 中，panic 会转为组件失败并触发逆序关闭；`Name` panic 会回退为具体类型名，不会中断启动或停止。`runtime.HTTP` 会 trim 监听地址，`Start` 在创建 listener 前拒绝空地址、nil 和 typed-nil handler，构造期会把 typed-nil `StreamCloser` 归一为 nil；`Start`/`Stop` 串行执行，重复 `Stop` 幂等，`Stop` 后不能再 `Start`；worker、scheduler 的启动/停止也串行化，scheduler 的 `Declare` 不会与启动过程竞争。

worker 每次 run 都捕获自己的完成 channel；即使一次 `Stop` 因超时先返回，旧 run 的 watcher 也只会关闭旧 channel，不会误关闭随后重启的新 run。handler 返回 typed-nil error 时会记录为明确的失败原因，而不是把它当作成功，也不会解引用 nil；`MarkFailed`/`MarkInterrupted` 的 cause 同样接受 typed-nil。`Worker` 和 `Scheduler` 的 nil receiver 也有确定行为：`Start`/`Declare` 返回错误，`Stop` 幂等成功，`Ready` 为 false，`InFlight` 为 0，`Failed` 为 nil，`Failure` 为 nil。`runtime.HTTP` 的监听、异常退出和 `http.Server` 内部错误日志统一携带 `component=http`，连接、握手与 TLS 错误不会绕过结构化日志。

组件可以实现可选的 `runtime.FailureReporter` 来报告 `Start` 返回后的异步故障。
`runtime.HTTP` 会在非正常 `Serve` 退出时关闭 `Failed()`；`Runtime` 随即按逆序停止所有组件，
并把该故障与停机错误一起返回给 `app.Main`，不会留下“监听已经死亡但进程继续挂起”的假活状态。

`postgres.Postgres` 和 `redis.Redis` 同样是一次性生命周期组件：构造期 trim 并拒绝空的 `DATABASE_URL` / `REDIS_URL`，启动、停止和 Ping 串行化，重复启动失败，重复停止幂等，停止后拒绝启动和 Ping。nil receiver 有确定行为：`Start`/`Ping` 返回错误，`Stop` 幂等成功，`Pool`/`Client` 返回 nil。`migrate.Migrator` 也采用单次生命周期，URL 会在构造期 trim；启用迁移但 URL 为空时会在打开数据库前失败。它还会在应用任何 source 之前校验所有 source 的名称、文件系统（包括 typed-nil `fs.FS`）、非冲突版本表和 goose provider；source 名称必须是合法 UTF-8、只含可打印字符且不超过 128 字节，版本表名还必须落在 PostgreSQL 63 字节标识符上限内，避免数据库截断后让两个 source 静默共用一张表。后续 source 的配置错误不会导致前面的 source 已经落库。`Migrator` 零值可使用，未配置 logger 时回退到 `slog.Default()`。

迁移执行遵守传入的 `context.Context`：取消发生在打开数据库前、source 之间或 goose 执行中时会停止后续工作并返回错误。Goose 默认把每个 SQL migration 放在独立事务中，因此失败版本本身通常回滚，但**已经完成的版本和更早的 source 不会回滚**；使用 `-- +goose NO TRANSACTION` 时，失败版本也可能只应用了一部分。框架不伪造跨 source 的全局事务，而是让迁移失败阻止 worker/HTTP 启动，并在错误中报告当前 source 已完成数量和此前已完成的 source。生产环境应由发布流水线保证 schema 变更可重入、可人工修复，并在启动前确认所需版本均已应用。

迁移文件使用 Goose 注释。框架和应用应各自保留 migration source：

```sql
-- +goose Up
ALTER TABLE example ADD COLUMN note text;

-- +goose Down
ALTER TABLE example DROP COLUMN note;
```

```go
migrate.New(url, enabled, logger,
	migrate.Migration{Name: "plinth", FS: plinthmigrations.FS()},
	migrate.Migration{Name: "app", FS: appmigrations.FS()},
)
```

每个 source 使用独立的 `goose_version_<name>` 表，应用 migration 版本不会和框架 migration 冲突。`AUTO_MIGRATE=true` 时在 worker 启动前执行所有 source 的 `Up`。生产环境如果由独立发布流水线管理 schema，可设置为 `false`，但必须保证 worker 启动前目标版本已应用。

## 日志输出

`logging` 只做一件事：把同一个 `slog` 记录写到一个或多个明确配置的目标。

```go
logger, err := logging.New(logging.Config{
	Level:   "info",
	Format:  "json",
	Console: logging.ConsoleStdout,
	File:    "/var/log/plinth.jsonl",
})
if err != nil {
	return err
}
defer logger.Close()

slogLogger := logger.Slog() // 传给只接受 *slog.Logger 的组件
slogLogger.Info("application starting")
```

- `Console` 零值是 `ConsoleStdout`；`ConsoleNone` 用于只写文件或只写自定义 writer，`ConsoleStderr` 写标准错误。
- `Format` 为空时使用 `json`，也可以显式选择 `text`；`Level` 为空时使用 `info`。
- `File` 以 append 模式打开，新文件权限为 `0600`；父目录必须已经存在。
- `Writers` 可以追加任意 `io.Writer`，但这些 writer 归调用方所有，`Close` 不会关闭它们。
- `ConfigureOptions` 在框架写入 `Level`/`AddSource` 后修改标准 `slog.HandlerOptions`，适合 `ReplaceAttr`、时间格式或覆盖级别。
- `NewHandler` 可以用自定义 `slog.Handler` 替换 JSON/Text，并接收合并后的输出 writer；此时不能再设置 `Format`。
- `Err()` 返回第一次写失败，`Close()` 也会返回该错误；`slog` 本身不会把 writer 错误交给业务代码。
- 不内置轮转、压缩或采样。生产环境应按部署方式使用 logrotate、sidecar 或外部日志系统。

## 通用工具

只有与业务和基础设施策略无关、所有应用都可能复用的纯函数才进入 `utils`：

- `utils.NonNilSlice` 把 nil slice 转为空 slice，避免 JSON `null` 或数据库数组参数出现意外 nil。
- `utils.NormalizePage` 统一分页的默认 limit、最大 limit 和负 offset。

日志构造不重复提供第二套 API，统一使用 `logging.New`。PostgreSQL 错误识别属于平台能力，
例如 `postgres.IsUniqueViolation` 判断 `23505` 并支持错误链；应用仍负责把它映射成自己的
冲突错误。业务映射、缓存 key、事件 envelope、Slug 规则等仍留在应用侧。

## 配置

参考应用的配置只有一个入口：`internal/config.Config`。公共库不自带全局配置。没有 Viper、没有多层隐式优先级、没有配置热更新。

`app` 的 `env` 和 `check` 命令共用同一套层级 schema。根前缀只配置一次：
`Application.EnvPrefix` 或加载配置时的 `app.WithEnvPrefix(...)`。后续每层默认使用
字段名转换出的 `UPPER_SNAKE` segment，也可以显式写 `env:"SEGMENT"`，最终逐层拼接：

```go
type Config struct {
    DatabaseURL string                 `env:"DATABASE_URL,required"`
    Worker      jobs.WorkerConfig      `env:""`
    Scheduler   jobs.SchedulerConfig   `env:""`
    Ops         kernel.OpsSettings     `env:""`
}
```

根前缀为 `SHELF` 时，`Worker` 自动得到 `WORKER_` segment；`WorkerConfig` 自己声明
`CONCURRENCY`、`POLL_INTERVAL`、`TIMEOUT`、`STALE_AFTER`、`RETRY_BASE` 和
`RETRY_MAX`，因此生成 `SHELF_WORKER_*`。`SchedulerConfig` 同理生成
`SHELF_SCHEDULER_INTERVAL` 和 `SHELF_SCHEDULER_BATCH`。叶子字段省略变量名时也使用
字段名，例如 `env:",default=3"` 会得到 `..._VALUE=3`。`prefix=` 不再用于嵌套字段；
根前缀之外的层级只用 segment。

框架自身的通用配置以可嵌入片段提供。`kernel.OpsSettings` 的 `Ops` 字段自动得到
`OPS_` segment，生成启用开关和四个路径变量，默认全部启用。它只包含跨应用稳定的
env，`Checks`、`Logger` 和自定义 handler 等运行时依赖仍属于 `kernel.OpsConfig`：

```go
router.RegisterOps(cfg.Ops.Apply(kernel.OpsConfig{
    Logger: logger,
    Checks: checks,
}))
```

`OpsSettings.Apply` 只派生注册开关和路径，不修改传入的 `OpsConfig`。直接使用
`OpsConfig` 时其零值仍表示全部端点启用；`OpsSettings` 的零值则因为使用正向布尔值而
表示全部关闭，因此应用配置应通过 env loader（缺省值即启用）或
`kernel.DefaultOpsSettings()`。这是有意的边界：环境配置使用正向 `Enable`，运行时
`OpsConfig` 用 `Disable` 保持零值即启用；转换只发生在 `Apply` 一处，避免应用代码
重复取反。

四个路径都可以独立覆盖；`DocsPath` 省略尾斜杠时会自动补齐：

```sh
OPS_LIVENESS_PATH=/health/live
OPS_READINESS_PATH=/health/ready
OPS_OPENAPI_PATH=/api/openapi.json
OPS_DOCS_PATH=/api/docs/
```

`default=` 必须写在最后一个 option，且值里的尾随空格会原样保留。未知或空 option、
重复 key、未导出字段上的 tag、递归 struct/指针、重复 option、`env:"-"` 后的 option、
非法 segment 或根前缀，都会在生成模板或 `check` 阶段失败，而不是拖到进程启动。
重复 key 检查覆盖整个嵌套结构，包括父子层级和两个同 segment 的兄弟字段。
key 必须是合法的 shell 变量名，生成值在需要时会被 shell 单引号转义。
`default=` 中的换行和 NUL 也会被拒绝，因为这样的值无法安全地写入 `.env` 并交给进程环境。
叶子字段可以声明 `envdoc:"..."`，`env` 命令会把说明输出为紧邻变量的注释；该 tag
只允许出现在真正生成 ENV 的叶子字段上，嵌套 struct、`env:"-"` 和未导出字段使用它
会在生成模板或 `check` 阶段失败。
应用加载配置时使用 `app.LoadEnv(ctx, &config, app.WithEnvPrefix("SHELF"))`；它先执行
同一套 schema 校验，再解码，并运行字段上的 `validate` tag。`gt`、`gtfield` 等规则
可以直接表达 duration/数值不变量；校验错误会带最终 ENV 名，而不是 Go 字段路径。
因此绕过 `check` 的直接启动也不会静默接受重复变量或无效配置。
`shelf serve -h`、`shelf check -h` 和 `shelf env -h` 都只打印命令帮助并成功退出，
不会启动服务、构建路由或读取 Config；因此它们不需要应用注册对应依赖。
模板先写入目标目录中的临时文件：不带 `-force` 时以排他方式创建，已有文件不会
被覆盖；带 `-force` 时原子替换目标，保留已有普通文件权限，并且不会跟随符号链接
写入链接目标。临时文件在内容写完前保持 `0600`，完成写入后才设置最终权限并提交；
提交失败会删除临时文件，目标保持原状。空输出路径、空 commit 和短写都会明确失败，
不会留下空文件或半截内容。

参考配置不会把“变量存在但值为空”当成有效配置：`DATABASE_URL`、`REDIS_URL` 和
`HTTP_ADDR` 都会被检查，三个值也都会去掉首尾空白后再交给组件。`LOG_LEVEL`、`LOG_FORMAT`
和 `LOG_FILE` 会先 trim；前两者会转小写再校验，因此 `INFO` / `JSON` 与固定小写值等价。

| 变量 | 默认值 | 说明 |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `TLS_CERT_FILE` | 空 | TLS 证书 PEM；与 `TLS_KEY_FILE` 成对出现 |
| `TLS_KEY_FILE` | 空 | TLS 私钥 PEM |
| `HTTP2_ENABLED` | `false` | 在 TLS 上启用 HTTP/2 |
| `DATABASE_URL` | 必填 | Postgres DSN |
| `REDIS_URL` | 必填 | Redis URL |
| `AUTO_MIGRATE` | `true` | 启动前执行 migration |
| `OPS_LIVENESS_ENABLED` | `true` | 注册 `/livez` |
| `OPS_LIVENESS_PATH` | `/livez` | liveness 路径 |
| `OPS_READINESS_ENABLED` | `true` | 注册 `/readyz` |
| `OPS_READINESS_PATH` | `/readyz` | readiness 路径 |
| `OPS_OPENAPI_ENABLED` | `true` | 注册 `/openapi.json` 和 `/docs/` |
| `OPS_OPENAPI_PATH` | `/openapi.json` | OpenAPI JSON 路径 |
| `OPS_DOCS_PATH` | `/docs/` | Swagger UI 路径 |
| `WORKER_CONCURRENCY` | `4` | 并发 worker 数 |
| `WORKER_POLL_INTERVAL` | `500ms` | 空闲领取间隔 |
| `WORKER_TIMEOUT` | `30s` | 单次任务执行上限 |
| `WORKER_STALE_AFTER` | `2m` | 崩溃任务回收阈值 |
| `WORKER_RETRY_BASE` | `1s` | 首次重试退避 |
| `WORKER_RETRY_MAX` | `1m` | 最大退避 |
| `SCHEDULER_INTERVAL` | `1s` | 到期计划扫描间隔 |
| `SCHEDULER_BATCH` | `20` | 单次扫描最大派发数 |
| `SHUTDOWN_TIMEOUT` | `10s` | 全组件关闭预算 |
| `STATIC_DIR` | 空 | 覆盖内嵌前端目录 |
| `LOG_LEVEL` | `info` | `debug/info/warn/error` |
| `LOG_FORMAT` | `json` | `json/text` |
| `LOG_CONSOLE` | `true` | 是否写终端；`false` 时必须配置 `LOG_FILE` |
| `LOG_FILE` | 空 | 追加写入的日志文件 |

## 可选能力

这些能力都通过独立子包 opt-in，不改变核心 typed HTTP 的依赖闭包。

### Redis 锁

`platform/redislock` 的锁是带随机 token 的租约，释放和续期只有在 token 仍匹配时
才成功。默认使用 Lua（`GET`/`DEL`/`PEXPIRE`）；Redis ACL 禁用 `EVAL` 或部署策略
不允许脚本时改用 `WATCH/MULTI/EXEC`：

```go
locker, err := redislock.New(redisClient, redislock.Config{
    Prefix:       "shelf:lock:",
    Strategy:     redislock.StrategyCommands,
    RedisVersion: "6.2.7", // 可选；用户明确指定的服务端版本
})
if err != nil {
    return err
}
lock, err := locker.AcquireWait(ctx, "link:"+slug, 30*time.Second, 5*time.Second, 50*time.Millisecond)
if err != nil {
    return err
}
defer lock.Release(ctx)
```

两条策略共同依赖 Redis `2.6.12+` 的 `SET NX PX`；低于该版本会在启动时被拒绝。
`RedisVersion` 支持 `major.minor` 和 `major.minor.patch`，用于把部署版本约束显式化，
而不是在第一次请求时才因命令或 EVAL 权限失败。Lua 策略要求 `EVAL`，命令策略不要求。

### 幂等键

`platform/idempotency` 提供内存和 Redis `Store`。中间件在请求完成前保留 pending
reservation，响应成功后保存状态码、安全响应头和 body；相同 key、相同指纹的重放直接
返回原响应，并用 `Idempotency-Replayed: true` 标记。相同 key 但不同请求指纹返回 409。
5xx 不缓存，失败请求会释放 reservation。Redis 实现同样使用 `WATCH/MULTI/EXEC`，
不依赖 Lua。

### 认证、JWT 与 OIDC

`kernel/auth` 只定义 Bearer 提取、401 Problem 和 `Principal` context；它不绑定 JWT
库。`platform/jwt` 支持 HMAC、RSA、ECDSA 和显式算法 allowlist；`platform/oidc`
负责 issuer discovery、JWKS 刷新和 ID token 校验。两者都实现同一个 `auth.Verifier`，
可以挂在需要认证的 group 上，也可以保持 `Optional` 只允许缺失 header：

```go
verifier, err := jwt.NewVerifier(jwt.Config{Secret: secret, Algorithms: []string{"HS256"}})
if err != nil {
    return err
}
middleware, err := auth.New(verifier, auth.Config{}, logger)
if err != nil {
    return err
}
protected := router.Group("/v1", middleware)
```

### 声明式授权

`kernel/authz` 不定义身份或权限来源，只消费路由上的权限声明。输入结构体的
`kernel.Meta` 可以写 `authz` tag，也可以对 raw route 使用
`kernel.RequirePermissions`：

```go
type GetItemInput struct {
    kernel.Meta `route:"GET /v1/items/{id}" authz:"items:read"`
}

permissions := authz.PermissionSourceFunc(func(ctx context.Context) ([]string, error) {
    principal, ok := auth.FromContext(ctx)
    if !ok {
        return nil, nil
    }
    operation, _ := kernel.OperationFromContext(ctx)
    return policy.Permissions(ctx, principal, operation.OperationID)
})
authorize, err := authz.New(permissions, authz.Config{Logger: logger})
if err != nil {
    return err
}
router.Use(authorize)
```

同一个路由声明的权限按 all-of 语义全部满足才放行；权限源返回 `*` 表示全权。
未声明权限的路由不会调用权限源。缺失权限返回 RFC 9457 403，权限源失败返回 500；
生成 OpenAPI 时权限列表出现在 `x-required-permissions`。权限字符串在路由注册阶段
去重并校验，非法 tag 会让启动失败，而不是等到请求期。

### CORS

`kernel/cors` 是显式 allowlist 中间件，不会在核心 router 中隐式开启。它必须包在整个
`http.Handler` 外层，才能在 `ServeMux` 之前处理 preflight：

```go
crossOrigin, err := cors.New(cors.Config{
    AllowedOrigins:   []string{"https://app.example"},
    AllowCredentials: true,
    MaxAge:           10 * time.Minute,
})
if err != nil {
    return err
}
handler := crossOrigin(router)
```

允许的 preflight 返回 204；不允许的 origin、method 或 header 返回 403 Problem。
通配符 origin 只能在不带 credentials 时使用；method/header 必须是显式 token，不接受
`*`。CORS 不是认证、授权或 CSRF 防护，不能因为配置了 CORS 就跳过这些边界。

### CSRF

`platform/csrf` 使用 cookie 中的随机 token 加请求头回传，并在 store 中只保存 token 的
SHA-256 标识。默认保护 `POST`、`PUT`、`PATCH`、`DELETE`；安全方法自动签发或复用
token：

```go
store, err := csrfredis.NewStore(redisClient, "shelf:csrf:")
if err != nil {
    return err
}
protect, err := csrf.New(csrf.Config{
    Store:  store,
    Secure: true, // HTTPS 部署必须开启
})
if err != nil {
    return err
}
router.Use(protect)

// 服务端渲染表单或返回 SPA bootstrap JSON 时读取当前 token。
token := csrf.TokenFromContext(ctx)
```

默认 cookie 为 `SameSite=Lax`、非 `HttpOnly`，便于同源 JavaScript 读取后放入
`X-CSRF-Token`。如果 token 由响应体交给前端，可以开启 `HttpOnly`。`SameSite=None`
必须同时开启 `Secure`。Redis store 只使用 `SET`/`EXISTS`/`DEL`，不依赖 Lua。

### 限流

`platform/ratelimit` 提供固定窗口 limiter 和 HTTP 中间件。内存实现适合单实例；
多实例使用 `platform/ratelimit/redis`：

```go
limiter, err := redilimit.New(redisClient, redilimit.Config{
    Prefix:       "shelf:ratelimit:",
    Limit:        120,
    Window:       time.Minute,
    Strategy:     redilimit.StrategyCommands, // ACL 禁用 EVAL 时使用
    RedisVersion: "7.2.4",                    // 可选，启动期校验
})
if err != nil {
    return err
}
throttle, err := ratelimit.New(ratelimit.Config{Limiter: limiter, Logger: logger})
if err != nil {
    return err
}
router.Use(throttle)
```

默认 key 是匹配到的 operation ID 加直接连接地址；框架不会信任
`X-Forwarded-For`。需要按用户限流时注入 `Key` 函数。超限返回 429 Problem 和
`RateLimit-*`/`Retry-After` header；limiter 故障 fail closed 为 500。Redis Lua 策略
要求 `EVAL`，命令策略不需要；两者最低支持 Redis `2.6.0`。进程内 limiter 的 key
表有上限，容量耗尽时 fail closed，不能用来替代网关级 DDoS 防护。

### Session

`platform/session` 包装 `alexedwards/scs/v2`，统一 cookie 策略、自动 load/save 和
RFC 9457 错误响应。单实例可用内存 store；多实例使用 `platform/session/redis`：

```go
store, err := sessionredis.NewStore(redisClient, "shelf:session:")
if err != nil {
    return err
}
sessions, err := session.New(store, session.Config{
    Lifetime:    24 * time.Hour,
    IdleTimeout: 30 * time.Minute,
    Secure:      true, // HTTPS 部署必须开启
})
if err != nil {
    return err
}
router.Use(sessions.Middleware)

// handler 内：
sessions.Put(ctx, "user_id", userID)
```

session cookie 始终 `HttpOnly`，store 中保存的是 hash 后的 token；`SameSite=None` 和
Partitioned cookie 都要求 `Secure`。默认使用持久 cookie，`NoPersist` 可改为浏览器
会话 cookie。Session 与 CSRF 解决的是不同问题，涉及 cookie 认证的状态变更接口应同时
评估两者。

### Multipart 上传

`kernel/uploads` 是 opt-in 的流式 `multipart/form-data` 解析器，不使用
`ParseMultipartForm` 的全量内存缓冲。它限制文件数、字段数、单文件/总大小和可选 MIME
类型，把文件写入 `0600` 临时文件；handler 必须 `defer form.Close()`。typed JSON body
绑定仍然只接受 `application/json`，上传路由使用 raw handler 显式接入。

### 对象存储

`platform/objectstore` 定义 `Put`/`Open`/`Stat`/`Delete`/`List` 契约。`localfs` 提供
原子本地写入、key traversal 防护和 symlink 拒绝；`s3` 是独立可选适配器，接受
`aws-sdk-go-v2` 的 `*s3.Client` 或同接口 fake，支持 bucket prefix、分页列表和
`ErrNotFound` 映射。`Put` 的 `size` 是精确字节数：短读和尾随数据都会被拒绝，不会静默
截断。只使用 `localfs` 的应用不会链接 AWS SDK。

### 可观测性

`platform/observability/prometheus` 按 `method`、ServeMux route pattern 和 `status`
记录请求计数、时延和 in-flight；`platform/observability/otel` 创建 server span 并
记录 method、route、status、字节数和 panic。两者都保留 `Flusher`/`Hijacker` 等
流式与升级接口，且可以同时挂在同一 router 上。`jobs.Observer` 是 jobs 包的
backend-neutral 钩子，供应用把 worker attempt 和 scheduler scan 接到自己的 metrics
或 tracing 实现，不需要 jobs 反向依赖 Prometheus/OTel。

### 迁移与 dead-letter 运维

`plinth-migrate` 可以脱离应用启动 migration，并接受多个独立 source：

```sh
plinth-migrate -database-url "$DATABASE_URL" \
  -source plinth=./platform/jobs/migrations -source app=./migrations
```

`platform/jobs/admin` 提供 `GET /jobs/failed`、`POST /jobs/{id}/retry` 和
`DELETE /jobs/{id}`。它不自动挂到公开路由；应用应把它注册到受鉴权的 group，并自行
决定运营权限。

## 设计取舍

- **薄层，但不假装零成本。** 启动时做类型反射与 schema 生成，请求期只做字段赋值、validator 调用和 JSON 编解码。
- **泛型只在边界。** job payload 与 HTTP 输入输出有类型；内核、store、worker 内部保持普通 struct，避免泛型污染。
- **无包级可变状态。** 文档、registry、stream、组件都从 `main` 注入，方便测试和多实例嵌入。
- **不隐藏 goroutine。** worker/scheduler/HTTP 的 goroutine 都有 owner、取消和等待。
- **不提供任务管理 UI。** 任务是运行时基础设施；运营界面应独立建设。
- **TLS 可选，安全中间件默认不启用。** 框架可直接加载证书并启用 HTTP/2，也可以由
  网关终止 TLS；`kernel/auth`、JWT、OIDC、CORS、授权、CSRF、限流和 session 都是显式
  opt-in 子包或应用接线，不会隐式改变核心 router 的响应。
  `BodyLimit` 提供应用内请求体上限，超限返回 413；它仍是显式中间件，因为上传和流式
  端点可能需要不同上限。

## HTTP 性能基线

框架在 `kernel/benchmark_test.go` 维护一组可重复的进程内基准，覆盖 typed GET、
并行 typed GET、typed JSON POST、校验失败、raw 响应、404 和常用中间件链。它测量的是
`Router.ServeHTTP` 的应用侧成本，不包含真实 socket、TLS、keep-alive 或 TLS 握手。

Go 1.27.3、当前开发机上的 v173 基线（五轮结果取大致范围；更早数字保留在交接记录中用于对比）：

| 场景 | 时间 | 内存 | 分配 |
|---|---:|---:|---:|
| typed GET | 0.65–0.66 µs/op | 136 B/op | 6 allocs/op |
| typed GET, no query | 0.53–0.54 µs/op | 144 B/op | 6 allocs/op |
| typed GET, header binding | 0.70–0.75 µs/op | 160 B/op | 6 allocs/op |
| typed GET, static route | 0.42–0.43 µs/op | 80 B/op | 3 allocs/op |
| typed GET parallel | 0.27–0.28 µs/op | 136 B/op | 6 allocs/op |
| typed JSON POST | 1.67–1.87 µs/op | 706 B/op | 11 allocs/op |
| validation failure | 3.34–3.62 µs/op | 1776 B/op | 30 allocs/op |
| JSON encode failure | 3.29–4.19 µs/op | 641 B/op | 12 allocs/op |
| body limit, declared oversize | 1.19–1.40 µs/op | 945 B/op | 12 allocs/op |
| body limit, streamed oversize | 1.85–2.04 µs/op | 1552 B/op | 16 allocs/op |
| raw | 173–209 ns/op | 18 B/op | 2 allocs/op |
| not found | 0.83–0.86 µs/op | 240 B/op | 14 allocs/op |
| middleware chain | 3.47–3.59 µs/op | 1170 B/op | 18 allocs/op |

TLS、HTTP/2 和日志配置只发生在构造/启动阶段，不进入请求热路径；v173 复测的分配数与
v162 一致，时间和基线噪声范围内。

运行：

```sh
go test ./kernel -run '^$' \
  -bench 'BenchmarkHTTP' -benchmem -count=3
```

`make bench` 会跑 5 轮进程内矩阵。`kernel/budget_test.go` 只用
`testing.AllocsPerRun` 约束 typed GET、无 query typed GET、静态 typed GET、header
绑定、typed JSON POST、校验失败、JSON 编码失败、声明/流式 body-limit 拒绝和中间件链的 alloc/op；墙钟时间不进入 CI 断言。预算留有回退余量，
新增或更新预算必须附带多轮基准结果，不能为了让测试变绿而放宽。

基准不是行为契约，也不能单独作为发布门槛；只有连续多轮稳定后才会增加回归阈值。任何
优化都不得放宽严格 JSON、先编码后提交、typed `Responder` 或动态 header 校验规则。
POST 基准使用可重置的固定请求体并维持正确的 `ContentLength`，不会把
`httptest.NewRequest` 的解析成本混入框架成本。

真实 `net/http.Server` 的端到端基准在 `BenchmarkHTTPServer`，使用 keep-alive 连接；
这些数字包含 client、socket、调度和协议栈成本，不能与上面的进程内数字直接比较：

| 场景 | 时间 | 内存 | 分配 |
|---|---:|---:|---:|
| keep-alive GET | 55–57 µs/op | 5708–5720 B/op | 72 allocs/op |
| keep-alive GET parallel | 14.8–15.9 µs/op | 5682–5699 B/op | 71 allocs/op |
| keep-alive JSON POST | 62 µs/op | 8004–8069 B/op | 92 allocs/op |
| TLS GET | 62 µs/op | 5733–5742 B/op | 72 allocs/op |

```sh
go test ./kernel -run '^$' \
  -bench 'BenchmarkHTTPServer' -benchmem -count=3
```

## HTTP 协议边界

- `HEAD` 由 `net/http.ServeMux` 的 GET 路由匹配，真实 server 按 HTTP 规范抑制响应
  body；`OPTIONS` 不会自动生成 CORS 响应，未注册时返回 405 和 `Allow`。
- 404/405、HEAD、HTTP/1.1 keep-alive、TLS、header 读取上限属于 `net/http` 的协议层；
  Plinth 不复制第二套协议实现。
- TLS 可以终止在本进程，也可以由反向代理或负载均衡器终止。代理模式使用普通
  `http://` listener，不配置证书；框架不会猜测 `X-Forwarded-*`，客户端协议、可信代理
  和 scheme 恢复必须由应用或网关边界明确处理，不能让任意客户端伪造这些头。
- h2c（明文 HTTP/2）通过 `HTTPConfig.Protocols` 的 `SetUnencryptedHTTP2(true)` 开启；
  `EnableHTTP2` 只表示 TLS 上的 HTTP/2。协议协商由 `net/http` 完成，Plinth 只验证配置
  组合并把 `Protocols` 接到 `http.Server`。h2c 更适合私网、sidecar 或本地链路，不应
  因为方便而替代公网 TLS。
- multipart/form-data 不属于 typed JSON body 绑定：`Handle` 的 `body` 参数只接受
  `application/json`。需要上传时使用 raw handler 显式接入 `kernel/uploads`（或应用
  自己的解析器），并为该路由设置独立的 body、文件大小、临时文件清理和内容类型限制。
  核心 router 不会自动解析 multipart。
- CORS、认证、授权、CSRF、限流和 session 通过可选子包或应用中间件接入，不能隐式
  改变所有路由的响应。CORS 必须包在 router 外层才能处理未注册的 preflight。
- 1xx、101、204、205、304 的 bodyless 行为由 typed 输出和 `statusWriter` 共同保持；
  需要 trailer、协议升级或显式接管时使用 raw handler。
- `BodyLimit` 是显式中间件，适合为普通 JSON API 设置上限；上传和流式端点应使用不同
  路由或独立配置。声明 `Content-Length` 已超限时会在读取 body 前直接返回 413；未知
  长度或 chunked 请求仍由 `http.MaxBytesReader` 在读取过程中兜底。严格 JSON 仍拒绝
  任意深度的 `null`、重复键、未知字段和尾随值。

## 继续保持边缘化

认证、授权、CORS、CSRF、限流、session、可观测性、分布式锁、幂等键、对象存储、上传、
migration CLI 和 dead-letter 运维 API 都以独立子包或独立命令提供。应用只 import
自己需要的能力；不要把这些
可选依赖反向塞进 `kernel`、`runtime` 或 `app` 的核心依赖图，也不要让框架替业务决定
授权策略、存储后端或指标基数。

## 参与贡献

参见 [CONTRIBUTING.md](CONTRIBUTING.md)。安全漏洞请按 [SECURITY.md](SECURITY.md)
私密报告。

## 许可证

Plinth 采用 [Apache License 2.0](LICENSE)。版权与归属信息见 [NOTICE](NOTICE)；
第三方包清单见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
