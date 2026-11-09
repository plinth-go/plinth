// Package postgres provides the pgx-backed implementation of the jobs store.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/plinth-go/plinth/platform/jobs"
	"github.com/plinth-go/plinth/platform/jobs/jobutil"
)

type Store struct {
	pool *pgxpool.Pool
}

var _ jobs.Store = (*Store)(nil)

const upsertScheduleSQL = `
	INSERT INTO plinth_schedules (name, kind, payload, interval_seconds, next_run_at)
	VALUES ($1, $2, $3, $4::int, now() + make_interval(secs => $4::int))
	ON CONFLICT (name) DO UPDATE
	SET kind = EXCLUDED.kind,
	    payload = EXCLUDED.payload,
	    interval_seconds = EXCLUDED.interval_seconds,
	    enabled = true,
	    updated_at = now()
`

const disableSchedulesExceptSQL = `
	UPDATE plinth_schedules
	SET enabled = false, updated_at = now()
	WHERE enabled AND name <> ALL($1::text[])
`

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) available() error {
	if s == nil || s.pool == nil {
		return jobs.ErrStoreUnavailable
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, kind string, payload any, opts ...jobs.EnqueueOption) (jobs.Job, error) {
	kind, err := jobutil.NormalizeJobKind(kind)
	if err != nil {
		return jobs.Job{}, err
	}
	options, err := jobs.ResolveEnqueueOptions(opts...)
	if err != nil {
		return jobs.Job{}, err
	}
	body, err := jobutil.MarshalPayload(payload)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("marshal job payload: %w", err)
	}
	if err := s.available(); err != nil {
		return jobs.Job{}, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO plinth_jobs (kind, payload, run_at, max_attempts)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, kind, payload, status, run_at, attempts, max_attempts,
		          last_error, created_at, updated_at, ''::text
	`, kind, body, options.RunAt, options.MaxAttempts)
	return scanJob(row)
}

func (s *Store) Get(ctx context.Context, id string) (jobs.Job, error) {
	id, err := jobutil.NormalizeJobID(id)
	if err != nil {
		return jobs.Job{}, err
	}
	if err := s.available(); err != nil {
		return jobs.Job{}, err
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, kind, payload, status, run_at, attempts, max_attempts,
		       last_error, created_at, updated_at, ''::text
		FROM plinth_jobs
		WHERE id = $1
	`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, err
}

// ListFailed returns permanently failed jobs, newest first. Dead-letter
// administration is intentionally limited in batch size to keep responses and
// database scans bounded.
func (s *Store) ListFailed(ctx context.Context, limit, offset int) ([]jobs.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("job list limit must be between 1 and 100")
	}
	if offset < 0 {
		return nil, fmt.Errorf("job list offset must not be negative")
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, kind, payload, status, run_at, attempts, max_attempts,
		       last_error, created_at, updated_at, ''::text
		FROM plinth_jobs
		WHERE status = 'failed'
		ORDER BY updated_at DESC, id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]jobs.Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// RetryFailed returns one failed job to the queue with a fresh attempt budget.
// It never revives a running or already-pending job.
func (s *Store) RetryFailed(ctx context.Context, id string) (jobs.Job, error) {
	id, err := jobutil.NormalizeJobID(id)
	if err != nil {
		return jobs.Job{}, err
	}
	if err := s.available(); err != nil {
		return jobs.Job{}, err
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE plinth_jobs
		SET status = 'pending', attempts = 0, run_at = now(), last_error = NULL,
		    locked_by = NULL, locked_at = NULL, lease_token = NULL, updated_at = now()
		WHERE id = $1 AND status = 'failed'
		RETURNING id::text, kind, payload, status, run_at, attempts, max_attempts,
		          last_error, created_at, updated_at, ''::text
	`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, s.failedJobStateError(ctx, id)
	}
	return job, err
}

// DeleteFailed permanently removes one failed job. Running work cannot be
// removed through this administrative path.
func (s *Store) DeleteFailed(ctx context.Context, id string) error {
	id, err := jobutil.NormalizeJobID(id)
	if err != nil {
		return err
	}
	if err := s.available(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM plinth_jobs WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.failedJobStateError(ctx, id)
	}
	return nil
}

func (s *Store) failedJobStateError(ctx context.Context, id string) error {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM plinth_jobs WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: job %s is %s", jobs.ErrNotFailed, id, status)
}

func (s *Store) Claim(ctx context.Context, workerID string) (*jobs.Job, error) {
	workerID, err := jobutil.NormalizeWorkerID(workerID)
	if err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		WITH picked AS (
			SELECT id
			FROM plinth_jobs
			WHERE status = 'pending' AND run_at <= now()
			ORDER BY run_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE plinth_jobs AS job
		SET status = 'running',
		    attempts = attempts + 1,
		    locked_by = $1,
		    locked_at = now(),
		    lease_token = gen_random_uuid(),
		    updated_at = now()
		FROM picked
		WHERE job.id = picked.id
		RETURNING job.id::text, job.kind, job.payload, job.status, job.run_at,
		          job.attempts, job.max_attempts, job.last_error,
		          job.created_at, job.updated_at, job.lease_token::text
	`, workerID)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// MarkSucceeded completes exactly the claimed attempt in lease. Replaying the
// same successful lease is a no-op. It returns ErrLeaseLost when another
// attempt owns or finalized the job.
func (s *Store) MarkSucceeded(ctx context.Context, lease jobs.Lease) error {
	lease, err := normalizeLease(lease)
	if err != nil {
		return err
	}
	if err := s.available(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE plinth_jobs
		SET status = 'succeeded', locked_by = NULL, locked_at = NULL,
		    last_error = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3
		  AND lease_token = $4
	`, lease.JobID, lease.WorkerID, lease.Attempt, lease.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		settled, err := s.leaseSettled(ctx, lease, func(state jobLeaseState) bool {
			return state.status == "succeeded" && state.attempts == lease.Attempt
		})
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		return fmt.Errorf("%w: job %s", jobs.ErrLeaseLost, lease.JobID)
	}
	return nil
}

type jobLeaseState struct {
	status     string
	attempts   int
	lastError  pgtype.Text
	leaseToken string
}

func (s *Store) leaseSettled(ctx context.Context, lease jobs.Lease, matches func(jobLeaseState) bool) (bool, error) {
	var state jobLeaseState
	err := s.pool.QueryRow(ctx, `
		SELECT status, attempts, last_error, COALESCE(lease_token::text, '')
		FROM plinth_jobs
		WHERE id = $1
	`, lease.JobID).Scan(&state.status, &state.attempts, &state.lastError, &state.leaseToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return matches(state), nil
}

// MarkFailed records a failed attempt only while lease is still current. The
// database decides whether the job becomes pending again or permanently failed.
func (s *Store) MarkFailed(ctx context.Context, lease jobs.Lease, cause error, retryAt time.Time) error {
	lease, err := normalizeLease(lease)
	if err != nil {
		return err
	}
	if retryAt.IsZero() {
		return fmt.Errorf("job retry time must not be zero")
	}
	if err := s.available(); err != nil {
		return err
	}
	message := jobutil.ErrorMessage(cause)
	tag, err := s.pool.Exec(ctx, `
		UPDATE plinth_jobs
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    run_at = CASE WHEN attempts >= max_attempts THEN run_at ELSE $4 END,
		    last_error = $5,
		    locked_by = NULL,
		    locked_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3
		  AND lease_token = $6
	`, lease.JobID, lease.WorkerID, lease.Attempt, retryAt, message, lease.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		settled, err := s.leaseSettled(ctx, lease, func(state jobLeaseState) bool {
			return state.leaseToken == lease.Token && state.attempts == lease.Attempt &&
				state.lastError.Valid && state.lastError.String == message &&
				(state.status == "pending" || state.status == "failed")
		})
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		return fmt.Errorf("%w: job %s", jobs.ErrLeaseLost, lease.JobID)
	}
	return nil
}

// MarkInterrupted returns the exact claimed attempt in lease to the queue
// without spending an attempt. It returns ErrLeaseLost if another attempt owns
// the job. It is used when a worker is stopping, not when the job itself failed.
func (s *Store) MarkInterrupted(ctx context.Context, lease jobs.Lease, cause error) error {
	lease, err := normalizeLease(lease)
	if err != nil {
		return err
	}
	if err := s.available(); err != nil {
		return err
	}
	message := jobutil.ErrorMessage(cause)
	if message == "" {
		message = "worker interrupted"
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE plinth_jobs
		SET status = 'pending',
		    attempts = GREATEST(attempts - 1, 0),
		    last_error = $4,
		    locked_by = NULL,
		    locked_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND locked_by = $2 AND attempts = $3
		  AND lease_token = $5
	`, lease.JobID, lease.WorkerID, lease.Attempt, message, lease.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		settled, err := s.leaseSettled(ctx, lease, func(state jobLeaseState) bool {
			return state.leaseToken == lease.Token && state.status == "pending" && state.attempts == lease.Attempt-1 &&
				state.lastError.Valid && state.lastError.String == message
		})
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		return fmt.Errorf("%w: job %s", jobs.ErrLeaseLost, lease.JobID)
	}
	return nil
}

// RecoverStale releases running jobs whose worker lease expired. A job with no
// attempts remaining becomes failed instead of exceeding max_attempts.
func (s *Store) RecoverStale(ctx context.Context, staleAfter time.Duration) error {
	if staleAfter <= 0 {
		return fmt.Errorf("stale duration must be greater than zero")
	}
	if err := s.available(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE plinth_jobs
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    locked_by = NULL,
		    locked_at = NULL,
		    last_error = 'worker restarted while job was running',
		    updated_at = now()
		WHERE status = 'running' AND locked_at < now() - make_interval(secs => $1)
	`, staleAfter.Seconds())
	return err
}

// DispatchDueSchedules creates at most one job for each due schedule. The
// schedule row lock, job insert, and next_run_at advance commit together, so
// concurrent dispatchers cannot duplicate one tick. A commit error is safe to
// retry: a committed tick has advanced next_run_at, while a rolled-back tick
// remains due. Missed intervals while no dispatcher is running are not
// replayed; the next tick starts from now.
func (s *Store) DispatchDueSchedules(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, fmt.Errorf("schedule dispatch limit must be at least 1")
	}
	if err := s.available(); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id::text, name, kind, payload, interval_seconds
		FROM plinth_schedules
		WHERE enabled AND next_run_at <= now()
		ORDER BY next_run_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1
	`, limit)
	if err != nil {
		return 0, err
	}

	type schedule struct {
		id       string
		name     string
		kind     string
		payload  []byte
		interval int
	}
	schedules := make([]schedule, 0, limit)
	for rows.Next() {
		var s schedule
		if err := rows.Scan(&s.id, &s.name, &s.kind, &s.payload, &s.interval); err != nil {
			rows.Close()
			return 0, err
		}
		schedules = append(schedules, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, s := range schedules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plinth_jobs (kind, payload)
			VALUES ($1, $2)
		`, s.kind, s.payload); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE plinth_schedules
			SET next_run_at = now() + ($2 * interval '1 second'),
			    updated_at = now()
			WHERE id = $1
		`, s.id, s.interval); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(schedules), nil
}

// UpsertSchedule declares a durable schedule. A restart reconciles the
// declaration without moving an already waiting next_run_at forward.
func (s *Store) UpsertSchedule(ctx context.Context, schedule jobs.Schedule) error {
	prepared, err := prepareSchedule(schedule)
	if err != nil {
		return err
	}
	if err := s.available(); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, upsertScheduleSQL,
		prepared.schedule.Name,
		prepared.schedule.Kind,
		prepared.payload,
		prepared.intervalSeconds,
	)
	return err
}

type preparedSchedule struct {
	schedule        jobs.Schedule
	payload         []byte
	intervalSeconds int
}

func prepareSchedule(schedule jobs.Schedule) (preparedSchedule, error) {
	schedule, err := normalizeSchedule(schedule)
	if err != nil {
		return preparedSchedule{}, err
	}
	payload, err := jobutil.MarshalPayload(schedule.Payload)
	if err != nil {
		return preparedSchedule{}, fmt.Errorf("marshal schedule payload: %w", err)
	}
	return preparedSchedule{
		schedule:        schedule,
		payload:         payload,
		intervalSeconds: int(schedule.Interval / time.Second),
	}, nil
}

// ReconcileSchedules applies every declaration and disables undeclared rows in
// one transaction. All payloads are preflighted before the transaction begins,
// so a later serialization error cannot leave earlier declarations committed.
func (s *Store) ReconcileSchedules(ctx context.Context, schedules []jobs.Schedule) error {
	prepared := make([]preparedSchedule, 0, len(schedules))
	names := make([]string, 0, len(schedules))
	seen := make(map[string]bool, len(schedules))
	for _, schedule := range schedules {
		value, err := prepareSchedule(schedule)
		if err != nil {
			return fmt.Errorf("schedule %q: %w", schedule.Name, err)
		}
		name := value.schedule.Name
		if seen[name] {
			return fmt.Errorf("schedule %q is declared more than once", name)
		}
		seen[name] = true
		prepared = append(prepared, value)
		names = append(names, name)
	}
	if err := s.available(); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, schedule := range prepared {
		if _, err := tx.Exec(ctx, upsertScheduleSQL,
			schedule.schedule.Name,
			schedule.schedule.Kind,
			schedule.payload,
			schedule.intervalSeconds,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, disableSchedulesExceptSQL, names); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DisableSchedulesExcept reconciles removed code declarations. Schedules are
// code-owned; a row left from an older binary must not keep firing forever.
func (s *Store) DisableSchedulesExcept(ctx context.Context, names []string) error {
	names, err := jobutil.NormalizeScheduleNames(names)
	if err != nil {
		return err
	}
	if err := s.available(); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, disableSchedulesExceptSQL, names)
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.available(); err != nil {
		return err
	}
	return s.pool.Ping(ctx)
}

func normalizeLease(lease jobs.Lease) (jobs.Lease, error) {
	normalized, err := jobutil.NormalizeLease(lease.JobID, lease.WorkerID, lease.Attempt, lease.Token)
	if err != nil {
		return jobs.Lease{}, err
	}
	return jobs.Lease{
		JobID:    normalized.JobID,
		WorkerID: normalized.WorkerID,
		Attempt:  normalized.Attempt,
		Token:    normalized.Token,
	}, nil
}

func normalizeSchedule(schedule jobs.Schedule) (jobs.Schedule, error) {
	name, kind, err := jobutil.NormalizeSchedule(schedule.Name, schedule.Kind, schedule.Interval)
	if err != nil {
		return jobs.Schedule{}, err
	}
	seconds := schedule.Interval / time.Second
	if seconds > math.MaxInt32 {
		return jobs.Schedule{}, fmt.Errorf("schedule %q interval exceeds the Postgres integer-seconds limit", name)
	}
	schedule.Name = name
	schedule.Kind = kind
	return schedule, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (jobs.Job, error) {
	var job jobs.Job
	var payload []byte
	var lastError pgtype.Text
	err := row.Scan(
		&job.ID, &job.Kind, &payload, &job.Status, &job.RunAt,
		&job.Attempts, &job.MaxAttempts, &lastError,
		&job.CreatedAt, &job.UpdatedAt, &job.LeaseToken,
	)
	if err != nil {
		return jobs.Job{}, err
	}
	job.Payload = payload
	if lastError.Valid {
		job.LastError = &lastError.String
	}
	return job, nil
}
