# Plinth Roadmap

本文件描述方向性计划，不是交付承诺或日期承诺。`v1.x` 内继续保持公开 API、wire
contract、默认安全行为和依赖边界兼容；路线图不构成提前修改这些契约的许可。

当前阶段的首要目标不是扩大 API，而是让第一次接入、长期运行和问题反馈都更可靠。

## v1.1 候选

`v1.1` 聚焦兼容性增强、验证深度和部署可操作性：

- [ ] 增加 macOS 与 Windows CI 覆盖：<https://github.com/plinth-go/plinth/issues/15>
- [ ] 定期运行更长时间的 fuzz：<https://github.com/plinth-go/plinth/issues/16>
- [ ] 建立路由与绑定 benchmark 基线：<https://github.com/plinth-go/plinth/issues/17>
- [ ] 增加可运行的 TLS / HTTP/2 部署示例：<https://github.com/plinth-go/plinth/issues/18>

这些 issue 已加入 GitHub `v1.1` milestone。范围、优先级和是否进入该版本以 milestone
和 issue 讨论为准；未完成项不应为了赶版本而降级测试或安全要求。

## 选择原则

1. 先解决真实接入、部署和兼容性问题，再增加新的框架能力。
2. 核心包保持薄，可选能力继续放在独立包中，并由应用显式 import。
3. 新依赖必须有明确维护收益，不能由一个小而可测试的标准库实现替代。
4. 公开行为必须有测试或可重复的验证路径，文档示例优先使用真实 API。

## v1.1 暂不计划

- 第二个 router、DI 容器、ORM 或隐式全局配置系统。
- 让核心包自动 import 数据库、Redis、JWT、对象存储、指标或 tracing 集成。
- 为了隐藏未使用的可选依赖而拆分多个 Go module；当前单 module 的兼容性和维护成本更可控。
- 把 `plinth-example` 变成官方学习入口；可运行 quickstart 仍位于 `examples/hello`。

## 路线图变更

新的功能请求请使用 feature request 模板。维护者会在范围、兼容性和验证方式明确后，把
接受的工作放入具体 milestone；路线图文件本身不替代 issue 状态。
