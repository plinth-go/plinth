# Runnable quickstart

This is the dependency-free quickstart in `examples/`. It uses only the Plinth
router and the standard library; it does not require Postgres or Redis. See
[`../tls-http2/`](../tls-http2/README.md) for a runnable TLS and HTTP/2
deployment example.

From the repository root:

```sh
go run ./examples/hello
```

In another terminal:

```sh
curl http://127.0.0.1:8080/hello/Ada
# {"message":"hello, Ada"}
```

Set `ADDR` to change the listen address. `main_test.go` starts the same router
with `httptest`, so `go test ./...` covers the example.
