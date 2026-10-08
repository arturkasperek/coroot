package api

import (
	"fmt"

	"github.com/coroot/coroot/api/views/overview"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/utils"
	"github.com/coroot/coroot/world"
)

type DataWithContext struct {
	Context Context `json:"context"`
	Data    any     `json:"data"`
}

type Context struct {
	Status         Status                            `json:"status"`
	Search         Search                            `json:"search"`
	Incidents      map[model.ApplicationCategory]int `json:"incidents"`
	Alerts         map[string]int                    `json:"alerts"`
	Fluxcd         *GitOpsStatus                     `json:"fluxcd"`
	Argocd         *GitOpsStatus                     `json:"argocd"`
	License        *License                          `json:"license,omitempty"`
	Multicluster   bool                              `json:"multicluster"`
	MemberProjects []string                          `json:"member_projects,omitempty"`
}

type GitOpsStatus struct {
	Issues int `json:"issues"`
}

type Status struct {
	Status           model.Status      `json:"status"`
	Error            string            `json:"error"`
	Metrics          Metrics           `json:"metrics"`
	NodeAgent        NodeAgent         `json:"node_agent"`
	KubeStateMetrics *KubeStateMetrics `json:"kube_state_metrics"`
}

type Metrics struct {
	Status  model.Status `json:"status"`
	Message string       `json:"message"`
	Error   string       `json:"error"`
	Action  string       `json:"action"`
}

type NodeAgent struct {
	Status model.Status `json:"status"`
	Nodes  int          `json:"nodes"`
}

type KubeStateMetrics struct {
	Status       model.Status `json:"status"`
	Applications int          `json:"applications"`
}

type Search struct {
	Applications []Application `json:"applications"`
	Nodes        []Node        `json:"nodes"`
}

type Application struct {
	Id model.ApplicationId `json:"id"`
}

type Node struct {
	Name string `json:"name"`
}

type License struct {
	Invalid bool   `json:"invalid"`
	Message string `json:"message"`
}

type LicenseManager interface {
	CheckLicense() *License
}

func (api *Api) WithContext(p *db.Project, cacheStatus *world.Status, w *model.World, data any) DataWithContext {
	if p == nil {
		return DataWithContext{}
	}
	alerts, _ := api.db.GetFiringAlertCountsBySeverity(p.Id)
	if alerts == nil {
		alerts = map[string]int{}
	}
	res := DataWithContext{
		Context: Context{
			Status:         renderStatus(p, cacheStatus, w),
			Search:         renderSearch(w),
			Incidents:      renderIncidents(w),
			Alerts:         alerts,
			Fluxcd:         gitOpsStatus(w, w != nil && w.Flux != nil, overview.CountFluxIssues),
			Argocd:         gitOpsStatus(w, w != nil && w.ArgoCD != nil, overview.CountArgoCDIssues),
			Multicluster:   p.Multicluster(),
			MemberProjects: p.Settings.MemberProjects,
		},
		Data: data,
	}
	if lm := api.licenseMgr; lm != nil {
		if l := lm.CheckLicense(); l != nil {
			res.Context.License = l
			if l.Invalid {
				res.Data = nil
			}
		}
	}
	return res
}

func renderIncidents(w *model.World) map[model.ApplicationCategory]int {
	res := map[model.ApplicationCategory]int{}
	if w == nil {
		return res
	}
	for _, app := range w.Applications {
		if len(app.Incidents) == 0 {
			continue
		}
		if last := app.Incidents[len(app.Incidents)-1]; !last.Resolved() {
			res[app.Category]++
		}
	}
	return res
}

func gitOpsStatus(w *model.World, present bool, count func(*model.World) int) *GitOpsStatus {
	if !present {
		return nil
	}
	return &GitOpsStatus{Issues: count(w)}
}

func renderStatus(p *db.Project, cacheStatus *world.Status, w *model.World) Status {
	res := Status{
		Status: model.OK,
	}

	if p == nil {
		res.Status = model.WARNING
		res.Error = "Project not found"
		return res
	}

	res.Metrics.Status = model.OK
	res.Metrics.Message = "ok"
	switch {
	case cacheStatus != nil && cacheStatus.Error != "":
		res.Metrics.Status = model.WARNING
		res.Metrics.Message = "An error has occurred while evaluating metrics:"
		res.Metrics.Error = cacheStatus.Error
	case cacheStatus != nil && cacheStatus.LagMax > 5*world.Step:
		lag := utils.FormatDuration(cacheStatus.LagAvg, 1)
		res.Metrics.Status = model.WARNING
		res.Metrics.Message = fmt.Sprintf("The metrics lag is %s, likely due to a restart or upgrade. Synchronization is in progress.", lag)
		res.Metrics.Action = "wait"
	}

	if res.Metrics.Status >= model.WARNING {
		res.Status = model.WARNING
	}

	if w == nil {
		return res
	}

	is := w.IntegrationStatus
	if !is.NodeAgent.Installed {
		res.NodeAgent.Status = model.WARNING
		res.Status = model.WARNING
	} else {
		res.NodeAgent.Status = model.OK
		res.NodeAgent.Nodes = len(w.Nodes)
	}

	if is.KubeStateMetrics.Required {
		res.KubeStateMetrics = &KubeStateMetrics{}
		if is.KubeStateMetrics.Installed {
			res.KubeStateMetrics.Status = model.OK
			res.KubeStateMetrics.Applications = len(w.Applications) // TODO: count k8s apps only,
		} else {
			res.KubeStateMetrics.Status = model.WARNING
			res.Status = model.WARNING
		}
	}

	return res
}

func renderSearch(w *model.World) Search {
	search := Search{}
	if w == nil {
		return search
	}
	for _, app := range w.Applications {
		search.Applications = append(search.Applications, Application{
			Id: app.Id,
		})
	}
	for _, node := range w.Nodes {
		search.Nodes = append(search.Nodes, Node{
			Name: node.GetName(),
		})
	}
	return search
}
