package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// haOptionsFile is where the Home Assistant Supervisor mounts the add-on
// options. Its presence means "running as a Home Assistant add-on";
// overridable for tests.
var haOptionsFile = "/data/options.json"

// haOptions mirrors the schema in the Home Assistant add-on's config.yaml
// (repository steiner-dominik/home-assistant-apps).
type haOptions struct {
	Devices []struct {
		Host     string `json:"host"`
		Name     string `json:"name"`
		Password string `json:"password"`
		User     string `json:"user"`
	} `json:"devices"`
	ShellyPassword        string `json:"shelly_password"`
	DebugToken            string `json:"debug_token"`
	BackgroundPollSeconds *int   `json:"background_poll_seconds"`
	HistoryMaxMB          *int   `json:"history_max_mb"`
	AutoRefreshSeconds    *int   `json:"auto_refresh_seconds"`
	AutoRefreshDefault    *bool  `json:"auto_refresh_default"`
	QueryTimeoutSeconds   *int   `json:"query_timeout_seconds"`
	QueryMinIntervalSecs  *int   `json:"query_min_interval_seconds"`
	MetricsEnabled        *bool  `json:"metrics_enabled"`
}

// applyHAOptions translates the Supervisor's options file into the
// corresponding environment variables so the rest of the configuration
// works identically in both deployments. Explicitly set environment
// variables always win. In add-on mode the page is served at the root
// (Home Assistant ingress handles the path) and, unless a debug_token is
// configured, API auth is disabled — ingress already authenticates every
// request and the port is not published by default.
func applyHAOptions() error {
	data, err := os.ReadFile(haOptionsFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %v", haOptionsFile, err)
	}
	var o haOptions
	if err := json.Unmarshal(data, &o); err != nil {
		return fmt.Errorf("parsing %s: %v", haOptionsFile, err)
	}

	setIfUnset := func(key, val string) {
		if _, ok := os.LookupEnv(key); !ok {
			os.Setenv(key, val)
		}
	}
	for i, d := range o.Devices {
		n := i + 1
		setIfUnset(fmt.Sprintf("SHELLY_%d_HOST", n), d.Host)
		if d.Name != "" {
			setIfUnset(fmt.Sprintf("SHELLY_%d_NAME", n), d.Name)
		}
		if d.Password != "" {
			setIfUnset(fmt.Sprintf("SHELLY_%d_PASSWORD", n), d.Password)
		}
		if d.User != "" {
			setIfUnset(fmt.Sprintf("SHELLY_%d_USER", n), d.User)
		}
	}
	if o.ShellyPassword != "" {
		setIfUnset("SHELLY_PASSWORD", o.ShellyPassword)
	}
	setIfUnset("DEBUG_TOKEN", o.DebugToken) // "" = auth via ingress
	setIfUnset("BASE_PATH", "/")            // ingress serves the app at its root
	setInt := func(key string, v *int) {
		if v != nil {
			setIfUnset(key, strconv.Itoa(*v))
		}
	}
	setBool := func(key string, v *bool) {
		if v != nil && *v {
			setIfUnset(key, "true")
		}
	}
	setInt("BACKGROUND_POLL_SECONDS", o.BackgroundPollSeconds)
	setInt("HISTORY_MAX_MB", o.HistoryMaxMB)
	setInt("AUTO_REFRESH_SECONDS", o.AutoRefreshSeconds)
	setBool("AUTO_REFRESH_DEFAULT", o.AutoRefreshDefault)
	setInt("QUERY_TIMEOUT_SECONDS", o.QueryTimeoutSeconds)
	setInt("QUERY_MIN_INTERVAL_SECONDS", o.QueryMinIntervalSecs)
	setBool("METRICS_ENABLED", o.MetricsEnabled)
	return nil
}
