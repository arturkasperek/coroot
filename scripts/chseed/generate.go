package main

import (
	"hash/fnv"
	"strconv"
	"time"
)

const (
	namespace     = "coroot-dev"
	expressDeploy = "express-demo"
	nextjsDeploy  = "nextjs-demo"

	severityInfo  int32 = 9
	severityError int32 = 17
)

type LogRecord struct {
	Timestamp          time.Time
	TraceId            string
	SpanId             string
	TraceFlags         uint32
	SeverityText       string
	SeverityNumber     int32
	ServiceName        string
	Body               string
	ResourceAttributes map[string]string
	LogAttributes      map[string]string
}

type logTemplate struct {
	deploy         string
	container      string
	severityText   string
	severityNumber int32
	body           string
	attrs          map[string]string
}

func Record(cfg Config, now time.Time, i int) LogRecord {
	if i < 0 || i >= cfg.Count {
		panic("chseed: record index out of range")
	}
	from := now.Add(-time.Duration(cfg.Days) * 24 * time.Hour)
	ts := now
	if cfg.Count > 1 {
		switch i {
		case 0:
			ts = from
		case cfg.Count - 1:
			ts = now
		default:
			// Divide first: i*span overflows int64 around 15k rows for a 7-day window.
			span := now.Sub(from)
			ts = from.Add(span / time.Duration(cfg.Count-1) * time.Duration(i))
		}
	}

	tpl := templateFor(i)
	service := serviceName(tpl.deploy)
	pod := podName(tpl.deploy, i)
	attrs := map[string]string{
		"chseed":       "1",
		"pattern.hash": patternHash(tpl.body),
	}
	for k, v := range tpl.attrs {
		attrs[k] = v
	}
	return LogRecord{
		Timestamp:      ts,
		SeverityText:   tpl.severityText,
		SeverityNumber: tpl.severityNumber,
		ServiceName:    service,
		Body:           tpl.body,
		ResourceAttributes: map[string]string{
			"service.name": service,
			"container.id": "/k8s/" + namespace + "/" + pod + "/" + tpl.container,
		},
		LogAttributes: attrs,
	}
}

func serviceName(deploy string) string {
	return "/k8s/" + namespace + "/" + deploy
}

func podName(deploy string, i int) string {
	// ReplicaSet hex + Kubernetes-style 5-char suffix (no vowels).
	replicas := []string{"a1b2c3d4e", "7d9f8c6b4", "c0ffee123"}
	suffixes := []string{"xk2nq", "b4d7k", "m2p5w"}
	r := replicas[i%len(replicas)]
	s := suffixes[(i/len(replicas))%len(suffixes)]
	return deploy + "-" + r + "-" + s
}

func templateFor(i int) logTemplate {
	n := i / 2
	if i%2 == 0 {
		switch n % 20 {
		case 0:
			return logTemplate{expressDeploy, expressDeploy, "ERROR", severityError, "express simulated failure", map[string]string{"path": "/api/error"}}
		case 1:
			return logTemplate{expressDeploy, expressDeploy, "INFO", severityInfo, "express slow path", map[string]string{"path": "/api/slow"}}
		default:
			return logTemplate{expressDeploy, expressDeploy, "INFO", severityInfo, "express hello", map[string]string{"path": "/api/hello"}}
		}
	}
	if n%20 == 0 {
		return logTemplate{nextjsDeploy, nextjsDeploy, "ERROR", severityError, "nextjs express fetch failed", map[string]string{"message": "connect ECONNREFUSED"}}
	}
	return logTemplate{nextjsDeploy, nextjsDeploy, "INFO", severityInfo, "nextjs fetching express", map[string]string{"url": "http://express-demo:3000/api/hello"}}
}

func patternHash(body string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(body))
	return strconv.FormatUint(h.Sum64(), 16)
}
