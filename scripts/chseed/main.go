package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	log.SetFlags(0)
	cfg, err := ParseArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	chHTTP := envDefault("COROOT_DEV_CLICKHOUSE_HTTP", "http://127.0.0.1:18123")
	corootURL := strings.TrimRight(envDefault("COROOT_URL", "http://127.0.0.1:18080"), "/")
	user := envDefault("COROOT_DEV_CLICKHOUSE_USER", "default")
	password := os.Getenv("COROOT_DEV_CLICKHOUSE_PASSWORD")

	ch := newCHClient(chHTTP, "default", user, password)
	if err = ch.Ping(ctx); err != nil {
		log.Fatalf("clickhouse %s: %v (is Tilt forwarding ClickHouse on 18123?)", chHTTP, err)
	}

	dbName := os.Getenv("COROOT_SEED_DATABASE")
	if dbName == "" {
		dbName, err = resolveDatabase(ctx, ch, corootURL)
		if err != nil {
			log.Fatal(err)
		}
	}
	ch = ch.withDatabase(dbName)

	log.Printf("seeding %s at %s (%d days, %d logs for express-demo + nextjs-demo)", dbName, chHTTP, cfg.Days, cfg.Count)
	if err = Seed(ctx, ch, cfg, time.Now().UTC()); err != nil {
		log.Fatal(err)
	}
	log.Printf("done")
}

func resolveDatabase(ctx context.Context, ch *chClient, corootURL string) (string, error) {
	names, err := listLogsDatabases(ctx, ch)
	if err != nil {
		return "", err
	}
	dbName, err := PickLogsDatabase(names)
	if err != nil {
		if ids, apiErr := fetchProjectIDs(ctx, corootURL); apiErr == nil && len(ids) > 0 {
			return "", fmt.Errorf("%w (Coroot project id %s)", err, ids[0])
		}
		return "", err
	}
	return dbName, nil
}

func fetchProjectIDs(ctx context.Context, corootURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, corootURL+"/api/user", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return ParseUserProjects(body)
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
