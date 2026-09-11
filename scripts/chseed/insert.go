package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const seedBatchSize = 10_000

const insertLogsJSONEachRow = `INSERT INTO otel_logs (
	Timestamp, TraceId, SpanId, TraceFlags,
	SeverityText, SeverityNumber, ServiceName, Body,
	ResourceAttributes, LogAttributes
) FORMAT JSONEachRow`

const insertTracesJSONEachRow = `INSERT INTO otel_traces (
	Timestamp, TraceId, SpanId, ParentSpanId, TraceState,
	SpanName, SpanKind, ServiceName,
	ResourceAttributes, SpanAttributes, Duration, StatusCode, StatusMessage
) FORMAT JSONEachRow`

func Seed(ctx context.Context, ch *chClient, cfg Config, now time.Time) error {
	if err := prepareLogs(ctx, ch, cfg); err != nil {
		return err
	}
	if err := prepareTraces(ctx, ch, cfg); err != nil {
		return err
	}
	log.Printf("inserting %d logs and %d traces over %d days (%d/day) in parallel", cfg.Count, cfg.Count, cfg.Days, cfg.PerDay)
	return insertLogsAndTraces(ctx, ch, cfg, now)
}

func prepareLogs(ctx context.Context, ch *chClient, cfg Config) error {
	ttl := TTLSeconds(cfg.Days)
	log.Printf("extending logs TTL to %d seconds (%d days)", ttl, ttl/86400)
	for _, q := range logTTLQueries(ttl) {
		if err := ch.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	log.Printf("removing previous chseed logs")
	for _, q := range logCleanupQueries() {
		if err := ch.Exec(ctx, q); err != nil {
			return fmt.Errorf("delete previous seed logs: %w", err)
		}
	}
	return nil
}

func prepareTraces(ctx context.Context, ch *chClient, cfg Config) error {
	ttl := TTLSeconds(cfg.Days)
	log.Printf("extending traces TTL to %d seconds (%d days)", ttl, ttl/86400)
	for _, q := range traceTTLQueries(ttl) {
		if err := ch.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	log.Printf("removing previous chseed traces")
	for _, q := range traceCleanupQueries() {
		if err := ch.Exec(ctx, q); err != nil {
			return fmt.Errorf("delete previous seed traces: %w", err)
		}
	}
	return nil
}

func insertLogsAndTraces(ctx context.Context, ch *chClient, cfg Config, now time.Time) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errCh <- insertBatched(ctx, ch, cfg, "logs", insertLogsJSONEachRow, func(i int) ([]byte, error) {
			return encodeJSONEachRow(Record(cfg, now, i))
		})
	}()
	go func() {
		defer wg.Done()
		errCh <- insertBatched(ctx, ch, cfg, "traces", insertTracesJSONEachRow, func(i int) ([]byte, error) {
			return encodeTraceJSONEachRow(TraceRecordFor(cfg, now, i))
		})
	}()
	go func() {
		wg.Wait()
		close(errCh)
	}()

	var first error
	for err := range errCh {
		if err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

func insertProgress(kind string, done, total int, elapsed time.Duration) string {
	return fmt.Sprintf("inserted %d / %d %s (%s)", done, total, kind, elapsed.Truncate(time.Millisecond))
}

func insertBatched(ctx context.Context, ch *chClient, cfg Config, kind, insertSQL string, encode func(int) ([]byte, error)) error {
	var buf bytes.Buffer
	buf.Grow(seedBatchSize * 512)
	started := time.Now()
	flush := func(lastIndex int) error {
		if buf.Len() == 0 {
			return nil
		}
		if err := ch.InsertJSONEachRow(ctx, insertSQL, buf.Bytes()); err != nil {
			return fmt.Errorf("send %s batch at row %d: %w", kind, lastIndex, err)
		}
		buf.Reset()
		log.Print(insertProgress(kind, lastIndex+1, cfg.Count, time.Since(started)))
		return nil
	}
	for i := 0; i < cfg.Count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := encode(i)
		if err != nil {
			return fmt.Errorf("encode %s row %d: %w", kind, i, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		if (i+1)%seedBatchSize == 0 || i+1 == cfg.Count {
			if err := flush(i); err != nil {
				return err
			}
		}
	}
	return nil
}

func logTTLQueries(ttl uint64) []string {
	return []string{
		ModifyTTLQuery("otel_logs", "Timestamp", ttl),
		ModifyTTLQuery("otel_logs_service_name_severity_text", "LastSeen", ttl),
	}
}

func traceTTLQueries(ttl uint64) []string {
	return []string{
		ModifyTTLQuery("otel_traces", "Timestamp", ttl),
		ModifyTTLQuery("otel_traces_histogram", "Timestamp", ttl),
		ModifyTTLQuery("otel_traces_service_name", "LastSeen", ttl),
		ModifyTTLQuery("otel_traces_trace_id_ts", "Start", ttl),
	}
}

func logCleanupQueries() []string {
	return []string{
		"DELETE FROM otel_logs WHERE LogAttributes['chseed'] = '1'",
	}
}

func traceCleanupQueries() []string {
	return []string{
		"DELETE FROM otel_traces WHERE SpanAttributes['chseed'] = '1'",
		"DELETE FROM otel_traces_histogram WHERE NetPeerName = '" + seedHistPeer + "'",
		"DELETE FROM otel_traces_trace_id_ts WHERE startsWith(TraceId, '" + seedTraceIDPrefix + "')",
	}
}

func listLogsDatabases(ctx context.Context, ch *chClient) ([]string, error) {
	out, err := ch.Query(ctx, "SELECT database FROM system.tables WHERE name = 'otel_logs'")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}
