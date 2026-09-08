package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type chClient struct {
	baseURL  string
	database string
	user     string
	password string
	http     *http.Client
}

func (c *chClient) withDatabase(database string) *chClient {
	cp := *c
	cp.database = database
	return &cp
}

func clickHouseQueryURL(baseURL, database, query string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid ClickHouse HTTP URL %q", baseURL)
	}
	q := u.Query()
	if database != "" {
		q.Set("database", database)
	}
	if query != "" {
		q.Set("query", query)
	}
	u.RawQuery = q.Encode()
	return u, nil
}

func encodeJSONEachRow(r LogRecord) ([]byte, error) {
	row := map[string]any{
		"Timestamp":          r.Timestamp.UTC().Format("2006-01-02 15:04:05.000000000"),
		"TraceId":            r.TraceId,
		"SpanId":             r.SpanId,
		"TraceFlags":         r.TraceFlags,
		"SeverityText":       r.SeverityText,
		"SeverityNumber":     r.SeverityNumber,
		"ServiceName":        r.ServiceName,
		"Body":               r.Body,
		"ResourceAttributes": r.ResourceAttributes,
		"LogAttributes":      r.LogAttributes,
	}
	return json.Marshal(row)
}

func (c *chClient) do(ctx context.Context, query string, body io.Reader) ([]byte, error) {
	u, err := clickHouseQueryURL(c.baseURL, c.database, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.password != "" {
		req.Header.Set("X-ClickHouse-Key", c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *chClient) Exec(ctx context.Context, query string) error {
	_, err := c.do(ctx, query, nil)
	return err
}

func (c *chClient) Query(ctx context.Context, query string) (string, error) {
	data, err := c.do(ctx, query, nil)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *chClient) InsertJSONEachRow(ctx context.Context, query string, payload []byte) error {
	_, err := c.do(ctx, query, bytes.NewReader(payload))
	return err
}

func (c *chClient) Ping(ctx context.Context) error {
	u, err := url.Parse(strings.TrimRight(c.baseURL, "/") + "/ping")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clickhouse ping %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func newCHClient(baseURL, database, user, password string) *chClient {
	return &chClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		database: database,
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 5 * time.Minute},
	}
}
