# Plinth 安全边界

Plinth 是应用内核，不是托管服务，也不是完整的访问控制产品。安全责任按边界划分：
框架负责自己写出的 HTTP 契约、可选安全中间件的默认值和基础设施接线；业务身份、
权限内容、代理信任和上传策略由应用或网关负责。

## 框架负责的控制

- typed `body` 只接受严格 JSON：拒绝 `null`、重复键、未知成员和尾随值；失败不提交
  部分响应。
- `BodyLimit` 对声明的超限 `Content-Length` 在读取前返回 413，对未知长度或 chunked
  body 使用 `http.MaxBytesReader` 在读取中兜底。它不替代上传端点的独立大小策略。
- RFC 9457 `Problem` 对 5xx 隐藏 title/detail，只返回通用错误；原始错误和 detail
  进入日志。4xx 的 detail 仍由应用决定，不能把秘密放入公开业务错误。
- 动态 response header 在提交前校验名称、值、重复 key 和 framing/hop-by-hop 字段；
  `Content-Length`、`Transfer-Encoding`、`Connection`、`Upgrade` 等由 `net/http` 控制。
- `webui` 静态文件 handler 拒绝目录列表、非普通文件、越过静态根目录的路径和 symlink；
  文件响应带 `nosniff`，错误响应不缓存。
- WebSocket 默认使用同源 `Origin` 检查。需要跨域时，应用必须显式提供经过审计的
  upgrade/鉴权边界，不能关闭检查后才补业务授权。
- `X-Request-ID` 只复用单个、长度有限、可打印 ASCII 的客户端值，避免日志注入；其他
  情况生成新值。
- TLS、HTTP/2、h2c 只做配置组合校验并接到 `net/http`/`crypto/tls`；框架不复制协议栈。
- 健康检查、OpenAPI 和静态资源默认使用 `no-store`/`nosniff` 等边界；需要公开或隐藏
  哪些运维端点由应用配置。
- `kernel/cors` 只接受显式 origin/method/header allowlist；通配符 origin 不允许与
  credentials 组合，method/header 不接受通配符。被拒绝的 preflight 返回 403，允许的
  preflight 返回 204。CORS 不替代认证、授权或 CSRF。
- `kernel/authz` 在路由注册期校验并去重 `authz` 权限声明，在匹配路由的中间件中执行
  all-of 权限检查。缺失权限返回 403，权限源失败 fail closed 为 500；权限源仍由应用
  提供，框架不推断业务身份或权限内容。
- `platform/csrf` 使用 32 字节随机 token，store 只保存 SHA-256 标识，比较 cookie 与
  请求头时使用常量时间比较。默认保护状态变更方法；`SameSite=None` 必须 `Secure`。
  内存 store 有容量上限并 fail closed；Redis store 不依赖 Lua。
- `platform/ratelimit` 的默认 key 使用匹配 operation ID 和直接连接地址，不信任
  `X-Forwarded-For`。limiter 错误或内存容量耗尽 fail closed 为 500；超限为 429。
  Redis 实现支持 Lua 和命令策略，并可在启动期校验服务端版本。
- `platform/session` 固定 session cookie 为 `HttpOnly`，在 store 中只保存 hash 后的
  token；`SameSite=None` 和 Partitioned cookie 都要求 `Secure`。多实例必须使用共享
  store，进程内 store 只适合单实例和测试。

## 应用或网关负责的控制

- 认证和授权策略的具体身份来源、权限模型与业务规则；CORS origin allowlist、CSRF
  启用范围、限流额度和 IP 信任边界。框架只提供可选中间件，不会替应用决定这些策略。
- 网关级 DDoS 防护、恶意请求拦截和全局连接/带宽限制；进程内或 Redis 固定窗口限流
  都不是网络层防护。
- 反向代理后的 `X-Forwarded-*` 信任策略；Plinth 不会自动推断客户端 scheme 或真实 IP。
- `multipart/form-data`、大文件上传、对象存储、临时文件清理和恶意内容检测。
- 传给 `WriteFile`/`WriteAttachment` 的文件路径或文件名。不要直接把未校验的用户输入
  当作服务器文件路径；下载文件名也不能包含路径分隔符或控制字符。
- 数据库参数化、迁移凭据、任务 payload 的业务校验、日志中的敏感字段脱敏和密钥管理。
- 业务 4xx detail 的内容审查；任何内部错误都应通过 `kernel.Internal` 或同样会隐藏
  detail 的路径返回。

## 审计检查

发布前至少运行：

```sh
make generate
make fmt
make release-check
```

`make release-check` 还会运行 `actionlint` 和 `govulncheck`。仓库启用的 CodeQL default
setup 负责持续代码扫描。还应使用真实 server 复核 TLS/代理部署、静态文件根目录、上传
路由的 body 限制、CORS 是否包在 router 外层、认证/授权/CSRF/session 中间件顺序、
Secure cookie 和 WebSocket 同源策略。漏洞扫描不能替代这些部署边界复核或框架单元测试。

发现安全问题时，请提供最小复现、受影响版本/commit、攻击前提和可能的缓解方式；不要在
公开 issue 中粘贴真实凭据或生产数据。

## 报告安全问题

请使用 GitHub 的 Private Vulnerability Reporting 私密报告：仓库 **Security** 页面 →
**Advisories** → **Report a vulnerability**。不要在公开 issue 中披露未修复漏洞、真实
凭据或生产数据。若私密报告入口不可用，请通过维护者的 GitHub 主页联系方式联系。
