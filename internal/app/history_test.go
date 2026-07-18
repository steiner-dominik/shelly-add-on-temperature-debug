package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryMemoryBudgetEvictsOldestOverall(t *testing.T) {
	// Budget of 4 samples total (4 × bytesPerSample), shared by two sensors.
	h := newHistory(4 * bytesPerSample)
	rec := func(key string, ts int64, v float64) {
		h.recordSensor("A", SensorResult{Key: key, Kind: "temperature", Name: key, Value: f(v)}, time.Unix(ts, 0))
	}
	rec("temperature:100", 10, 1)
	rec("temperature:101", 11, 2)
	rec("temperature:100", 12, 3)
	rec("temperature:101", 13, 4)
	rec("temperature:100", 14, 5) // exceeds the budget → ts=10 must go

	snap := h.snapshot(0)["A"]
	s100, s101 := snap["temperature:100"].Samples, snap["temperature:101"].Samples
	if len(s100)+len(s101) != 4 {
		t.Fatalf("total samples = %d, want 4", len(s100)+len(s101))
	}
	if len(s100) != 2 || s100[0].TS != 12 {
		t.Errorf("oldest overall sample (ts=10) should have been evicted: %+v", s100)
	}
	if len(s101) != 2 {
		t.Errorf("the other sensor must keep its samples: %+v", s101)
	}

	// A sensor polled heavily (e.g. wiggle test) may consume the budget...
	for i := int64(0); i < 10; i++ {
		rec("temperature:100", 20+i, 9)
	}
	snap = h.snapshot(0)["A"]
	total := len(snap["temperature:100"].Samples) + len(snap["temperature:101"].Samples)
	if total != 4 {
		t.Fatalf("after burst: total samples = %d, want 4", total)
	}
	if got := snap["temperature:100"].Samples; len(got) != 4 || got[0].TS != 26 {
		t.Errorf("burst sensor should hold the 4 newest samples: %+v", got)
	}
}

func TestNewHistoryMinimumBudget(t *testing.T) {
	h := newHistory(1) // absurdly small budget must still keep 2 samples
	if h.max != 2 {
		t.Fatalf("max = %d, want 2", h.max)
	}
}

func TestApplyHAOptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "options.json")
	if err := os.WriteFile(file, []byte(`{
		"devices": [
			{"host": "192.168.1.50", "name": "Pool", "password": "pw1"},
			{"host": "shelly-garden.lan"}
		],
		"shelly_password": "global",
		"debug_token": "",
		"background_poll_seconds": 60,
		"history_max_mb": 32,
		"metrics_enabled": true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := haOptionsFile
	haOptionsFile = file
	defer func() { haOptionsFile = orig }()
	// applyHAOptions writes env vars; isolate and clean them up.
	for _, k := range []string{"SHELLY_1_HOST", "SHELLY_1_NAME", "SHELLY_1_PASSWORD", "SHELLY_2_HOST",
		"SHELLY_PASSWORD", "DEBUG_TOKEN", "BASE_PATH", "BACKGROUND_POLL_SECONDS", "HISTORY_MAX_MB", "METRICS_ENABLED"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HISTORY_MAX_MB", "8") // explicit env must win over the options file

	if err := applyHAOptions(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SHELLY_1_HOST":           "192.168.1.50",
		"SHELLY_1_NAME":           "Pool",
		"SHELLY_1_PASSWORD":       "pw1",
		"SHELLY_2_HOST":           "shelly-garden.lan",
		"SHELLY_PASSWORD":         "global",
		"BASE_PATH":               "/",
		"BACKGROUND_POLL_SECONDS": "60",
		"HISTORY_MAX_MB":          "8",
		"METRICS_ENABLED":         "true",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Empty debug_token must still count as "set" (auth disabled for ingress).
	if v, ok := os.LookupEnv("DEBUG_TOKEN"); !ok || v != "" {
		t.Errorf("DEBUG_TOKEN: set=%v value=%q, want set with empty value", ok, v)
	}
}

func TestApplyHAOptionsMissingFileIsNoop(t *testing.T) {
	orig := haOptionsFile
	haOptionsFile = filepath.Join(t.TempDir(), "nope.json")
	defer func() { haOptionsFile = orig }()
	if err := applyHAOptions(); err != nil {
		t.Fatalf("missing options file must not be an error: %v", err)
	}
}
