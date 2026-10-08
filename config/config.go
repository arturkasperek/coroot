package config

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"

	"github.com/coroot/coroot/cloud"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenAddress      string `yaml:"listen_address"`
	HTTPSListenAddress string `yaml:"https_listen_address"`
	HTTPDisabled       bool   `yaml:"http_disabled"`
	UrlBasePath        string `yaml:"url_base_path"`
	DataDir            string `yaml:"data_dir"`

	GRPC GRPC `yaml:"grpc"`
	TLS  *TLS `yaml:"tls"`

	Traces   Traces   `yaml:"traces"`
	Logs     Logs     `yaml:"logs"`
	Profiles Profiles `yaml:"profiles"`
	Metrics  Metrics  `yaml:"metrics"`

	Postgres         *Postgres   `yaml:"postgres"`
	GlobalClickhouse *Clickhouse `yaml:"global_clickhouse"`

	Auth Auth `yaml:"auth"`

	Projects []Project `yaml:"projects"`

	DoNotCheckForDeployments bool `yaml:"do_not_check_for_deployments"`
	DoNotCheckForUpdates     bool `yaml:"do_not_check_for_updates"`
	DisableUsageStatistics   bool `yaml:"disable_usage_statistics"`
	DisableBuiltinAlerts     bool `yaml:"disableBuiltinAlerts"`

	DeveloperMode bool `yaml:"developer_mode"`

	ClickHouseSpaceManager ClickHouseSpaceManager `yaml:"clickhouse_space_manager"`

	CorootCloud *cloud.Settings `yaml:"corootCloud"`

	BootstrapClickhouse *Clickhouse `yaml:"-"`
}

type GRPC struct {
	Disabled      bool   `yaml:"disabled"`
	ListenAddress string `yaml:"listenAddress"`
}

type TLS struct {
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

func (c *TLS) Validate() error {
	if c == nil {
		return nil
	}
	if c.CertFile == "" {
		return fmt.Errorf("certFile is required")
	}
	if c.KeyFile == "" {
		return fmt.Errorf("keyFile is required")
	}
	if _, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile); err != nil {
		return fmt.Errorf("invalid certificate: %w", err)
	}
	return nil
}

type ClickHouseSpaceManager struct {
	Enabled               bool `yaml:"enabled"`
	UsageThresholdPercent int  `yaml:"usage_threshold_percent"`
	MinPartitions         int  `yaml:"min_partitions"`
}

type Traces struct {
	TTL timeseries.Duration `yaml:"ttl"`
}

type Logs struct {
	TTL timeseries.Duration `yaml:"ttl"`
}

type Profiles struct {
	TTL timeseries.Duration `yaml:"ttl"`
}

type Metrics struct {
	TTL timeseries.Duration `yaml:"ttl"`
}

type Postgres struct {
	ConnectionString string `yaml:"connection_string"`
}

type Clickhouse struct {
	Address       string `yaml:"address"`
	User          string `yaml:"user"`
	Password      string `yaml:"password"`
	Database      string `yaml:"database"`
	TlsEnable     bool   `yaml:"tls_enable"`
	TlsSkipVerify bool   `yaml:"tls_skip_verify"`
	TlsCAFile     string `yaml:"tls_ca_file"`
}

func (c *Clickhouse) Validate() error {
	if c == nil {
		return nil
	}
	if c.Address == "" {
		return fmt.Errorf("address is required")
	}
	host, port, err := net.SplitHostPort(c.Address)
	if host == "" || port == "" {
		return fmt.Errorf("invalid address: %s", c.Address)
	}
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	return nil
}

type Auth struct {
	AnonymousRole          string `yaml:"anonymous_role"`
	BootstrapAdminPassword string `yaml:"bootstrap_admin_password"`
}

func NewConfig() *Config {
	cfg := &Config{
		ListenAddress: ":8080",
		UrlBasePath:   "/",
		DataDir:       "./data",

		Traces: Traces{
			TTL: 7 * timeseries.Day,
		},
		Logs: Logs{
			TTL: 7 * timeseries.Day,
		},
		Profiles: Profiles{
			TTL: 7 * timeseries.Day,
		},
		Metrics: Metrics{
			TTL: 7 * timeseries.Day,
		},

		Auth: Auth{
			BootstrapAdminPassword: db.AdminUserDefaultPassword,
		},

		ClickHouseSpaceManager: ClickHouseSpaceManager{
			Enabled:               true,
			UsageThresholdPercent: 70,
			MinPartitions:         1,
		},
	}
	if !cfg.GRPC.Disabled && cfg.GRPC.ListenAddress == "" {
		cfg.GRPC.ListenAddress = ":4317"
	}
	return cfg
}

