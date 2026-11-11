# pprof 与火焰图

目标：显式、临时地采集进程级 profile，同时避免让正常构建中的框架依赖修改
`http.DefaultServeMux`。

## 框架边界

pprof 采样的是整个 Go 进程，不是某一个 listener。同一进程内的业务 HTTP、管理
HTTP、worker、scheduler 和数据库连接池都会进入 CPU、heap、goroutine 等 profile。
独立 listener 只是 profile 数据的 HTTP 传输入口，不会自动按 listener、route 或租户
拆分；请求指标和 route 拆分使用 `platform/observability/prommetrics` 与
`platform/observability/oteltrace`。

`net/http/pprof` 在 import 的 init 阶段向 `http.DefaultServeMux` 注册
`/debug/pprof/*`。这是标准库行为，任何 wrapper 都无法消除，因此 Plinth 不提供 pprof
公共包或自动调试端口，核心包和框架可选包也由依赖测试禁止引入它。

应用一旦 import 该包，同一进程内任何仍在服务 `http.DefaultServeMux` 的组件都会暴露
这些 endpoint。Plinth 的 router 使用自己的 `http.ServeMux`，但业务或第三方库可能另有
DefaultServeMux 使用方，启用前必须审计。

## 构建隔离

把 import 放进 build tag，保证普通发布二进制完全不包含 pprof。`pprof_debug.go`：

```go
//go:build pprof

package main

import (
	"net/http"
	"net/http/pprof"
)

func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
```

普通构建提供空实现，`pprof_disabled.go`：

```go
//go:build !pprof

package main

import "net/http"

func pprofHandler() http.Handler { return nil }
```

在 `Serve` 中只在显式配置管理地址时启动独立 listener：

```go
if cfg.PProfAddr != "" {
	handler := pprofHandler()
	if handler == nil {
		return errors.New("binary was built without -tags pprof")
	}
	rt.Add(runtime.NewHTTP(cfg.PProfAddr, handler, nil, logger.Slog()))
}
```

调试构建使用 `go build -tags pprof`。管理地址优先使用 `127.0.0.1:6060`，通过 SSH 或
Kubernetes port-forward 访问；不要在公网 listener 上暴露 pprof。必须绑定非 loopback
时，在 handler 外层增加认证或 IP allowlist，并确认同一调试构建中不存在意外服务的
`http.DefaultServeMux`。

## 采集与火焰图

先对业务施加真实流量，再采集 CPU profile：

```sh
go tool pprof -http=127.0.0.1:8081 \
  'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
```

浏览器打开 `http://127.0.0.1:8081/ui/flamegraph`，或进入 pprof UI 后选择
`Flame Graph`。常用 profile：

```sh
# 当前堆内存
go tool pprof -http=127.0.0.1:8081 'http://127.0.0.1:6060/debug/pprof/heap'

# 累计分配，排查 alloc_count/alloc_space
go tool pprof -http=127.0.0.1:8081 'http://127.0.0.1:6060/debug/pprof/allocs'

# goroutine 堆积
go tool pprof -http=127.0.0.1:8081 'http://127.0.0.1:6060/debug/pprof/goroutine'
```

远程机器先转发管理端口：

```sh
ssh -L 6060:127.0.0.1:6060 user@server
# 或
kubectl port-forward pod/<pod-name> 6060:6060
```

需要查看锁竞争时，临时启用 mutex/block profile：

```go
goruntime.SetMutexProfileFraction(1)
goruntime.SetBlockProfileRate(1)
```

这两个设置有明显的运行时开销，只应在诊断期间启用。多副本部署时每个进程都是独立
样本；按需转发到目标 pod，采集结束后删除 pprof build 产物或关闭调试部署。
