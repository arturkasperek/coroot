package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

const defaultPostgresTimeoutSecond = "30"

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
	ErrReadonly = errors.New("readonly")
)

// DB is the Postgres database that keeps the configuration and the state of
// Coroot (projects, users, settings, alerting rules, alerts, incidents). It
// is the one store shared by every Coroot replica.
type DB struct {
	db *sql.DB

	// primaryLockConn holds the primary lock (GetPrimaryLock) while this
	// instance is the primary; primaryLock guards it.
	primaryLock     sync.Mutex
	primaryLockConn *sql.Conn
}

func NewPostgres(dsn string) (*DB, error) {
	var err error
	if dsn, err = addPostgresConnectTimeout(dsn); err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	return &DB{db: db}, nil
}

func addPostgresConnectTimeout(dsn string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := u.Query()
		if q.Get("connect_timeout") == "" {
			q.Set("connect_timeout", defaultPostgresTimeoutSecond)
		}
		u.RawQuery = q.Encode()
		return u.String(), nil
	} else {
		if !strings.Contains(dsn, "connect_timeout=") {
			dsn += " connect_timeout=" + defaultPostgresTimeoutSecond
		}
		return dsn, nil
	}
}

// Close closes the database, giving up the primary lock if this instance
// holds it.
func (db *DB) Close() error {
	db.primaryLock.Lock()
	if db.primaryLockConn != nil {
		_ = db.primaryLockConn.Close()
		db.primaryLockConn = nil
	}
	db.primaryLock.Unlock()
	return db.db.Close()
}

func (db *DB) DB() *sql.DB {
	return db.db
}

func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.db.Exec(query, args...)
}

func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.db.Query(query, args...)
}

func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.db.QueryRow(query, args...)
}

func (db *DB) IsUniqueViolationError(err error) bool {
	e, ok := err.(*pq.Error)
	return ok && e.Code.Name() == "unique_violation"
}

func (db *DB) GetDeploymentUuid() (string, error) {
	settingKey := "deployment_uuid"
	var id string
	err := db.GetSetting(settingKey, &id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	uid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	id = uid.String()
	err = db.SetSetting(settingKey, id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func marshal[T any](v *T) (*string, error) {
	if v == nil {
		return nil, nil
	}
	d, err := json.Marshal(v)
	s := string(d)
	return &s, err
}

func unmarshal[T any](s string, v **T) error {
	if s == "" {
		return nil
	}
	var vv T
	*v = &vv
	return json.Unmarshal([]byte(s), &vv)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
