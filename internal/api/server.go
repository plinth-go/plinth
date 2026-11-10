package api

//go:generate go run ../../cmd/plinth-docgen -root ../.. -out ../generated/docs_gen.go

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/plinth-go/plinth/internal/generated"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/kernel/wshub"
	"github.com/plinth-go/plinth/platform/jobs"
	"github.com/plinth-go/plinth/platform/pgdb"
	"github.com/plinth-go/plinth/platform/redisclient"
)

const (
	apiTitle   = "Plinth"
	apiVersion = "0.1.0"
)

type Dependencies struct {
	Logger    *slog.Logger
	Postgres  *pgdb.Postgres
	Redis     *redisclient.Redis
	Jobs      jobs.Store
	Registry  *jobs.Registry
	Worker    *jobs.Worker
	Scheduler *jobs.Scheduler
	Streams   *kernel.StreamRegistry
	Static    http.Handler
	Ops       kernel.OpsSettings
}

type API struct {
	deps       Dependencies
	router     *kernel.Router
	websockets *wshub.Hub
}

func New(deps Dependencies) (*kernel.Router, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Jobs == nil || deps.Registry == nil {
		return nil, fmt.Errorf("api requires job store and registry")
	}
	if deps.Streams == nil {
		return nil, fmt.Errorf("api requires stream registry")
	}
	if deps.Static == nil {
		return nil, fmt.Errorf("api requires static file handler")
	}

	router := kernel.NewRouter(deps.Logger, apiTitle, apiVersion)
	router.SetDocs(generated.Docs())
	router.Use(
		kernel.RequestID(),
		kernel.AccessLog(deps.Logger),
		kernel.Recover(deps.Logger),
		kernel.BodyLimit(2<<20),
	)

	a := &API{
		deps:       deps,
		router:     router,
		websockets: wshub.NewHub(deps.Streams, deps.Logger),
	}
	a.routes()
	return router, nil
}

func (a *API) routes() {
	a.router.RegisterOps(a.deps.Ops.Apply(kernel.OpsConfig{
		Logger: a.deps.Logger,
		Checks: a.readyChecks(),
	}))
	a.router.Raw("GET", "/{$}", http.RedirectHandler("/web/", http.StatusTemporaryRedirect), kernel.Hidden())
	a.router.Raw("GET", "/web/", http.StripPrefix("/web/", a.deps.Static), kernel.Hidden())

	jobRoutes := a.router.Group("/v1/jobs").Tag("jobs")
	jobRoutes.Handle(a.enqueueJob, kernel.Status(http.StatusAccepted))
	jobRoutes.Handle(a.getJob)
	a.router.Group("/demo").Tag("demo").Handle(a.responseHeaders)
	a.router.Raw("GET", "/demo/events", http.HandlerFunc(a.events), kernel.Tags("demo"), kernel.Produces("text/event-stream"))
	a.router.Raw("GET", "/demo/ws", http.HandlerFunc(a.websocket), kernel.Tags("demo"), kernel.Status(http.StatusSwitchingProtocols))
	a.router.Raw("GET", "/demo/export.csv", http.HandlerFunc(a.exportCSV), kernel.Tags("demo"), kernel.Produces("text/csv"))
}

func (a *API) readyChecks() []kernel.ReadyCheck {
	checks := make([]kernel.ReadyCheck, 0, 4)
	if a.deps.Postgres != nil {
		checks = append(checks, kernel.ReadyCheck{Name: "postgres", Check: a.deps.Postgres.Ping})
	}
	if a.deps.Redis != nil {
		checks = append(checks, kernel.ReadyCheck{Name: "redis", Check: a.deps.Redis.Ping})
	}
	if a.deps.Worker != nil {
		checks = append(checks, kernel.ReadyCheck{Name: "jobs_worker", Check: func(context.Context) error {
			if !a.deps.Worker.Ready() {
				return errors.New("worker is stopped")
			}
			return nil
		}})
	}
	if a.deps.Scheduler != nil {
		checks = append(checks, kernel.ReadyCheck{Name: "jobs_scheduler", Check: func(context.Context) error {
			if !a.deps.Scheduler.Ready() {
				return errors.New("scheduler is stopped")
			}
			return nil
		}})
	}
	return checks
}

// events streams a heartbeat using server-sent events.
//
// The request remains owned by the framework stream registry so shutdown is bounded.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	err := a.deps.Streams.ServeSSE(w, r, func(ctx context.Context, sse *kernel.SSE) error {
		if err := sse.Event("ready", map[string]string{"status": "connected"}); err != nil {
			return err
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case now := <-ticker.C:
				inflight := int64(0)
				if a.deps.Worker != nil {
					inflight = a.deps.Worker.InFlight()
				}
				if err := sse.Event("tick", map[string]any{"at": now.UTC(), "inflight_jobs": inflight}); err != nil {
					return err
				}
			}
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, kernel.ErrStreamsClosed) {
		a.deps.Logger.Info("SSE stream ended", "error", err)
	}
}

