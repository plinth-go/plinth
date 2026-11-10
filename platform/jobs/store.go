package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/plinth-go/plinth/platform/jobs/jobutil"
)

type Job struct {
	// Stable UUID assigned when the job is enqueued.
	ID string `json:"id"`
	// Registered job kind used to select the typed handler.
	Kind string `json:"kind"`
	// Opaque JSON payload decoded by the registered handler.
	Payload json.RawMessage `json:"payload"`
	// Lifecycle state: pending, running, succeeded, or failed.
	Status string `json:"status"`
	// Earliest time at which a worker may claim the job.
	RunAt time.Time `json:"run_at"`
	// Number of attempts already claimed.
	Attempts int `json:"attempts"`
	// Maximum attempts before the job becomes permanently failed.
	MaxAttempts int `json:"max_attempts"`
	// Last execution error, if any. Persisted messages are UTF-8 safe and capped at 4096 bytes.
	LastError *string `json:"last_error,omitempty"`
	// Creation timestamp.
	CreatedAt time.Time `json:"created_at"`
	// Last lifecycle transition timestamp.
	UpdatedAt time.Time `json:"updated_at"`

	// LeaseToken identifies the exact claimed attempt. It is issued only by
	// Claim and is intentionally not serialized with the job. Backends set it
	// when returning a claimed job.
	LeaseToken string `json:"-"`
}

// Lease identifies one claimed execution attempt. Terminal transitions are
// accepted only while the job is still running under this exact lease. Token
// is an opaque value minted by Claim; it prevents a reused attempt number from
// reviving an older execution after an interruption.
type Lease struct {
	JobID    string
	WorkerID string
	Attempt  int
	Token    string
}

func (j Job) Lease(workerID string) Lease {
	return Lease{JobID: j.ID, WorkerID: workerID, Attempt: j.Attempts, Token: j.LeaseToken}
}

type Schedule struct {
	Name     string
	Kind     string
	Payload  any
	Interval time.Duration
}

// Store is the complete backend contract for durable jobs and schedules.
// Implementations may use any database; the queue semantics are defined by the
// worker, scheduler, and jobutil contracts rather than by a specific driver.
type Store interface {
	Enqueue(context.Context, string, any, ...EnqueueOption) (Job, error)
	Get(context.Context, string) (Job, error)
	ListFailed(context.Context, int, int) ([]Job, error)
	RetryFailed(context.Context, string) (Job, error)
	DeleteFailed(context.Context, string) error
	Claim(context.Context, string) (*Job, error)
	MarkSucceeded(context.Context, Lease) error
	MarkFailed(context.Context, Lease, error, time.Time) error
	MarkInterrupted(context.Context, Lease, error) error
	RecoverStale(context.Context, time.Duration) error
	DispatchDueSchedules(context.Context, int) (int, error)
	UpsertSchedule(context.Context, Schedule) error
	ReconcileSchedules(context.Context, []Schedule) error
	DisableSchedulesExcept(context.Context, []string) error
	Ping(context.Context) error
}

// WorkerStore is the subset used by Worker.
type WorkerStore interface {
	Claim(context.Context, string) (*Job, error)
	MarkSucceeded(context.Context, Lease) error
	MarkFailed(context.Context, Lease, error, time.Time) error
	MarkInterrupted(context.Context, Lease, error) error
	RecoverStale(context.Context, time.Duration) error
}

// SchedulerStore is the subset used by Scheduler.
type SchedulerStore interface {
	ReconcileSchedules(context.Context, []Schedule) error
	DispatchDueSchedules(context.Context, int) (int, error)
}

// AdminStore is the failed-job subset used by administrative handlers.
type AdminStore interface {
	ListFailed(context.Context, int, int) ([]Job, error)
	RetryFailed(context.Context, string) (Job, error)
	DeleteFailed(context.Context, string) error
}

// EnqueueOptions contains the resolved enqueue settings. Backends must use
// ResolveEnqueueOptions so option handling and validation stay consistent.
type EnqueueOptions struct {
	RunAt       time.Time
	MaxAttempts int
}

type EnqueueOption func(*EnqueueOptions)

func WithDelay(delay time.Duration) EnqueueOption {
	return func(o *EnqueueOptions) {
		o.RunAt = time.Now().Add(delay)
	}
}

func WithRunAt(t time.Time) EnqueueOption {
	return func(o *EnqueueOptions) { o.RunAt = t }
}

func WithMaxAttempts(n int) EnqueueOption {
	return func(o *EnqueueOptions) { o.MaxAttempts = n }
}

// ResolveEnqueueOptions applies defaults and validates options before a backend
// touches its database.
func ResolveEnqueueOptions(opts ...EnqueueOption) (EnqueueOptions, error) {
	options := EnqueueOptions{RunAt: time.Now(), MaxAttempts: 3}
	for index, opt := range opts {
		if opt == nil {
			return EnqueueOptions{}, fmt.Errorf("job enqueue option at index %d is nil", index)
		}
		opt(&options)
	}
	if options.MaxAttempts < 1 {
		return EnqueueOptions{}, fmt.Errorf("job max attempts must be at least 1")
	}
	if options.MaxAttempts > math.MaxInt32 {
		return EnqueueOptions{}, fmt.Errorf("job max attempts must not exceed %d", math.MaxInt32)
	}
	return options, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isNilError(err error) bool {
	return isNilInterface(err)
}

func normalizeJobID(id string) (string, error) {
	return jobutil.NormalizeJobID(id)
}

func normalizeWorkerID(workerID string) (string, error) {
	return jobutil.NormalizeWorkerID(workerID)
}

func normalizeLease(lease Lease) (Lease, error) {
	normalized, err := jobutil.NormalizeLease(lease.JobID, lease.WorkerID, lease.Attempt, lease.Token)
	if err != nil {
		return Lease{}, err
	}
	return Lease{
		JobID:    normalized.JobID,
		WorkerID: normalized.WorkerID,
		Attempt:  normalized.Attempt,
		Token:    normalized.Token,
	}, nil
}

func normalizeSchedule(schedule Schedule) (Schedule, error) {
	name, kind, err := jobutil.NormalizeSchedule(schedule.Name, schedule.Kind, schedule.Interval)
	if err != nil {
		return Schedule{}, err
	}
	schedule.Name = name
	schedule.Kind = kind
	return schedule, nil
}

func normalizeScheduleNames(names []string) ([]string, error) {
	return jobutil.NormalizeScheduleNames(names)
}

func marshalPayload(payload any) ([]byte, error) {
	return jobutil.MarshalPayload(payload)
}

func errorMessage(err error) string {
	return jobutil.ErrorMessage(err)
}

const maxJobErrorBytes = jobutil.MaxErrorBytes

const truncatedJobErrorSuffix = jobutil.TruncatedErrorSuffix

var (
	ErrNotFound         = jobutil.ErrNotFound
	ErrNotFailed        = jobutil.ErrNotFailed
	ErrInvalidJobID     = jobutil.ErrInvalidJobID
	ErrInvalidLease     = jobutil.ErrInvalidLease
	ErrLeaseLost        = jobutil.ErrLeaseLost
	ErrStoreUnavailable = jobutil.ErrStoreUnavailable
)
