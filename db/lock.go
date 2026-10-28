package db

import (
	"context"

	"k8s.io/klog"
)

// The Postgres advisory locks Coroot instances sharing the database take.
const (
	// primaryLockId makes one instance the primary (GetPrimaryLock).
	primaryLockId = 1
	// MigrationLockId serializes the migrations of the Postgres schema.
	MigrationLockId = 2
	// ClickhouseMigrationLockId serializes the migrations of the ClickHouse
	// tables of the projects.
	ClickhouseMigrationLockId = 3
)

// WithLock runs fn while holding the advisory lock id, on a connection of its
// own: another Coroot instance that asks for the same lock waits until fn ends
// (for instance two replicas starting at once, migrating one after the other).
func (db *DB) WithLock(ctx context.Context, id int64, fn func() error) error {
	conn, err := db.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }() // a closed session releases its locks too
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", id); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", id)
	}()
	return fn()
}

// GetPrimaryLock tells whether this instance is the primary one: the one of
// the Coroot replicas sharing the database that runs the background jobs (the
// world evaluator, the watchers, the notifications). The primary holds a
// session advisory lock on a connection of its own and keeps it: it never
// gives it up while it lives, so the primary does not change from call to
// call. Postgres releases the lock when that connection ends (the instance
// stops or loses the database), and then another instance takes it. Safe for
// concurrent use.
func (db *DB) GetPrimaryLock(ctx context.Context) bool {
	db.primaryLock.Lock()
	defer db.primaryLock.Unlock()

	if db.primaryLockConn != nil {
		// still the primary while its connection, and so the lock, lives
		_, err := db.primaryLockConn.ExecContext(ctx, "SELECT 1")
		if err == nil {
			return true
		}
		klog.Warningln("lost the connection that holds the primary lock:", err)
		_ = db.primaryLockConn.Close()
		db.primaryLockConn = nil
	}

	conn, err := db.db.Conn(ctx)
	if err != nil {
		klog.Errorln(err)
		return false
	}
	var acquired bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", primaryLockId).Scan(&acquired); err != nil {
		klog.Errorln(err)
		_ = conn.Close()
		return false
	}
	if !acquired {
		_ = conn.Close()
		return false
	}
	klog.Infoln("this instance is the primary")
	db.primaryLockConn = conn
	return true
}
