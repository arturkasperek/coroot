-- +goose Up
-- metrics are read from ClickHouse only: the Prometheus config of a project is unused
ALTER TABLE project DROP COLUMN IF EXISTS prometheus;

-- +goose Down
-- forward only
