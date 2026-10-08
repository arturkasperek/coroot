package promql

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	promModel "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// The Prometheus HTTP API (query_range, series, label values, metadata) on top
// of the metrics table: the explorer and the custom panels of the UI use it.

type errorType string

const (
	errorNone     errorType = ""
	errorExec     errorType = "execution"
	errorBadData  errorType = "bad_data"
	errorInternal errorType = "internal"
)

type response struct {
	Status    string      `json:"status"`
	Data      interface{} `json:"data,omitempty"`
	ErrorType errorType   `json:"errorType,omitempty"`
	Error     string      `json:"error,omitempty"`
}

func write(w http.ResponseWriter, err error, typ errorType, data interface{}) {
	resp := &response{Status: "success", Data: data}
	if err != nil {
		resp.Status, resp.Error, resp.ErrorType = "error", err.Error(), typ
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func parseTime(s string) (time.Time, error) {
	if unixSeconds, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Unix(int64(unixSeconds), 0).UTC(), nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func parseDuration(s string) (time.Duration, error) {
	if d, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(d * float64(time.Second)), nil
	}
	if d, err := promModel.ParseDuration(s); err == nil {
		return time.Duration(d), nil
	}
	return 0, fmt.Errorf("cannot parse %q to a valid duration", s)
}

type matrixSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]interface{}  `json:"values"`
}

// matrix is the "matrix" result type of the Prometheus API: [unix seconds, "value"].
func matrix(series []rawSeries) []matrixSeries {
	res := make([]matrixSeries, 0, len(series))
	for _, s := range series {
		m := matrixSeries{Metric: s.tags, Values: make([][2]interface{}, 0, len(s.ts))}
		for i, t := range s.ts {
			if math.IsNaN(s.vs[i]) { // the series has ended (Prometheus has no point there)
				continue
			}
			m.Values = append(m.Values, [2]interface{}{float64(t), strconv.FormatFloat(s.vs[i], 'f', -1, 64)})
		}
		if len(m.Values) > 0 {
			res = append(res, m)
		}
	}
	return res
}

func (c *Client) QueryRangeHandler(r *http.Request, w http.ResponseWriter) {
	start, err := parseTime(r.FormValue("start"))
	if err != nil {
		write(w, err, errorBadData, nil)
		return
	}
	end, err := parseTime(r.FormValue("end"))
	if err != nil {
		write(w, err, errorBadData, nil)
		return
	}
	if end.Before(start) {
		write(w, errors.New("end timestamp must not be before start time"), errorBadData, nil)
		return
	}
	step, err := parseDuration(r.FormValue("step"))
	if err != nil {
		write(w, err, errorBadData, nil)
		return
	}
	if step <= 0 {
		write(w, errors.New("zero or negative query resolution step widths are not accepted. Try a positive integer"), errorBadData, nil)
		return
	}
	if end.Sub(start)/step > 11000 {
		write(w, errors.New("exceeded maximum resolution of 11,000 points per timeseries. Try decreasing the query resolution (?step=XX)"), errorBadData, nil)
		return
	}
	stepSec := int64(step / time.Second)
	if stepSec < 1 {
		stepSec = 1
	}
	series, err := c.queryRange(r.Context(), r.FormValue("query"), start.Unix(), end.Unix(), stepSec)
	if err != nil {
		write(w, err, errorExec, nil)
		return
	}
	write(w, nil, errorNone, map[string]interface{}{"resultType": "matrix", "result": matrix(series)})
}

func parseMatchers(r *http.Request) ([][]*labels.Matcher, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("error parsing form values: %w", err)
	}
	sets, err := parser.ParseMetricSelectors(r.Form["match[]"])
	if err != nil {
		return nil, fmt.Errorf("invalid matchers: %s", r.Form["match[]"])
	}
	return sets, nil
}

