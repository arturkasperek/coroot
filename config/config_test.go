package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The configuration and the state of Coroot live in Postgres only: Coroot
// does not start without its connection string.
func TestConfigRequiresPostgres(t *testing.T) {
	cfg := NewConfig()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Postgres connection string is required")

	cfg = NewConfig()
	cfg.Postgres = &Postgres{ConnectionString: "postgres://coroot:coroot@postgres:5432/coroot?sslmode=disable"}
	assert.NoError(t, cfg.Validate())
}
