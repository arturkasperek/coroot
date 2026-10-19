package collector

import (
	"context"
	"time"

	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

const (
	// applicationDirectoryRefresh is how often the application directory of a
	// project is built again, so that new pods and their IPs are known soon.
	applicationDirectoryRefresh = 30 * time.Second
	// applicationDirectoryRetry is how often it is built while there is none
	// yet (the metrics of a project not loaded yet after a start).
	applicationDirectoryRetry = 5 * time.Second
	// applicationDirectoryWindow is the time range of the world the directory
	// is built from: long enough to have every running pod, short enough for
	// an IP to belong to one pod.
	applicationDirectoryWindow      = 10 * timeseries.Minute
	applicationDirectoryLoadTimeout = 30 * time.Second
)

type projectApplicationDirectory struct {
	directory *ApplicationDirectory
	loadedAt  time.Time
	loading   bool
	// firstBuildOver is set once the first build is over, built or not.
	firstBuildOver bool
}

// getApplicationDirectory returns the last application directory of a project
// (nil when none could be built) and whether its first build is over, and
// starts building a new one in the background when it is older than
// applicationDirectoryRefresh (applicationDirectoryRetry while there is none).
// It never waits for a build.
func (c *Collector) getApplicationDirectory(project *db.Project) (*ApplicationDirectory, bool) {
	c.applicationDirectoriesLock.Lock()
	defer c.applicationDirectoriesLock.Unlock()
	entry := c.applicationDirectories[project.Id]
	if entry == nil {
		entry = &projectApplicationDirectory{}
		c.applicationDirectories[project.Id] = entry
	}
	refresh := applicationDirectoryRefresh
	if entry.directory == nil {
		refresh = applicationDirectoryRetry
	}
	if !entry.loading && time.Since(entry.loadedAt) > refresh {
		entry.loading = true
		go c.refreshApplicationDirectory(project, entry)
	}
	return entry.directory, entry.firstBuildOver
}

func (c *Collector) refreshApplicationDirectory(project *db.Project, entry *projectApplicationDirectory) {
	directory, err := c.buildApplicationDirectory(project)
	if err != nil {
		klog.Warningln("application directory of project", project.Id, err)
	}
	c.applicationDirectoriesLock.Lock()
	defer c.applicationDirectoriesLock.Unlock()
	entry.loading = false
	entry.loadedAt = time.Now() // after a failure too, not to retry on every span
	entry.firstBuildOver = true
	if directory != nil {
		entry.directory = directory
	}
}

// buildApplicationDirectory loads the recent world of a project from the
// metrics cache, as the agent config endpoint does; nil when there is nothing
// to load yet.
func (c *Collector) buildApplicationDirectory(project *db.Project) (*ApplicationDirectory, error) {
	if project.Multicluster() {
		return nil, nil
	}
	cacheClient := c.evaluator.Client(project.Id)
	to, err := cacheClient.GetTo()
	if err != nil || to.IsZero() {
		return nil, err
	}
	from := to.Add(-applicationDirectoryWindow)
	step, err := cacheClient.GetStep(from, to)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), applicationDirectoryLoadTimeout)
	defer cancel()
	ctr := constructor.New(c.db, project, map[db.ProjectId]constructor.Cache{project.Id: cacheClient}, nil)
	world, err := ctr.LoadWorld(ctx, from, to, step, nil)
	if err != nil {
		return nil, err
	}
	return NewApplicationDirectory(world), nil
}
