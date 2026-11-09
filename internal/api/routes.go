package api

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/platform/jobs"
	"github.com/plinth-go/plinth/platform/pgjobs"
)

func Routes() *kernel.Router {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := New(Dependencies{
		Logger:   logger,
		Jobs:     pgjobs.NewStore(nil),
		Registry: jobs.NewRegistry(),
		Streams:  kernel.NewStreamRegistry(),
		Static:   http.NotFoundHandler(),
		Ops:      kernel.DefaultOpsSettings(),
	})
	if err != nil {
		panic(err)
	}
	return router
}
