package collector

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
	coch "github.com/coroot/coroot/ch"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	promModel "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
	"k8s.io/klog"
)

func (c *Collector) Metrics(w http.ResponseWriter, r *http.Request) {
	project, err := c.getProject(r.Header.Get(ApiKeyHeader))
	if err != nil {
		klog.Errorln(err)
		if errors.Is(err, ErrProjectNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	req, err := parseMetricsRequestBody(r, body)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	c.getMetricsBatch(project).Add(req)
}

func parseMetricsRequestBody(r *http.Request, body []byte) (*prompb.WriteRequest, error) {
	if r.Header.Get("Content-Type") != "application/x-protobuf" {
		return nil, fmt.Errorf("expected application/x-protobuf content-type")
	}
	if r.Header.Get("Content-Encoding") != "snappy" {
		return nil, fmt.Errorf("expected snappy content-encoding")
	}

	decompressed, err := snappy.Decode(nil, body)
	if err != nil {
		return nil, err
	}

	var req prompb.WriteRequest
	if err = proto.Unmarshal(decompressed, &req); err != nil {
		return nil, err
	}

	return &req, nil
}

// Metrics go into one ClickHouse TimeSeries table (see ch.MetricsCluster). The
// engine takes a row per series: its name, its labels and all its samples as an
// array of (time, value) pairs, and keeps the labels once per series itself.
// ch-go has a bare proto.ColTuple (a slice of per-element columns), but no
// typed column for an array of tuples: ColTuple has no Array() and ColAuto
// cannot infer Tuple or Array(Tuple) (checked on v0.62 and v0.74). So the batch
// sends the times and the values as two arrays and ClickHouse zips them.
const (
	insertSamplesSQL = `INSERT INTO metrics (metric_name, tags, samples)
SELECT name, tags, arrayZip(ts, vs) FROM input('name String, tags Map(String, String), ts Array(DateTime64(3)), vs Array(Float64)') FORMAT Native`
	insertMetadataSQL = `INSERT INTO metrics (metric_family, type, unit, help)
SELECT family, type, unit, help FROM input('family String, type String, unit String, help String') FORMAT Native`
)

type MetricsBatch struct {
	limit int
	exec  func(query ch.Query) error

	lock sync.Mutex
	done chan struct{}

	rows int // samples in the batch

	name *chproto.ColStr
	tags *chproto.ColMap[string, string]
	ts   *chproto.ColArr[time.Time]
	vs   *chproto.ColArr[float64]

	family *chproto.ColStr
	typ    *chproto.ColStr
	unit   *chproto.ColStr
	help   *chproto.ColStr
}

func newTimeColumn() *chproto.ColArr[time.Time] {
	return chproto.NewArray[time.Time](new(chproto.ColDateTime64).WithPrecision(chproto.PrecisionMilli))
}

func NewMetricsBatch(limit int, timeout time.Duration, exec func(query ch.Query) error) *MetricsBatch {
	b := &MetricsBatch{
		limit: limit,
		exec:  exec,
		done:  make(chan struct{}),

		name: new(chproto.ColStr),
		tags: chproto.NewMap[string, string](new(chproto.ColStr), new(chproto.ColStr)),
		ts:   newTimeColumn(),
		vs:   chproto.NewArray[float64](new(chproto.ColFloat64)),

		family: new(chproto.ColStr),
		typ:    new(chproto.ColStr),
		unit:   new(chproto.ColStr),
		help:   new(chproto.ColStr),
	}

	go func() {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		for {
			select {
			case <-b.done:
				return
			case <-ticker.C:
				b.lock.Lock()
				b.save()
				b.lock.Unlock()
			}
		}
	}()

	return b
}

func (b *MetricsBatch) Close() {
	b.done <- struct{}{}
	b.lock.Lock()
	defer b.lock.Unlock()
	b.save()
}

func (b *MetricsBatch) Add(req *prompb.WriteRequest) {
	b.lock.Lock()
	defer b.lock.Unlock()

	for _, md := range req.GetMetadata() {
		b.family.Append(md.GetMetricFamilyName())
		b.typ.Append(md.GetType().String())
		b.unit.Append(md.GetUnit())
		b.help.Append(md.GetHelp())
	}

	for _, ts := range req.GetTimeseries() {
		if len(ts.Samples) == 0 {
			continue
		}
		var name string
		tags := make(map[string]string, len(ts.Labels))
		for _, l := range ts.Labels {
			switch {
			case l.Name == promModel.MetricNameLabel:
				name = l.Value
			case l.Value == "": // in PromQL an empty label is an absent label
			default:
				tags[l.Name] = l.Value
			}
		}
		times := make([]time.Time, len(ts.Samples))
		values := make([]float64, len(ts.Samples))
		for i, s := range ts.Samples {
			times[i] = time.UnixMilli(s.Timestamp)
			values[i] = s.Value
		}
		b.name.Append(name)
		b.tags.Append(tags)
		b.ts.Append(times)
		b.vs.Append(values)
		b.rows += len(ts.Samples)
	}

	if b.rows < b.limit {
		return
	}
	b.save()
}

func (b *MetricsBatch) save() {
	if b.rows > 0 {
		input := chproto.Input{
			{Name: "name", Data: b.name},
			{Name: "tags", Data: b.tags},
			{Name: "ts", Data: b.ts},
			{Name: "vs", Data: b.vs},
		}
		if err := b.exec(ch.Query{Body: insertSamplesSQL, Settings: coch.TimeSeriesSettings, Input: input}); err != nil {
			klog.Errorln("failed to insert metrics:", err)
		}
		b.name.Reset()
		b.tags.Reset()
		b.ts.Reset()
		b.vs.Reset()
		b.rows = 0
	}

	if b.family.Rows() > 0 {
		input := chproto.Input{
			{Name: "family", Data: b.family},
			{Name: "type", Data: b.typ},
			{Name: "unit", Data: b.unit},
			{Name: "help", Data: b.help},
		}
		if err := b.exec(ch.Query{Body: insertMetadataSQL, Settings: coch.TimeSeriesSettings, Input: input}); err != nil {
			klog.Errorln("failed to insert metrics metadata:", err)
		}
		b.family.Reset()
		b.typ.Reset()
		b.unit.Reset()
		b.help.Reset()
	}
}
