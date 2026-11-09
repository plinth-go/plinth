# Plinth 使用示例

这里多数文件是**文档型示例**，不是可运行的示例应用；`hello/` 是唯一可运行的 quickstart。

- 示例只回答“框架应该怎样被调用”，不包含领域模型、业务表结构、演示 CRUD 或业务规则。
- 权限、会话等基础设施可以出现最小占位 SQL，但必须明确表结构由应用定义。
- 伪代码是允许的；导入、日志、完整错误处理等样板可以省略。
- 除专门演示错误处理外，示例默认 `error` 为 `nil`，用 `_` 接收；只保留必要的返回值传播。
- 单个代码块尽量控制在 15–35 行；超出时拆成“接入 + 声明”或“配置 + 装配”两个块。
- 框架自己的类型、函数名和 tag 应尽量保持真实。示例与公开 API 冲突时，以
  [`../README.md`](../README.md) 和公开 API 快照为准。
- 除 `hello/` 外，示例不进入 `go test`、构建或发布二进制的依赖闭包。
- 真实业务应用继续放在独立仓库；`plinth-example` 是集成参考应用，不是官方学习入口。

示例放在框架仓库内，是因为它们需要和 API 同版本演进、同一 PR review、同一 tag 发布。
AI 或新维护者不需要跨仓库拼接上下文，也不会把参考应用的业务结构误当成推荐架构。

## 索引

| 想了解 | 示例 |
|---|---|
| 无外部依赖、可直接运行的最小服务 | [hello/](hello/README.md) |
| 最小 typed endpoint、路径/query/header/body、错误返回 | [minimal-http.md](minimal-http.md) |
| `app.Main`、配置、日志、自定义子命令、启动顺序 | [app-shell.md](app-shell.md) |
| JWT 认证 + Postgres 权限 + 接口鉴权 | [jwt-postgres-authz.md](jwt-postgres-authz.md) |
| 只接入声明式接口权限 | [authz.md](authz.md) |
| 只接入 CSRF（Cookie + header） | [csrf.md](csrf.md) |
| 只接入 CORS | [cors.md](cors.md) |
| 只接入限流（内存/Redis） | [ratelimit.md](ratelimit.md) |
| 只接入 Session / Cookie | [session.md](session.md) |
| 安全中间件组合顺序 | [security.md](security.md) |
| 使用者如何测试自己的业务 handler 和中间件接线 | [testing.md](testing.md) |
| Postgres、Redis、jobs、对象存储等 opt-in 能力 | [optional-platform.md](optional-platform.md) |

## 推荐阅读路径

- 第一次接入：`hello/` → `minimal-http.md` → `app-shell.md` → `testing.md`
- 做公网 API：`security.md` → `jwt-postgres-authz.md` / `authz.md` →
  `csrf.md` / `session.md` / `cors.md` / `ratelimit.md` → `../SECURITY.md`
- 只补一个能力：直接打开索引里的单篇，不需要先读安全总览
- 接入基础设施：`optional-platform.md` → `../README.md` 的“可选能力”
- AI/代码代理：先读 [../llms.txt](../llms.txt)，再按任务读取上述单篇

## 编写规则

- 不为示例发明框架里不存在的 `ExampleServer`、`NewAPI` 或配置 DSL。
- 不把某个业务的 `Order`、`User`、`Link` 领域结构写成“标准写法”。
- 需要第三方基础设施时，明确写出它是可选 import，不用伪依赖伪装成核心能力。
- 公开 API 变化时，同步更新直接覆盖该 API 的示例。
