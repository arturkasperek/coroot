package collector

import (
	"testing"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/assert"
)

func projectWithClickHouse(id, addr string) *db.Project {
	p := &db.Project{Id: db.ProjectId(id), Name: id}
	p.Settings.Integrations.Clickhouse = &db.IntegrationClickhouse{Protocol: "native", Addr: addr, Database: "default"}
	return p
}

// Another Coroot replica may change the ClickHouse of a project: when the
// collector sees the new settings, it forgets its client and its migration
// for that project, so that it connects to and migrates the new ClickHouse;
// the other projects are left alone.
func TestCollectorFollowsTheClickHouseOfAProject(t *testing.T) {
	c := &Collector{
		migrationDone:     map[db.ProjectId]bool{},
		clickhouseClients: map[db.ProjectId]*ch.LowLevelClient{},
		clickhouseReaders: nil,
	}
	c.setProjects(map[string]*db.Project{"a": projectWithClickHouse("a", "ch-1:9000"), "b": projectWithClickHouse("b", "ch-1:9000")})
	c.migrationDone["a"], c.migrationDone["b"] = true, true

	c.setProjects(map[string]*db.Project{"a": projectWithClickHouse("a", "ch-1:9000"), "b": projectWithClickHouse("b", "ch-1:9000")})
	assert.True(t, c.migrationDone["a"], "nothing changed")

	c.setProjects(map[string]*db.Project{"a": projectWithClickHouse("a", "ch-2:9000"), "b": projectWithClickHouse("b", "ch-1:9000")})
	assert.False(t, c.migrationDone["a"], "a moved to another ClickHouse: migrated again")
	assert.True(t, c.migrationDone["b"])
	project, err := c.loadedProject("a")
	assert.NoError(t, err)
	assert.Equal(t, "ch-2:9000", project.Settings.Integrations.Clickhouse.Addr, "the batches of a use its current settings")

	c.setProjects(map[string]*db.Project{"b": projectWithClickHouse("b", "ch-1:9000")})
	_, err = c.loadedProject("a")
	assert.Error(t, err, "a is deleted: its batches write nothing")
}
