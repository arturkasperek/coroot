//go:build e2e

// Package chtest gives integration tests a real ClickHouse: a fresh database
// with the full schema for every test. It connects to the dev ClickHouse
// (COROOT_DEV_CLICKHOUSE_ADDRESS, host:port of the native protocol, as set up
// by `make test-e2e`; user and password from COROOT_DEV_CLICKHOUSE_USER and
// COROOT_DEV_CLICKHOUSE_PASSWORD), and to the dev Postgres (dbtest) that holds
// the schema version of the database. Tests that use it carry the e2e build tag.
// Build the query client with clickhouse.NewClient(env.Config, project).
package chtest

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/config"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

type Env struct {
	Config   *db.IntegrationClickhouse
	Database string
	LL       *ch.LowLevelClient
	// Postgres holds the schema version of the database, as Coroot's does,
	// under ProjectId.
	Postgres        *db.DB
	ProjectId       db.ProjectId
	CollectorConfig config.CollectorConfig
}

var counter atomic.Int64

func integration(database string) *db.IntegrationClickhouse {
	user := os.Getenv("COROOT_DEV_CLICKHOUSE_USER")
	if user == "" {
		user = "default"
	}
	return &db.IntegrationClickhouse{
		Protocol: "native",
		Addr:     os.Getenv("COROOT_DEV_CLICKHOUSE_ADDRESS"),
		Auth:     utils.BasicAuth{User: user, Password: os.Getenv("COROOT_DEV_CLICKHOUSE_PASSWORD")},
		Database: database,
	}
}

// New creates a database named after the test and migrates the schema into it.
// The database is dropped when the test ends.
func New(t *testing.T) *Env {
	t.Helper()
	env := NewEmpty(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := env.LL.Migrate(ctx, env.CollectorConfig, env.Postgres, env.ProjectId, ch.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return env
}

// NewEmpty is New without the schema: the database exists, nothing in it.
func NewEmpty(t *testing.T) *Env {
	t.Helper()
	if os.Getenv("COROOT_DEV_CLICKHOUSE_ADDRESS") == "" {
		t.Skip("COROOT_DEV_CLICKHOUSE_ADDRESS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := fmt.Sprintf("t_%d_%d", time.Now().UnixNano()%1e9, counter.Add(1))
	admin, err := ch.NewLowLevelClient(ctx, integration("default"))
	if err != nil {
		t.Fatalf("connect to ClickHouse: %v", err)
	}
	defer admin.Close()
	if err = admin.CreateDB(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := ch.NewLowLevelClient(context.Background(), integration("default"))
		if err == nil {
			_ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" @on_cluster SYNC")
			c.Close()
		}
	})

	ll, err := ch.NewLowLevelClient(ctx, integration(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ll.Close)
	day := 24 * 3600 * timeseries.Second
	return &Env{
		Config:          integration(name),
		Database:        name,
		LL:              ll,
		Postgres:        dbtest.New(t),
		ProjectId:       db.ProjectId(name),
		CollectorConfig: config.CollectorConfig{TracesTTL: 30 * day, LogsTTL: 30 * day, ProfilesTTL: 30 * day, MetricsTTL: 30 * day},
	}
}

// Exec runs a statement against the test database.
func (e *Env) Exec(t *testing.T, query string) {
	t.Helper()
	if err := e.LL.Exec(context.Background(), query); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}