func (c *Client) LabelValues(r *http.Request, w http.ResponseWriter, labelName string) {
	if !promModel.LabelNameRE.MatchString(labelName) {
		write(w, fmt.Errorf("invalid label name: %q", labelName), errorBadData, nil)
		return
	}
	sets, err := parseMatchers(r)
	if err != nil {
		write(w, err, errorBadData, nil)
		return
	}
	where, args := matcherSetsSQL(sets)
	column := "metric_name"
	if labelName != labels.MetricName {
		column = "tags[@label]"
		args = append(args, chgo.Named("label", labelName))
	}
	args = append(args, chgo.Named("since", time.Now().Add(-since).Unix()))
	rows, err := c.c.Query(ch.TimeSeriesContext(r.Context()),
		`SELECT DISTINCT `+column+` AS v FROM timeSeriesTags(currentDatabase(), '`+metricsTable+`')
		 WHERE max_time >= toDateTime(@since) AND v != '' AND (`+where+`)`, args...)
	if err != nil {
		write(w, err, errorExec, nil)
		return
	}
	defer rows.Close()
	vals := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			write(w, err, errorExec, nil)
			return
		}
		vals = append(vals, v)
	}
	if err := rows.Err(); err != nil {
		write(w, err, errorExec, nil)
		return
	}
	slices.Sort(vals)
	write(w, nil, errorNone, vals)
}

func (c *Client) Series(r *http.Request, w http.ResponseWriter) {
	sets, err := parseMatchers(r)
	if err != nil {
		write(w, err, errorBadData, nil)
		return
	}
	if len(sets) == 0 {
		write(w, errors.New("no match[] parameter provided"), errorBadData, nil)
		return
	}
	where, args := matcherSetsSQL(sets)
	args = append(args, chgo.Named("since", time.Now().Add(-since).Unix()))
	rows, err := c.c.Query(ch.TimeSeriesContext(r.Context()),
		`SELECT DISTINCT metric_name, tags FROM timeSeriesTags(currentDatabase(), '`+metricsTable+`')
		 WHERE max_time >= toDateTime(@since) AND (`+where+`)`, args...)
	if err != nil {
		write(w, err, errorExec, nil)
		return
	}
	defer rows.Close()
	res := []map[string]string{}
	for rows.Next() {
		var name string
		var tags map[string]string
		if err := rows.Scan(&name, &tags); err != nil {
			write(w, err, errorExec, nil)
			return
		}
		m := make(map[string]string, len(tags)+1)
		for k, v := range tags {
			m[k] = v
		}
		m[labels.MetricName] = name
		res = append(res, m)
	}
	if err := rows.Err(); err != nil {
		write(w, err, errorExec, nil)
		return
	}
	write(w, nil, errorNone, res)
}

type metricMetadata struct {
	Type string `json:"type"`
	Unit string `json:"unit"`
	Help string `json:"help"`
}

func (c *Client) MetricMetadata(r *http.Request, w http.ResponseWriter) {
	query := `SELECT DISTINCT metric_family_name, lower(type), help, unit FROM timeSeriesMetricFamilies(currentDatabase(), '` + metricsTable + `')`
	var args []any
	if m := r.FormValue("metric"); m != "" {
		query += " WHERE metric_family_name = @metric"
		args = append(args, chgo.Named("metric", m))
	}
	rows, err := c.c.Query(ch.TimeSeriesContext(r.Context()), query, args...)
	if err != nil {
		write(w, err, errorInternal, nil)
		return
	}
	defer rows.Close()
	res := map[string][]metricMetadata{}
	for rows.Next() {
		var name string
		var md metricMetadata
		if err := rows.Scan(&name, &md.Type, &md.Help, &md.Unit); err != nil {
			write(w, err, errorInternal, nil)
			return
		}
		res[name] = []metricMetadata{md}
	}
	if err := rows.Err(); err != nil {
		write(w, err, errorInternal, nil)
		return
	}
	write(w, nil, errorNone, res)
}
