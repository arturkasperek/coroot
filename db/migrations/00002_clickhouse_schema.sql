-- +goose Up
CREATE TABLE IF NOT EXISTS clickhouse_schema (
	project_id TEXT NOT NULL,
	target TEXT NOT NULL,
	version INTEGER NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (project_id, target)
);

-- +goose Down
-- forward only
