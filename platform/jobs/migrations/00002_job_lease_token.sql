-- +goose Up
ALTER TABLE plinth_jobs ADD COLUMN IF NOT EXISTS lease_token uuid;

-- +goose Down
ALTER TABLE plinth_jobs DROP COLUMN IF EXISTS lease_token;
