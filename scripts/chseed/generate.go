package main

import (
	"fmt"
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

	seedTraceIDPrefix = "c5eed"
	seedHistPeer      = "chseed"
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

type TraceRecord struct {
	Timestamp          time.Time
	TraceId            string
	SpanId             string
	ParentSpanId       string
	SpanName           string
	SpanKind           string
	ServiceName        string
	ResourceAttributes map[string]string
	SpanAttributes     map[string]string
	Duration           int64
	StatusCode         string
	StatusMessage      string
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
	ts := seedTimestamp(cfg, now, i)
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

func TraceRecordFor(cfg Config, now time.Time, i int) TraceRecord {
	ts := seedTimestamp(cfg, now, i)
	tpl := templateFor(i)
	path := tracePath(tpl)
	status := "STATUS_CODE_UNSET"
	dur := int64(5_000_000)
	if tpl.severityNumber == severityError {
		status = "STATUS_CODE_ERROR"
		dur = 12_000_000
	} else if path == "/api/slow" {
		dur = 250_000_000
	}
	return TraceRecord{
		Timestamp:    ts,
		TraceId:      seedTraceID(i),
		SpanId:       seedSpanID(i),
		ParentSpanId: "",
		SpanName:     "GET " + path,
		SpanKind:     "SPAN_KIND_SERVER",
		ServiceName:  tpl.deploy,
		ResourceAttributes: map[string]string{
			"service.name":        tpl.deploy,
			"k8s.namespace.name":  namespace,
			"k8s.pod.name":        podName(tpl.deploy, i),
			"k8s.deployment.name": tpl.deploy,
			"k8s.container.name":  tpl.container,
		},
		SpanAttributes: map[string]string{
			"chseed":        "1",
			"http.method":   "GET",
			"http.route":    path,
			"http.target":   path,
			"net.peer.name": seedHistPeer,
		},
		Duration:      dur,
		StatusCode:    status,
		StatusMessage: "",
	}
}

func seedTimestamp(cfg Config, now time.Time, i int) time.Time {
	if i < 0 || i >= cfg.Count {
		panic("chseed: record index out of range")
	}
	from := now.Add(-time.Duration(cfg.Days) * 24 * time.Hour)
	if cfg.Count <= 1 {
		return now
	}
	switch i {
	case 0:
		return from
	case cfg.Count - 1:
		return now
	default:
		// Divide first: i*span overflows int64 around 15k rows for a 7-day window.
		span := now.Sub(from)
		return from.Add(span / time.Duration(cfg.Count-1) * time.Duration(i))
	}
}

func seedTraceID(i int) string {
	return fmt.Sprintf("%s%027x", seedTraceIDPrefix, i)
}

func seedSpanID(i int) string {
	return fmt.Sprintf("%016x", uint64(i)+1)
}

func tracePath(tpl logTemplate) string {
	if p := tpl.attrs["path"]; p != "" {
		return p
	}
	if tpl.severityNumber == severityError {
		return "/chain"
	}
	return "/"
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
