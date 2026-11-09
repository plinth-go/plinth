-- +goose Up
CREATE TABLE IF NOT EXISTS plinth_jobs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind          text NOT NULL,
    payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
    status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    run_at        timestamptz NOT NULL DEFAULT now(),
    attempts      integer NOT NULL DEFAULT 0,
    max_attempts  integer NOT NULL DEFAULT 3,
    last_error    text,
    locked_by     text,
    locked_at     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS plinth_jobs_due_idx
    ON plinth_jobs (run_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS plinth_jobs_status_idx
    ON plinth_jobs (status, created_at DESC);

CREATE TABLE IF NOT EXISTS plinth_schedules (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text NOT NULL UNIQUE,
    kind              text NOT NULL,
    payload           jsonb NOT NULL DEFAULT '{}'::jsonb,
    interval_seconds  integer NOT NULL CHECK (interval_seconds > 0),
    next_run_at       timestamptz NOT NULL DEFAULT now(),
    enabled           boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS plinth_schedules_due_idx
    ON plinth_schedules (next_run_at)
    WHERE enabled;

-- +goose Down
DROP TABLE IF EXISTS plinth_schedules;
DROP TABLE IF EXISTS plinth_jobs;
