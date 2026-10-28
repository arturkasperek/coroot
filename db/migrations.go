package db

import (
	"context"
	"embed"
	"io/fs"

	"github.com/pressly/goose/v3"
	"k8s.io/klog"
)

// MigrationFiles are the Postgres schema migrations, applied in order by
// Migrate. 00001_baseline is the schema of the Coroot that had no versions:
// IF NOT EXISTS everywhere, so a database it created passes it unchanged. A
// new change of the schema is a new file; a file already released never
// changes.
//
//go:embed migrations/*.sql
var MigrationFiles embed.FS

// Migrate brings the schema to the latest version, then updates the data that
// depends on the code (the builtin alerting rules of every project, the old
// form of application categories). Replicas starting at once migrate one
// after another.
func (db *DB) Migrate() error {
	ctx := context.Background()
	return db.WithLock(ctx, MigrationLockId, func() error {
		migrations, err := fs.Sub(MigrationFiles, "migrations")
		if err != nil {
			return err
		}
		provider, err := goose.NewProvider(goose.DialectPostgres, db.db, migrations)
		if err != nil {
			return err
		}
		results, err := provider.Up(ctx)
		if err != nil {
			return err
		}
		for _, result := range results {
			klog.Infof("postgres schema migration %s applied in %s", result.Source.Path, result.Duration)
		}
		return db.updateProjectsForThisVersion()
	})
}

// updateProjectsForThisVersion adds the builtin alerting rules this version
// of Coroot has and a project lacks, and moves application categories to their
// current form. It runs at every start: it depends on the code, not on the
// schema.
func (db *DB) updateProjectsForThisVersion() error {
	projects, err := db.GetProjects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err = db.migrateApplicationCategories(project); err != nil {
			return err
		}
		_ = db.InitBuiltinAlertingRules(project.Id)
	}
	return nil
}
