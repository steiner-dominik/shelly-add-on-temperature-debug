package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// EndpointConfig describes one Shelly device to query.
type EndpointConfig struct {
	Name     string
	BaseURL  string // e.g. http://192.168.1.10
	Host     string // display value, without scheme
	User     string
	Password string
}

// Config is the full application configuration, sourced from env vars only.
type Config struct {
	Port        string
	BasePath    string // "" means served at root
	Token       string
	HistorySize int
	Timeout     time.Duration
	MinInterval time.Duration // device queries are rate-limited to one per this interval
	Endpoints   []EndpointConfig
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	return p // "" for "/"
}

func loadConfig() (*Config, error) {
	histSize, err := strconv.Atoi(getenv("HISTORY_SIZE", "100"))
	if err != nil || histSize < 2 {
		return nil, fmt.Errorf("HISTORY_SIZE must be an integer >= 2")
	}
	timeoutSec, err := strconv.Atoi(getenv("QUERY_TIMEOUT_SECONDS", "5"))
	if err != nil || timeoutSec < 1 {
		return nil, fmt.Errorf("QUERY_TIMEOUT_SECONDS must be an integer >= 1")
	}
	minIntervalSec, err := strconv.Atoi(getenv("QUERY_MIN_INTERVAL_SECONDS", "2"))
	if err != nil || minIntervalSec < 0 {
		return nil, fmt.Errorf("QUERY_MIN_INTERVAL_SECONDS must be an integer >= 0")
	}

	cfg := &Config{
		Port:        getenv("PORT", "8080"),
		BasePath:    normalizeBasePath(getenv("BASE_PATH", "/debug")),
		Token:       os.Getenv("DEBUG_TOKEN"),
		HistorySize: histSize,
		Timeout:     time.Duration(timeoutSec) * time.Second,
		MinInterval: time.Duration(minIntervalSec) * time.Second,
	}

	globalPW := os.Getenv("SHELLY_PASSWORD")
	for i := 1; ; i++ {
		host := strings.TrimSpace(os.Getenv(fmt.Sprintf("SHELLY_%d_HOST", i)))
		if host == "" {
			break
		}
		base := host
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		base = strings.TrimRight(base, "/")
		display := strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://"), "/")
		name := getenv(fmt.Sprintf("SHELLY_%d_NAME", i), display)
		pw := os.Getenv(fmt.Sprintf("SHELLY_%d_PASSWORD", i))
		if pw == "" {
			pw = globalPW
		}
		cfg.Endpoints = append(cfg.Endpoints, EndpointConfig{
			Name:     name,
			BaseURL:  base,
			Host:     display,
			User:     getenv(fmt.Sprintf("SHELLY_%d_USER", i), "admin"),
			Password: pw,
		})
	}
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("no Shelly endpoints configured: set at least SHELLY_1_HOST (numbering must be contiguous, starting at 1)")
	}
	return cfg, nil
}