func Load() (*Config, error) {
	cfg := NewConfig()
	data, err := ReadFromFile()
	if err != nil {
		return nil, err
	}

	if len(data) > 0 {
		if err = yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	cfg.ApplyFlags()

	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func ReadFromFile() ([]byte, error) {
	if *configFile == "" {
		return nil, nil
	}
	f, err := os.Open(*configFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	data = []byte(os.ExpandEnv(string(data)))
	return data, nil
}

func (cfg *Config) Validate() error {
	var err error
	cfg.UrlBasePath, err = url.JoinPath("/", cfg.UrlBasePath, "/")
	if err != nil {
		return fmt.Errorf("invalid url_base_path: %s", cfg.UrlBasePath)
	}

	if cfg.HTTPDisabled && cfg.HTTPSListenAddress == "" {
		return fmt.Errorf("at least one of HTTP or HTTPS listener must be enabled")
	}

	if cfg.HTTPSListenAddress != "" && cfg.TLS == nil {
		return fmt.Errorf("TLS certificate and key are required for HTTPS")
	}

	if cfg.TLS != nil {
		if err = cfg.TLS.Validate(); err != nil {
			return fmt.Errorf("invalid tls settings: %w", err)
		}
	}

	if cfg.CorootCloud != nil {
		if err = cfg.CorootCloud.Validate(); err != nil {
			return fmt.Errorf("invalid corootCloud settings: %w", err)
		}
	}

	for i, p := range cfg.Projects {
		if err = p.Validate(); err != nil {
			return fmt.Errorf("invalid project #%d: %w", i, err)
		}
	}

	if err = cfg.GlobalClickhouse.Validate(); err != nil {
		return fmt.Errorf("invalid global_clickhouse: %w", err)
	}
	if cfg.GlobalClickhouse != nil {
		cfg.BootstrapClickhouse = nil
	}
	if err = cfg.BootstrapClickhouse.Validate(); err != nil {
		return fmt.Errorf("invalid bootstrap_clickhouse: %w", err)
	}

	if cfg.ClickHouseSpaceManager.UsageThresholdPercent < 0 || cfg.ClickHouseSpaceManager.UsageThresholdPercent > 100 {
		return fmt.Errorf("invalid usage_threshold_percent: %d", cfg.ClickHouseSpaceManager.UsageThresholdPercent)
	}

	return nil
}

func (cfg *Config) GetGlobalClickhouse() *db.IntegrationClickhouse {
	clickhouse := cfg.GlobalClickhouse
	if clickhouse == nil {
		return nil
	}
	c := &db.IntegrationClickhouse{
		Global:   true,
		Protocol: "native",
		Addr:     clickhouse.Address,
		Auth: utils.BasicAuth{
			User:     clickhouse.User,
			Password: clickhouse.Password,
		},
		Database:        "",
		InitialDatabase: clickhouse.Database,
		TlsEnable:       clickhouse.TlsEnable,
		TlsSkipVerify:   clickhouse.TlsSkipVerify,
		TlsCAFile:       clickhouse.TlsCAFile,
	}
	if c.Auth.User == "" {
		c.Auth.User = "default"
	}
	if c.InitialDatabase == "" {
		c.InitialDatabase = "default"
	}
	return c
}

func (cfg *Config) GetBootstrapClickhouse() *db.IntegrationClickhouse {
	clickhouse := cfg.BootstrapClickhouse
	if clickhouse == nil {
		return nil
	}
	c := &db.IntegrationClickhouse{
		Protocol: "native",
		Addr:     clickhouse.Address,
		Auth: utils.BasicAuth{
			User:     clickhouse.User,
			Password: clickhouse.Password,
		},
		Database:      clickhouse.Database,
		TlsEnable:     clickhouse.TlsEnable,
		TlsSkipVerify: clickhouse.TlsSkipVerify,
		TlsCAFile:     clickhouse.TlsCAFile,
	}
	if c.Auth.User == "" {
		c.Auth.User = "default"
	}
	if c.Database == "" {
		c.Database = "default"
	}
	return c
}
