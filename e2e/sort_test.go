//go:build e2e

package e2e

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestOverviewTraceSortCombinations(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	svc := "e2esort-t-" + token
	oldLong := "e2esort-old-long-" + token
	midShort := "e2esort-mid-short-" + token
	newMed := "e2esort-new-med-" + token
	baseTs := time.Now().UTC().Add(-4 * time.Minute)

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 1, ServiceName: svc, SpanName: oldLong, Timestamp: baseTs, DurationNs: 200_000_000},
		{Count: 1, ServiceName: svc, SpanName: midShort, Timestamp: baseTs.Add(time.Minute), DurationNs: 10_000_000},
		{Count: 1, ServiceName: svc, SpanName: newMed, Timestamp: baseTs.Add(2 * time.Minute), DurationNs: 50_000_000},
	})
	t.Cleanup(func() { deleteTraceFixture(t, svc) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "traces",
		"include_aux": true,
		"filters":     []map[string]string{{"field": "ServiceName", "op": "=", "value": svc}},
	}

	waitUntil(t, 15*time.Second, "overview traces for sort fixture", func() bool {
		traces := fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" && sameNames(spanNames(traces), oldLong, midShort, newMed)
	})

	dateDesc := []string{newMed, midShort, oldLong}
	dateAsc := []string{oldLong, midShort, newMed}
	durDesc := []string{oldLong, newMed, midShort}
	durAsc := []string{midShort, newMed, oldLong}

	cases := []struct {
		name string
		sort any
		want []string
	}{
		{name: "omitted sort is date desc", sort: nil, want: dateDesc},
		{name: "date desc", sort: map[string]string{"by": "date", "dir": "desc"}, want: dateDesc},
		{name: "date asc", sort: map[string]string{"by": "date", "dir": "asc"}, want: dateAsc},
		{name: "date missing dir defaults desc", sort: map[string]string{"by": "date"}, want: dateDesc},
		{name: "DATE ASC mixed case", sort: map[string]string{"by": "DATE", "dir": "ASC"}, want: dateAsc},
		{name: "duration desc", sort: map[string]string{"by": "duration", "dir": "desc"}, want: durDesc},
		{name: "duration asc", sort: map[string]string{"by": "duration", "dir": "asc"}, want: durAsc},
		{name: "duration missing dir defaults desc", sort: map[string]string{"by": "duration"}, want: durDesc},
		{name: "DURATION ASC mixed case", sort: map[string]string{"by": "DURATION", "dir": "ASC"}, want: durAsc},
		{name: "unknown by falls back to date desc", sort: map[string]string{"by": "message", "dir": "desc"}, want: dateDesc},
		{name: "invalid by falls back to date desc", sort: map[string]string{"by": "drop table", "dir": "asc"}, want: dateDesc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := copyQuery(base)
			if tc.sort != nil {
				q["sort"] = tc.sort
			}
			traces := fetchOverviewTraces(t, projectID, q)
			if traces.Error != "" {
				t.Fatalf("overview traces error: %s", traces.Error)
			}
			got := spanNames(traces)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order=%v want %v", got, tc.want)
			}
		})
	}
}

func TestOverviewLogSortCombinations(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-sort-%d", time.Now().UnixNano())
	oldBody := "e2esort-old-" + token
	midBody := "e2esort-mid-" + token
	newBody := "e2esort-new-" + token
	baseTs := time.Now().UTC().Add(-4 * time.Minute)

	insertLogFixture(t, token, []logFixtureRow{
		{Count: 1, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "sort-node", Body: oldBody, Timestamp: baseTs},
		{Count: 1, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "sort-node", Body: midBody, Timestamp: baseTs.Add(time.Minute)},
		{Count: 1, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "sort-node", Body: newBody, Timestamp: baseTs.Add(2 * time.Minute)},
	})
	t.Cleanup(func() { deleteFacetFixture(t, token) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":  "messages",
		"limit": 100,
		"filters": []map[string]string{{
			"name": "e2e.facets", "op": "=", "value": token,
		}},
	}

	waitUntil(t, 15*time.Second, "overview logs for sort fixture", func() bool {
		logs := fetchOverviewLogs(t, projectID, base)
		return logs.Error == "" && sameNames(logMessages(logs), oldBody, midBody, newBody)
	})

	dateDesc := []string{newBody, midBody, oldBody}
	dateAsc := []string{oldBody, midBody, newBody}

	cases := []struct {
		name string
		sort any
		want []string
	}{
		{name: "omitted sort is date desc", sort: nil, want: dateDesc},
		{name: "date desc", sort: map[string]string{"by": "date", "dir": "desc"}, want: dateDesc},
		{name: "date asc", sort: map[string]string{"by": "date", "dir": "asc"}, want: dateAsc},
		{name: "date missing dir defaults desc", sort: map[string]string{"by": "date"}, want: dateDesc},
		{name: "DATE ASC mixed case", sort: map[string]string{"by": "DATE", "dir": "ASC"}, want: dateAsc},
		{name: "duration is ignored", sort: map[string]string{"by": "duration", "dir": "desc"}, want: dateDesc},
		{name: "duration asc is ignored", sort: map[string]string{"by": "duration", "dir": "asc"}, want: dateDesc},
		{name: "unknown by falls back to date desc", sort: map[string]string{"by": "severity", "dir": "desc"}, want: dateDesc},
		{name: "invalid by falls back to date desc", sort: map[string]string{"by": "drop table", "dir": "asc"}, want: dateDesc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := copyQuery(base)
			if tc.sort != nil {
				q["sort"] = tc.sort
			}
			logs := fetchOverviewLogs(t, projectID, q)
			if logs.Error != "" {
				t.Fatalf("overview logs error: %s", logs.Error)
			}
			got := logMessages(logs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order=%v want %v", got, tc.want)
			}
		})
	}
}

func spanNames(traces overviewTraces) []string {
	names := make([]string, 0, len(traces.Traces))
	for _, s := range traces.Traces {
		names = append(names, s.Name)
	}
	return names
}

func logMessages(logs overviewLogs) []string {
	msgs := make([]string, 0, len(logs.Entries))
	for _, e := range logs.Entries {
		msgs = append(msgs, e.Message)
	}
	return msgs
}

func sameNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	have := map[string]int{}
	for _, n := range got {
		have[n]++
	}
	for _, n := range want {
		if have[n] == 0 {
			return false
		}
		have[n]--
	}
	return true
}
