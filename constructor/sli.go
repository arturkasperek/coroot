package constructor

import (
	"sort"
	"strconv"
	"strings"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

func (c *Constructor) loadSLIs(w *model.World, metrics map[string][]*model.MetricValues, project *db.Project) {
	slis := loadSLIValues(w, metrics, project, sliSuffix)
	raw := loadSLIValues(w, metrics, project, sliRawSuffix) // empty without OptionLoadRawSLIs

	for _, app := range w.Applications {
		availabilityCfg, _ := w.CheckConfigs.GetAvailability(app.Id)
		if availability, ok := slis.availability(app.Id, availabilityCfg.Custom); ok {
			availabilityRaw, _ := raw.availability(app.Id, availabilityCfg.Custom)
			app.AvailabilitySLIs = append(app.AvailabilitySLIs, &model.AvailabilitySLI{
				Config:        availabilityCfg,
				TotalRequests: availability.total, TotalRequestsRaw: availabilityRaw.total,
				FailedRequests: availability.failed, FailedRequestsRaw: availabilityRaw.failed,
			})
		}

		latencyCfg, _ := w.CheckConfigs.GetLatency(app.Id, app.Category)
		if histogram, ok := slis.latency(app.Id, latencyCfg.Custom); ok {
			histogramRaw, _ := raw.latency(app.Id, latencyCfg.Custom)
			app.LatencySLIs = append(app.LatencySLIs, &model.LatencySLI{
				Config:    latencyCfg,
				Histogram: histogram, HistogramRaw: histogramRaw,
			})
		}
	}
}

// sliValues are the SLIs of the applications read at one step: the custom
// ones, those of the inbound requests (from the server side) and the built-in
// ones (from the clients' side).
type sliValues struct {
	inboundAvailability, builtinAvailability, customAvailability map[model.ApplicationId]availabilitySlis
	inboundLatency, builtinLatency, customLatency                map[model.ApplicationId][]model.HistogramBucket
}

// loadSLIValues reads the SLI queries whose keys end with suffix.
func loadSLIValues(w *model.World, metrics map[string][]*model.MetricValues, project *db.Project, suffix string) sliValues {
	values := sliValues{
		inboundAvailability: inboundAvailability(w, metrics[qRecordingRuleApplicationL7InboundRequests+suffix], project),
		inboundLatency:      inboundLatency(w, metrics[qRecordingRuleApplicationL7InboundHistogram+suffix], project),
		builtinAvailability: builtinAvailability(w, metrics[qRecordingRuleApplicationL7Requests+suffix], project),
		builtinLatency:      builtinLatency(w, metrics[qRecordingRuleApplicationL7Histogram+suffix], project),
		customAvailability:  map[model.ApplicationId]availabilitySlis{},
		customLatency:       map[model.ApplicationId][]model.HistogramBucket{},
	}
	loadCustomSLIs(metrics, suffix, values.customAvailability, values.customLatency, project)
	return values
}

// availability is the availability SLI of an application: the custom one when
// configured, else that of the inbound requests, else the built-in one; false
// when the application has none.
func (values sliValues) availability(appId model.ApplicationId, custom bool) (availabilitySlis, bool) {
	if custom {
		return values.customAvailability[appId], true
	}
	availability, ok := values.inboundAvailability[appId]
	if !ok || availability.total.IsEmpty() {
		availability = values.builtinAvailability[appId]
	}
	return availability, !availability.total.IsEmpty()
}

// latency is the latency histogram of an application, chosen as availability.
func (values sliValues) latency(appId model.ApplicationId, custom bool) ([]model.HistogramBucket, bool) {
	if custom {
		return values.customLatency[appId], true
	}
	histogram := values.inboundLatency[appId]
	if len(histogram) == 0 {
		histogram = values.builtinLatency[appId]
	}
	return histogram, len(histogram) > 0
}

func inboundAvailability(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId]availabilitySlis {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		if w.GetApplication(appId) == nil {
			continue
		}
		status := mv.Labels["status"]
		if byApp[appId] == nil {
			byApp[appId] = map[string]*timeseries.TimeSeries{}
		}
		byApp[appId][status] = merge(byApp[appId][status], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId]availabilitySlis{}
	for appId, byStatus := range byApp {
		total := timeseries.NewAggregate(timeseries.NanSum)
		failed := timeseries.NewAggregate(timeseries.NanSum)
		for status, ts := range byStatus {
			total.Add(ts)
			if model.IsRequestStatusFailed(status) {
				failed.Add(ts)
			}
		}
		res[appId] = availabilitySlis{total: total.Get(), failed: failed.Get()}
	}
	return res
}

func inboundLatency(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId][]model.HistogramBucket {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		if w.GetApplication(appId) == nil {
			continue
		}
		buckets := byApp[appId]
		if buckets == nil {
			buckets = map[string]*timeseries.TimeSeries{}
			byApp[appId] = buckets
		}
		leStr := mv.Labels["le"]
		buckets[leStr] = merge(buckets[leStr], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId][]model.HistogramBucket{}
	for appId, buckets := range byApp {
		res[appId] = histogramBuckets(buckets)
	}
	return res
}

func loadCustomSLIs(metrics map[string][]*model.MetricValues, suffix string,
	availability map[model.ApplicationId]availabilitySlis,
	latency map[model.ApplicationId][]model.HistogramBucket,
	project *db.Project,
) {
	for queryName, values := range metrics {
		if len(values) == 0 || !strings.HasPrefix(queryName, qApplicationCustomSLI) {
			continue
		}
		parts := strings.Split(queryName, "/")
		if len(parts) != 3 {
			continue
		}
		appId, _ := model.NewApplicationIdFromString(parts[1], project.ClusterId())
		switch parts[2] {
		case "total_requests" + suffix:
			a := availability[appId]
			a.total = values[0].Values
			availability[appId] = a
		case "failed_requests" + suffix:
			a := availability[appId]
			a.failed = values[0].Values
			availability[appId] = a
		case "requests_histogram" + suffix:
			buckets := map[string]*timeseries.TimeSeries{}
			for _, mv := range values {
				leStr := mv.Labels["le"]
				buckets[leStr] = mv.Values
			}
			latency[appId] = histogramBuckets(buckets)
		}
	}
}

type availabilitySlis struct {
	total  *timeseries.TimeSeries
	failed *timeseries.TimeSeries
}

func builtinAvailability(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId]availabilitySlis {
	if len(values) == 0 {
		return nil
	}
	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		dstId, err := model.NewApplicationIdFromString(mv.Labels["dest"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		srcId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		status := mv.Labels["status"]
		src := w.GetApplication(srcId)
		dst := w.GetApplication(dstId)
		if src == nil || dst == nil {
			continue
		}
		if !dst.Category.Auxiliary() && src.Category.Auxiliary() {
			continue
		}
		if byApp[dstId] == nil {
			byApp[dstId] = map[string]*timeseries.TimeSeries{}
		}
		byApp[dstId][status] = merge(byApp[dstId][status], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId]availabilitySlis{}
	for appId, byStatus := range byApp {
		total := timeseries.NewAggregate(timeseries.NanSum)
		failed := timeseries.NewAggregate(timeseries.NanSum)
		for status, ts := range byStatus {
			total.Add(ts)
			if model.IsRequestStatusFailed(status) {
				failed.Add(ts)
			}
		}
		res[appId] = availabilitySlis{total: total.Get(), failed: failed.Get()}
	}
	return res
}

func builtinLatency(w *model.World, values []*model.MetricValues, project *db.Project) map[model.ApplicationId][]model.HistogramBucket {
	if len(values) == 0 {
		return nil
	}

	byApp := map[model.ApplicationId]map[string]*timeseries.TimeSeries{}
	for _, mv := range values {
		appId, err := model.NewApplicationIdFromString(mv.Labels["app"], project.ClusterId())
		if err != nil {
			klog.Warningln(err)
			continue
		}
		app := w.GetApplication(appId)
		if app == nil {
			continue
		}
		buckets := byApp[appId]
		if buckets == nil {
			buckets = map[string]*timeseries.TimeSeries{}
			byApp[appId] = buckets
		}
		leStr := mv.Labels["le"]
		buckets[leStr] = merge(buckets[leStr], mv.Values, timeseries.NanSum)
	}
	res := map[model.ApplicationId][]model.HistogramBucket{}
	for appId, buckets := range byApp {
		res[appId] = histogramBuckets(buckets)
	}
	return res
}

func histogramBuckets(values map[string]*timeseries.TimeSeries) []model.HistogramBucket {
	buckets := make([]model.HistogramBucket, 0, len(values))
	for leStr, ts := range values {
		le, err := strconv.ParseFloat(leStr, 64)
		if err != nil {
			klog.Warningln(err)
			continue
		}
		buckets = append(buckets, model.HistogramBucket{Le: float32(le), TimeSeries: ts})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Le < buckets[j].Le
	})
	return buckets
}
