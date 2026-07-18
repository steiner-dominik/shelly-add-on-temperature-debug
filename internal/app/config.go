package app

import (
	"fmt"
	"log"
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

// Config is the full application configuration, sourced from env vars
// (or, when running as a Home Assistant add-on, from /data/options.json —
// see applyHAOptions).
type Config struct {
	Port               string
	BasePath           string // "" means served at root
	Token              string // must be set; explicitly "" disables auth (proxy-side auth)
	ProvisionPass      string // gates the sensor-provisioning API; "" disables it entirely
	HistoryMaxMB       int    // total in-memory history budget across all sensors
	Timeout            time.Duration
	MinInterval        time.Duration // device queries are rate-limited to one per this interval
	Metrics            bool          // expose Prometheus metrics at {BASE_PATH}/metrics
	AutoRefreshSec     int           // page auto-refresh interval
	AutoRefreshDefault bool          // whether auto-refresh starts enabled for new browsers
	BackgroundPollSec  int           // >0: the server polls the Shellys itself on this interval
	Endpoints          []EndpointConfig
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

func boolenv(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1"
}

func loadConfig() (*Config, error) {
	if err := applyHAOptions(); err != nil {
		return nil, err
	}
	// One shared memory budget for the whole history, independent of how
	// many sensors there are: 16 MB ≈ 260k samples at ~64 bytes each.
	histMB, err := strconv.Atoi(getenv("HISTORY_MAX_MB", "16"))
	if err != nil || histMB < 1 {
		return nil, fmt.Errorf("HISTORY_MAX_MB must be an integer >= 1")
	}
	if os.Getenv("HISTORY_SIZE") != "" {
		log.Printf("WARNING: HISTORY_SIZE is no longer used; the history is bounded by HISTORY_MAX_MB (currently %d MB) instead", histMB)
	}
	timeoutSec, err := strconv.Atoi(getenv("QUERY_TIMEOUT_SECONDS", "5"))
	if err != nil || timeoutSec < 1 {
		return nil, fmt.Errorf("QUERY_TIMEOUT_SECONDS must be an integer >= 1")
	}
	minIntervalSec, err := strconv.Atoi(getenv("QUERY_MIN_INTERVAL_SECONDS", "2"))
	if err != nil || minIntervalSec < 0 {
		return nil, fmt.Errorf("QUERY_MIN_INTERVAL_SECONDS must be an integer >= 0")
	}
	autoRefreshSec, err := strconv.Atoi(getenv("AUTO_REFRESH_SECONDS", "30"))
	if err != nil || autoRefreshSec < 1 {
		return nil, fmt.Errorf("AUTO_REFRESH_SECONDS must be an integer >= 1")
	}
	bgPollSec, err := strconv.Atoi(getenv("BACKGROUND_POLL_SECONDS", "0"))
	if err != nil || bgPollSec < 0 {
		return nil, fmt.Errorf("BACKGROUND_POLL_SECONDS must be an integer >= 0 (0 disables background polling)")
	}

	// The token must be an explicit decision: unset refuses to start, while
	// DEBUG_TOKEN="" deliberately disables auth (e.g. auth at the proxy).
	token, tokenSet := os.LookupEnv("DEBUG_TOKEN")
	token = strings.TrimSpace(token)
	if !tokenSet {
		return nil, fmt.Errorf("DEBUG_TOKEN must be set (pick a long random value; set DEBUG_TOKEN=\"\" explicitly to run without authentication, e.g. behind an authenticating reverse proxy)")
	}

	cfg := &Config{
		Port:               getenv("PORT", "8080"),
		BasePath:           normalizeBasePath(getenv("BASE_PATH", "/debug")),
		Token:              token,
		ProvisionPass:      strings.TrimSpace(os.Getenv("PROVISION_PASSPHRASE")),
		HistoryMaxMB:       histMB,
		Timeout:            time.Duration(timeoutSec) * time.Second,
		MinInterval:        time.Duration(minIntervalSec) * time.Second,
		Metrics:            boolenv("METRICS_ENABLED"),
		AutoRefreshSec:     autoRefreshSec,
		AutoRefreshDefault: boolenv("AUTO_REFRESH_DEFAULT"),
		BackgroundPollSec:  bgPollSec,
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
