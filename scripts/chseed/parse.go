package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Days   int
	PerDay int
	Count  int
}

func ParseArgs(args []string) (Config, error) {
	if len(args) != 2 {
		return Config{}, fmt.Errorf("usage: make seed <days> <thousands-of-logs-per-day>  (example: make seed 30 1000)")
	}
	days, err := strconv.Atoi(args[0])
	if err != nil || days <= 0 {
		return Config{}, fmt.Errorf("days must be a positive integer, got %q", args[0])
	}
	thousands, err := strconv.Atoi(args[1])
	if err != nil || thousands <= 0 {
		return Config{}, fmt.Errorf("log volume must be a positive integer in thousands per day (1000 = 1_000_000 logs/day), got %q", args[1])
	}
	perDay := thousands * 1000
	return Config{Days: days, PerDay: perDay, Count: days * perDay}, nil
}

func ProjectDatabase(projectID string) string {
	return "coroot_" + projectID
}

func ClickHouseHTTPAddr(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid ClickHouse HTTP URL %q", raw)
	}
	return u.Host, nil
}

func TTLSeconds(days int) uint64 {
	ttlDays := days + 1
	if ttlDays < 7 {
		ttlDays = 7
	}
	return uint64(ttlDays) * uint64((24*time.Hour)/time.Second)
}

func ModifyTTLQuery(table, column string, ttlSeconds uint64) string {
	return fmt.Sprintf("ALTER TABLE %s MODIFY TTL toDateTime(%s) + toIntervalSecond(%d)", table, column, ttlSeconds)
}

func ParseUserProjects(body []byte) ([]string, error) {
	var user struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(user.Projects))
	for _, p := range user.Projects {
		if p.ID != "" {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("Coroot /api/user returned no projects")
	}
	return ids, nil
}

func PickLogsDatabase(names []string) (string, error) {
	var corootDBs []string
	var others []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || n == "system" || strings.EqualFold(n, "INFORMATION_SCHEMA") {
			continue
		}
		if strings.HasPrefix(n, "coroot_") && len(n) > len("coroot_") {
			corootDBs = append(corootDBs, n)
			continue
		}
		others = append(others, n)
	}
	if len(corootDBs) > 0 {
		sort.Strings(corootDBs)
		return corootDBs[0], nil
	}
	for _, n := range others {
		if n == "default" {
			return n, nil
		}
	}
	if len(others) > 0 {
		sort.Strings(others)
		return others[0], nil
	}
	return "", fmt.Errorf("no otel_logs table found (is make-dev running?)")
}
