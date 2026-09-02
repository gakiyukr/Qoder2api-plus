package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("duration must be a string such as \"15s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Listen              string   `json:"listen"`
	CredentialsFile     string   `json:"credentials_file"`
	Transport           string   `json:"transport"`
	ProxyURL            string   `json:"proxy_url,omitempty"`
	SessionHeader       string   `json:"session_header"`
	MaxRequestBytes     int64    `json:"max_request_bytes"`
	ConnectTimeout      Duration `json:"connect_timeout"`
	ResponseTimeout     Duration `json:"response_header_timeout"`
	StreamIdleTimeout   Duration `json:"stream_idle_timeout"`
	TotalRequestTimeout Duration `json:"total_request_timeout"`
	CatalogTTL          Duration `json:"catalog_ttl"`
	CooldownDefault     Duration `json:"cooldown_default"`
	Max5xxRetries       int      `json:"max_5xx_retries"`
	CORSOrigins         []string `json:"cors_origins,omitempty"`
}

func Default() Config {
	return Config{
		Listen:              "127.0.0.1:8080",
		CredentialsFile:     "credentials.json",
		Transport:           "auto",
		SessionHeader:       "X-Session-Key",
		MaxRequestBytes:     8 << 20,
		ConnectTimeout:      Duration{15 * time.Second},
		ResponseTimeout:     Duration{30 * time.Second},
		StreamIdleTimeout:   Duration{90 * time.Second},
		TotalRequestTimeout: Duration{10 * time.Minute},
		CatalogTTL:          Duration{time.Hour},
		CooldownDefault:     Duration{time.Minute},
		Max5xxRetries:       1,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if !filepath.IsAbs(cfg.CredentialsFile) {
		cfg.CredentialsFile = filepath.Join(filepath.Dir(path), cfg.CredentialsFile)
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if c.Transport != "auto" && c.Transport != "bearer" && c.Transport != "cosy" {
		return fmt.Errorf("transport must be auto, bearer, or cosy")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("listen host must be an IP address")
	}
	if !net.ParseIP(host).IsLoopback() && os.Getenv("QODER_PROXY_API_KEY") == "" {
		return errors.New("refusing non-loopback listen address without QODER_PROXY_API_KEY")
	}
	if c.MaxRequestBytes < 1024 || c.MaxRequestBytes > 64<<20 {
		return errors.New("max_request_bytes must be between 1 KiB and 64 MiB")
	}
	if c.ProxyURL != "" {
		u, err := url.Parse(c.ProxyURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("proxy_url must be an http or https URL")
		}
	}
	return nil
}
