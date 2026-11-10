# 可运行的 TLS、HTTP/2 与反向代理示例

> **仅用于本地开发。** 这里生成的证书是有效期为 24 小时的自签名 localhost 证书，
> 不包含主机验证、CA 信任、轮换、吊销或私钥托管方案。生产证书、TLS 策略和代理信任
> 始终由应用或运维平台负责。

这个示例使用 `runtime.NewHTTP` 启动真实 listener，并演示两种部署形状：

- `direct`：Plinth 进程直接加载证书，在 TLS 上协商 HTTP/2。
- `proxy`：Plinth 只监听明文 HTTP，证书、HTTP/2 和公网入口由反向代理负责。

## 1. 生成开发证书

从仓库根目录运行：

```sh
go run ./examples/tls-http2 -generate-dev-cert
# created development-only files .dev-tls/tls.crt and .dev-tls/tls.key
```

生成器使用 ECDSA P-256，证书主题明确标记为 `Plinth development only`，在 POSIX
系统上请求私钥权限 `0600`。它不会覆盖已有文件；需要重新生成时先删除 `.dev-tls/`。

## 2. 进程内 TLS + HTTP/2

```sh
go run ./examples/tls-http2
```

另开终端请求：

```sh
curl --http2 -ksS https://127.0.0.1:8443/
# {"message":"Plinth TLS/HTTP example","protocol":"HTTP/2.0","tls":true}
```

也可以只查看协商版本：

```sh
curl --http2 -ksS -o /dev/null -w '%{http_version}\n' https://127.0.0.1:8443/
# 2
```

`-k` 只用于信任这个临时开发证书；不要把开发证书加入系统信任库。该模式对应
`HTTPConfig.TLSCertFile`、`TLSKeyFile` 和 `EnableHTTP2=true`。也可以改用应用拥有的
`TLSConfig`，但证书加载、ALPN、握手和连接生命周期仍由 `crypto/tls` 与 `net/http`
负责。

## 3. 反向代理终止 TLS

让 Plinth 只在回环地址提供明文 HTTP：

```sh
go run ./examples/tls-http2 -mode proxy -addr 127.0.0.1:8080
```

```sh
curl -sS http://127.0.0.1:8080/
# {"message":"Plinth TLS/HTTP example","protocol":"HTTP/1.1","tls":false}
```

代理负责公网 listener、证书、TLS 策略、HTTP/2 ALPN 和连接限制，再把普通 HTTP/1.1
请求转发到 `127.0.0.1:8080` 或私网地址。Plinth 不应在这个边界上加载证书，也不应把
该 listener 暴露到不可信网络。

| 设置 | `direct` | `proxy` |
|---|---|---|
| Plinth listener | TLS，默认 `127.0.0.1:8443` | 明文，示例用 `127.0.0.1:8080` |
| 证书位置 | 应用进程 | 反向代理或负载均衡器 |
| HTTP/2 | 进程内 ALPN 协商 | 代理对客户端协商；回源可为 HTTP/1.1 |
| `TLSCertFile` / `TLSKeyFile` | 必填 | 留空 |
| `EnableHTTP2` | `true` | `false` |

Plinth 不会自动信任或推断 `X-Forwarded-For`、`X-Forwarded-Proto`、Forwarded 等头。
只有应用明确配置了可信代理边界时，才能读取它们；否则任意客户端都可以伪造来源和
scheme。证书签发、续期、私钥权限、代理 ACL、请求头清洗和公网 TLS 版本策略都不由
这个示例代管。

## 验证

测试会生成临时证书，并启动真实的 loopback listener：

```sh
go test ./examples/tls-http2
```

覆盖内容包括开发证书生成、重复生成保护、直接 TLS 模式协商到 HTTP/2，以及代理模式下
保持明文 HTTP/1.1。