// websocket upgrades a connection and echoes messages.
//
// Read deadlines keep abandoned clients from pinning resources indefinitely.
func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	err := a.websockets.Serve(w, r, func(ctx context.Context, conn *gorilla.Conn) error {
		conn.SetReadLimit(1 << 20)
		for {
			if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
				return err
			}
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return err
			}
			if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			if err := conn.WriteMessage(messageType, data); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	})
	err = wshub.NormalizeError(err)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, kernel.ErrStreamsClosed) {
		a.deps.Logger.Debug("websocket closed", "error", err)
	}
}

// exportCSV streams a small technical export.
//
// The response sets text/csv and an attachment filename without buffering the file.
func (a *API) exportCSV(w http.ResponseWriter, _ *http.Request) {
	rows := [][]string{
		{"1", "runtime", "ok"},
		{"2", "postgres", "declared"},
		{"3", "redis", "declared"},
	}
	if err := kernel.WriteCSV(w, "plinth-runtime.csv", []string{"id", "component", "state"}, func(yield func([]string) error) error {
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		a.deps.Logger.Error("CSV export failed", "error", err)
	}
}

type EnqueueJobInput struct {
	kernel.Meta `route:"POST"`
	// Job definition to persist before a worker claims it.
	Body EnqueueJobBody `body:",required"`
}

type EnqueueJobBody struct {
	// Kind must have a handler registered in the jobs.Registry.
	Kind string `json:"kind" validate:"required,min=1,max=128"`
	// Payload is decoded into the typed value accepted by that handler.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Optional earliest execution time; omit to run immediately.
	RunAt *time.Time `json:"run_at,omitempty"`
	// Optional retry ceiling; defaults to three attempts.
	MaxAttempts int `json:"max_attempts,omitempty" validate:"omitempty,min=1,max=20"`
}

// enqueueJob creates a durable job.
//
// The framework only accepts kinds with a registered typed handler; arbitrary
// payload execution is not exposed.
func (a *API) enqueueJob(ctx context.Context, in EnqueueJobInput) (jobs.Job, error) {
	if err := a.deps.Registry.ValidatePayload(in.Body.Kind, in.Body.Payload); err != nil {
		return jobs.Job{}, kernel.Unprocessable(err.Error())
	}
	options := make([]jobs.EnqueueOption, 0, 2)
	if in.Body.RunAt != nil {
		options = append(options, jobs.WithRunAt(*in.Body.RunAt))
	}
	if in.Body.MaxAttempts > 0 {
		options = append(options, jobs.WithMaxAttempts(in.Body.MaxAttempts))
	}
	job, err := a.deps.Jobs.Enqueue(ctx, in.Body.Kind, in.Body.Payload, options...)
	if err != nil {
		return jobs.Job{}, kernel.Internal(err)
	}
	return job, nil
}

type GetJobInput struct {
	kernel.Meta `route:"GET /{id}"`
	// Job UUID returned by the enqueue endpoint.
	ID string `path:"id" validate:"required,uuid"`
}

// getJob returns the durable lifecycle state of one job.
//
// The path identifier must be a UUID and is validated before the database call.
func (a *API) getJob(ctx context.Context, in GetJobInput) (jobs.Job, error) {
	job, err := a.deps.Jobs.Get(ctx, in.ID)
	if errors.Is(err, jobs.ErrNotFound) {
		return jobs.Job{}, kernel.NotFound("job not found")
	}
	if err != nil {
		return jobs.Job{}, kernel.Internal(err)
	}
	return job, nil
}

type ResponseHeadersOutput struct {
	// Headers are applied to the HTTP response and omitted from the JSON body.
	Headers kernel.Headers `json:"-"`
	// Request ID propagated by the framework middleware.
	RequestID string `json:"request_id"`
}

func (o ResponseHeadersOutput) ResponseHeaders() http.Header { return http.Header(o.Headers) }

type ResponseHeadersInput struct {
	kernel.Meta `route:"GET /response-headers"`
}

// responseHeaders demonstrates per-response headers while keeping a typed body.
func (a *API) responseHeaders(ctx context.Context, _ ResponseHeadersInput) (ResponseHeadersOutput, error) {
	return ResponseHeadersOutput{
		Headers: kernel.Headers{
			"Cache-Control":        {"no-store"},
			"X-Plinth-Response":    {"typed-header"},
			"X-Content-Language":   {"en"},
			"X-Framework-Contract": {"net-http"},
		},
		RequestID: kernel.RequestIDFromContext(ctx),
	}, nil
}
