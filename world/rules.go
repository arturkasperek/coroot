package world

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

// rulesMarker is a query that has a point per computed chunk of rules: how far
// they have been computed. It depends on the set of rules, so that a rule that
// is added later is backfilled (the others are not written twice, see notBefore).
func rulesMarker(names []string) string {
	sum := md5.Sum([]byte(strings.Join(names, ",")))
	return "rr_world_computed_" + hex.EncodeToString(sum[:4])
}

// ruleHistory is how much of the past the World of a chunk reaches back. The
// rules need the context of the steps they compute: the World is built from
// what exists in its window, and a pod that was deleted a minute ago is still
// the other end of connections: with a window of only the new steps it is an
// unknown address, an "external service". The chunk cache of old built every World
// from the start of its 10 minute chunk; this is the same.
const ruleHistory = 10 * timeseries.Minute

// ruleLookahead: a rule is computed for a step when the World already holds this
// many steps after it. The metadata that tells what a connection is (the
// Kubernetes object of a pod that has just started) arrives after the
// connection does; the chunk cache of old recomputed the last chunk on every
// update and so corrected a first guess, rows here are written once.
const ruleLookahead = 2

// ruleChunk is how much of a backfill one World is built for: a World of 24 h at
// once would be heavy, and the rules are the same function of every step.
const ruleChunk = timeseries.Hour

// recordingRules computes the constructor's recording rules (application level
// aggregates that are not PromQL: connections between applications, log
// messages per application) for the steps they are missing, from the World that
// the stored results of the queries make, and stores them like any other query.
func (e *Evaluator) recordingRules(ctx context.Context, project *db.Project, ps *projectState, evaluatedTo timeseries.Time, res *Result) error {
	to := evaluatedTo.Add(-ruleLookahead * Step) // the last step the rules are computed for
	res.To = to                                  // and so the last one a page can use
	names := make([]string, 0, len(constructor.RecordingRules))
	for name := range constructor.RecordingRules {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		res.To = evaluatedTo
		return nil
	}
	marker := rulesMarker(names)
	last, err := ps.b.lastPerQuery(ctx)
	if err != nil {
		return err
	}
	// A rule that has nothing to say (an empty World, an application without logs) leaves
	// no rows, so the rows cannot tell how far the rules were computed: a marker does.
	from := to.Add(-e.backfill)
	if l, ok := last[marker]; ok {
		from = l.Add(Step)
	}
	from = from.Truncate(Step)
	if from > to {
		return nil
	}
	cacheClients := map[db.ProjectId]constructor.Cache{project.Id: ps.store}
	for chunkFrom := from; chunkFrom <= to; chunkFrom = chunkFrom.Add(ruleChunk) {
		chunkTo := chunkFrom.Add(ruleChunk - Step)
		if chunkTo > to {
			chunkTo = to
		}
		ctr := constructor.New(e.db, project, cacheClients, nil,
			constructor.OptionLoadInstanceToInstanceConnections, constructor.OptionDoNotLoadRawSLIs, constructor.OptionLoadContainerLogs)
		worldTo := chunkTo.Add(ruleLookahead * Step)
		if worldTo > evaluatedTo {
			worldTo = evaluatedTo
		}
		world, err := ctr.LoadWorld(ctx, chunkFrom.Add(-ruleHistory), worldTo, Step, nil)
		if err != nil {
			return fmt.Errorf("load world: %w", err)
		}
		var out batch
		for _, name := range names {
			// a rule that has rows is continued after them, whatever the marker says: a rule
			// added later is backfilled, the others are not written twice
			notBefore := chunkFrom // the steps of the history are context, not results
			if l, ok := last[name]; ok && l.Add(Step) > notBefore {
				notBefore = l.Add(Step)
			}
			for _, mv := range constructor.RecordingRules[name](e.db, project, world) {
				appendSeries(&out, ps.seen, name, mv, chunkTo, notBefore, chunkTo)
			}
		}
		n := len(out.points)
		out.points = append(out.points, pointRow{query: marker, t: chunkTo, v: 1})
		if err := ps.b.write(ctx, out); err != nil {
			return err
		}
		res.Series += len(out.series)
		res.Points += n
		klog.V(2).Infof("%s: recording rules for %d..%d: %d points", project.Id, chunkFrom, chunkTo, n)
	}
	return nil
}
