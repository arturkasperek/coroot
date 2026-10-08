package api

import (
	"net/http"

	"github.com/coroot/coroot/db"
	"github.com/gorilla/mux"
	"k8s.io/klog"
)

func (api *Api) PrometheusQueryRange(w http.ResponseWriter, r *http.Request, project *db.Project) {
	c, err := api.evaluator.PromQL(project)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	c.QueryRangeHandler(r, w)
}

func (api *Api) PrometheusMetricMetadata(w http.ResponseWriter, r *http.Request, project *db.Project) {
	c, err := api.evaluator.PromQL(project)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	c.MetricMetadata(r, w)
}

func (api *Api) PrometheusSeries(w http.ResponseWriter, r *http.Request, project *db.Project) {
	c, err := api.evaluator.PromQL(project)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	c.Series(r, w)
}

func (api *Api) PrometheusLabelValues(w http.ResponseWriter, r *http.Request, project *db.Project) {
	c, err := api.evaluator.PromQL(project)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	c.LabelValues(r, w, mux.Vars(r)["labelName"])
}
