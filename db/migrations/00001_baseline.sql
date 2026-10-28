-- +goose Up
CREATE TABLE IF NOT EXISTS project (
	id TEXT NOT NULL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	prometheus TEXT
);
ALTER TABLE project ADD COLUMN IF NOT EXISTS settings text;

CREATE TABLE IF NOT EXISTS check_configs (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	configs TEXT,
	PRIMARY KEY (project_id, application_id)
);

CREATE TABLE IF NOT EXISTS incident (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	key TEXT NOT NULL,
	opened_at INT NOT NULL,
	resolved_at INT NOT NULL DEFAULT 0,
	severity INT NOT NULL,
	PRIMARY KEY (project_id, application_id, opened_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS incident_key ON incident (project_id, key);
CREATE INDEX IF NOT EXISTS incident_project_id_resolved_opened ON incident (project_id, (resolved_at = 0), opened_at);
ALTER TABLE incident ADD COLUMN IF NOT EXISTS details text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca_status text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca_summary text;

CREATE TABLE IF NOT EXISTS incident_notification (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	incident_key TEXT NOT NULL,
	status INT NOT NULL,
	destination TEXT NOT NULL,
	timestamp INT NOT NULL,
	sent_at INT NOT NULL DEFAULT 0,
	external_key TEXT NOT NULL DEFAULT '',
	details TEXT
);

CREATE TABLE IF NOT EXISTS alert_notification (
	project_id TEXT NOT NULL REFERENCES project(id),
	alert_id TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	application_id TEXT NOT NULL,
	status INT NOT NULL,
	destination TEXT NOT NULL,
	timestamp INT NOT NULL,
	sent_at INT NOT NULL DEFAULT 0,
	external_key TEXT NOT NULL DEFAULT '',
	details TEXT
);

CREATE TABLE IF NOT EXISTS application_deployment (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	name TEXT NOT NULL,
	started_at INT NOT NULL,
	finished_at INT NOT NULL DEFAULT 0,
	details TEXT,
	metrics_snapshot TEXT,
	notifications TEXT,
	PRIMARY KEY (project_id, application_id, started_at)
);

CREATE TABLE IF NOT EXISTS application_settings (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	settings TEXT NOT NULL,
	PRIMARY KEY (project_id, application_id)
);

CREATE TABLE IF NOT EXISTS dashboards (
	project_id TEXT NOT NULL REFERENCES project(id),
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	config TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (project_id, id)
);

CREATE TABLE IF NOT EXISTS settings (
	name TEXT NOT NULL PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id SERIAL PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	password TEXT NOT NULL,
	roles TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS alerting_rule (
	id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES project(id),
	config TEXT NOT NULL,
	enabled INT NOT NULL DEFAULT 1,
	builtin INT NOT NULL DEFAULT 0,
	created_at INT NOT NULL,
	updated_at INT NOT NULL,
	PRIMARY KEY (id, project_id)
);
CREATE INDEX IF NOT EXISTS alerting_rule_project ON alerting_rule (project_id);

CREATE TABLE IF NOT EXISTS alert (
	id TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	application_category TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL,
	summary TEXT NOT NULL,
	details TEXT,
	opened_at INT NOT NULL,
	resolved_at INT NOT NULL DEFAULT 0,
	updated_at INT NOT NULL,
	suppressed INT NOT NULL DEFAULT 0,
	resolved_by TEXT NOT NULL DEFAULT '',
	report TEXT NOT NULL DEFAULT '',
	pattern_words TEXT NOT NULL DEFAULT '',
	manually_resolved_at INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS alert_project_resolved ON alert (project_id, resolved_at);
CREATE INDEX IF NOT EXISTS alert_fingerprint_resolved ON alert (fingerprint, resolved_at);
CREATE INDEX IF NOT EXISTS alert_fingerprint_suppressed ON alert (fingerprint, suppressed);
CREATE INDEX IF NOT EXISTS alert_project_rule_resolved ON alert (project_id, rule_id, resolved_at);

CREATE TABLE IF NOT EXISTS mcp_session (
	id TEXT NOT NULL PRIMARY KEY,
	project_id TEXT NOT NULL,
	updated_at BIGINT NOT NULL
);

-- +goose Down
-- forward only
