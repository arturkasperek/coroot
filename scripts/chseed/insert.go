package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const seedBatchSize = 10_000

const insertJSONEachRow = `INSERT INTO otel_logs (
	Timestamp, TraceId, SpanId, TraceFlags,
	SeverityText, SeverityNumber, ServiceName, Body,
	ResourceAttributes, LogAttributes
) FORMAT JSONEachRow`

func Seed(ctx context.Context, ch *chClient, cfg Config, now time.Time) error {
	ttl := TTLSeconds(cfg.Days)
	log.Printf("extending logs TTL to %d seconds (%d days)", ttl, ttl/86400)
	for _, q := range []string{
		ModifyTTLQuery("otel_logs", "Timestamp", ttl),
		ModifyTTLQuery("otel_logs_service_name_severity_text", "LastSeen", ttl),
	} {
		if err := ch.Exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}

	log.Printf("removing previous chseed rows")
	if err := ch.Exec(ctx, "DELETE FROM otel_logs WHERE LogAttributes['chseed'] = '1'"); err != nil {
		return fmt.Errorf("delete previous seed: %w", err)
	}

	log.Printf("inserting %d logs over %d days (%d/day)", cfg.Count, cfg.Days, cfg.PerDay)
	var buf bytes.Buffer
	buf.Grow(seedBatchSize * 512)
	started := time.Now()
	flush := func(lastIndex int) error {
		if buf.Len() == 0 {
			return nil
		}
		if err := ch.InsertJSONEachRow(ctx, insertJSONEachRow, buf.Bytes()); err != nil {
			return fmt.Errorf("send batch at row %d: %w", lastIndex, err)
		}
		buf.Reset()
		elapsed := time.Since(started).Truncate(time.Millisecond)
		log.Printf("inserted %d / %d (%s)", lastIndex+1, cfg.Count, elapsed)
		return nil
	}
	for i := 0; i < cfg.Count; i++ {
		line, err := encodeJSONEachRow(Record(cfg, now, i))
		if err != nil {
			return fmt.Errorf("encode row %d: %w", i, err)
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
