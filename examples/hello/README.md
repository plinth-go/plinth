# Runnable quickstart

This is the single runnable example in `examples/`. It uses only the Plinth
router and the standard library; it does not require Postgres or Redis.

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
