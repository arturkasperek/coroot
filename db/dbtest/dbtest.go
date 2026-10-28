//go:build e2e

// Package dbtest gives integration tests a real Postgres: a fresh database
// with the full schema for every test. It connects to the dev Postgres
// (COROOT_DEV_POSTGRES_DSN, as set up by `make test-e2e`). A database of its
// own keeps a test apart from Coroot and from other tests, the primary lock
// (an advisory lock, local to a database) included. Tests that use it carry
// the e2e build tag.
package dbtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coroot/coroot/db"
	_ "github.com/lib/pq"
)

var counter atomic.Int64

// New creates a database named after the test, migrates the schema into it and
// returns it. The database is dropped when the test ends.
func New(t *testing.T) *db.DB {
	t.Helper()
	database, _ := NewWithDSN(t)
	return database
}

// NewWithDSN is New that also returns the connection string of the database,
// for a test to open it again as another Coroot replica would (Open).
func NewWithDSN(t *testing.T) (*db.DB, string) {
	t.Helper()
	dsn := NewEmpty(t)
	database := Open(t, dsn)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate %s: %v", dsn, err)
	}
	return database, dsn
}

// NewEmpty creates a database with no schema, dropped when the test ends, and
// returns its connection string.
func NewEmpty(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("COROOT_DEV_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("COROOT_DEV_POSTGRES_DSN is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	name := fmt.Sprintf("t_%d_%d", time.Now().UnixNano()%1e9, counter.Add(1))
	// the dev port-forward refuses connections for a moment after it lost
	// one (a database dropped WITH (FORCE) ends its sessions)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err = admin.Exec("CREATE DATABASE " + name)
		if err == nil || !strings.Contains(err.Error(), "connection refused") || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Open connects to a database of NewWithDSN, as another Coroot replica; it is
// closed when the test ends.
func Open(t *testing.T, dsn string) *db.DB {
	t.Helper()
	database, err := db.NewPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
