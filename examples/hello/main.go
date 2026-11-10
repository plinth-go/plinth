package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/utils"
)

type helloInput struct {
	kernel.Meta `route:"GET /hello/{name}"`

	Name string `path:"name" validate:"required,min=1,max=40"`
}

type helloOutput struct {
	Message string `json:"message"`
}

func newRouter(logger *slog.Logger) *kernel.Router {
	router := kernel.NewRouter(logger, "Plinth quickstart", "dev")
	router.Use(
		kernel.RequestID(),
		kernel.AccessLog(logger),
		kernel.Recover(logger),
		kernel.BodyLimit(1<<20),
	)
	router.Handle(hello)
	return router
}

func hello(_ context.Context, in helloInput) (helloOutput, error) {
	return helloOutput{Message: "hello, " + in.Name}, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	addr := utils.IfEmptyElse(os.Getenv("ADDR"), "127.0.0.1:8080")

	server := &http.Server{
		Addr:              addr,
		Handler:           newRouter(logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
