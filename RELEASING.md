# Plinth 版本与发布

Plinth 使用 Semantic Versioning 发布 Go module。开发记录里的 `v177`、`v181`
等编号只是内部迭代号，不是 module version，也不进入 tag。

当前稳定版本是 **`v1.0.0`**。v1 内保持公开 API、行为契约和安全边界兼容；未发布变更先
记录在 `CHANGELOG.md` 的 `[Unreleased]` 下，发布时再冻结版本号和日期并创建 tag。

## 版本语义

- **MAJOR**：删除或不兼容地修改公开 Go API、wire contract 或默认安全/可靠性保证。
- **MINOR**：向后兼容地新增公开 API、配置接缝、生成能力或可选行为。
- **PATCH**：向后兼容的 bug fix、文档修正、性能修复和安全修复。
- 预发布版本使用 `v1.0.0-rc.1` 这类 SemVer pre-release tag，不能当作稳定版宣传。
- v1 的 module path 是 `github.com/plinth-go/plinth`；v2 及以后必须使用 Go semantic import
  versioning 的 `/v2` 路径。

安全和数据正确性修复可以在 PATCH 中改变不安全或不正确的旧行为，但 release notes
必须明确说明迁移影响。

## 公开 API 范围

公开 API 指这些包的导出声明：`app`、`kernel`、`kernel/websocket`、`logging`、
`platform/jobs`、`platform/jobs/migrations`、`platform/migrate`、
`platform/postgres`、`platform/redis`、`runtime`、`utils`、`webui`。

- 包括导出的类型、字段、函数、方法、常量、变量和嵌入契约。
- 不包括 `internal/`、`cmd/`、参考应用、测试代码、生成文件内部结构和未导出类型上的
  方法。
- HTTP 状态码、Problem 形状、严格 JSON 规则、header 约束和 OpenAPI 生成规则属于行为
  契约，即使不改变 Go 签名，也按兼容性变更审查。
- 新增依赖如果会扩大使用者必须接受的运行时或安全面，应在 release notes 中说明。
- 可选公开子包与核心包的 import 边界属于兼容性契约：未 import 的可选子包不得被核心
  包重新带入编译或链接，`internal/depscheck` 是发布前必跑的结构检查。

`internal/apicheck/testdata/public-api.txt` 是可审查的导出声明基线，不是语义兼容性的
替代品。正常检查：

```sh
make api-check
```

只有确认变更是有意且符合版本策略时才刷新：

```sh
make api-update
git diff -- internal/apicheck/testdata/public-api.txt
```

## 发布前检查

在干净工作区运行。`generate` 和 `fmt` 会写文件，因此应先执行并 review diff：

```sh
cd plinth
make generate
make fmt
make release-check
```

`make release-check` 包含文档/API 新鲜度、路由与 OpenAPI 契约检查、GitHub Actions
workflow lint、普通测试、vet、build、完整 `-race` 测试、两个 5 秒 fuzz 运行，以及
`govulncheck`。若本次发布包含参考应用，再验证：

```sh
cd ../plinth-example
make generate
make fmt
make check
make test
make vet
make build
go test -race ./... -count=1
```

还必须按 [`SECURITY.md`](SECURITY.md) 做部署边界复核：

- TLS 在进程内终止，以及反向代理终止时的明文 listener 配置。
- 代理信任策略和 `X-Forwarded-*` 不依赖框架自动推断。
- 静态文件根目录、symlink 拒绝和缓存 header。
- 上传路由的独立 body/file 限制、内容类型检查和临时文件清理。
- CORS、认证、CSRF、限流中间件的顺序。
- WebSocket 同源策略，以及显式跨域时的鉴权边界。
- 关闭公开 OpenAPI/readiness，或将其挂到授权路由组。

第三方依赖漏洞扫描由 `make release-check` 中的 `govulncheck` 和仓库启用的 CodeQL
default setup 执行；本地单元测试不能替代它。
第三方依赖的许可证兼容性由 `make license-check` 检查；公开清单由
`make license-update` 生成到 `THIRD_PARTY_NOTICES.md`，发布前应与 `go.mod`/`go.sum`
的变更一起 review。

Dependabot PR 的生成文件同步由 `.github/workflows/dependabot-autofix.yml`
自动处理；发布前仍应确认 `THIRD_PARTY_NOTICES.md` 与 `go.mod`/`go.sum` 一致。

## 发布步骤

1. 确认版本号和 `CHANGELOG.md` 与公开 API diff 一致。
2. 完成上面的 framework/example gate 和安全复核，记录精确命令与结果。
3. 确认仓库可见性、许可证和发布渠道已经由项目所有者决定。
4. 仅在明确授权后提交所有变更，保持工作区干净。
5. 在当前 release commit 上创建 annotated tag：

```sh
git tag -a v1.0.0 -m "Plinth v1.0.0"
git show --stat --oneline v1.0.0
```

6. 推送 commit 和 tag；在发布记录中写入 tag、commit 和验证结果。
7. 用干净模块验证可获取和可构建：

```sh
GOFLAGS=-mod=mod go list -m github.com/plinth-go/plinth@v1.0.0
```

8. 发布后 smoke test 失败时，先停止继续宣传并记录影响；需要撤回版本时，使用 Go
   module 的 `retract` 机制发布一个修正版本，不要移动或覆盖已有 tag。

## 发布后不回写

已发布 tag 不可移动。发现问题的处理顺序是：先发布修复或 `retract` 版本，再更新文档。
分支上的内部迭代号可以继续增长，但只有 SemVer tag 定义外部可见版本。
