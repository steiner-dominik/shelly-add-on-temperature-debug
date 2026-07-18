package main

import "testing"

func f(v float64) *float64 { return &v }

func TestClassifySensor(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		v       *float64
		errs    []string
		missing bool
		want    string
	}{
		{"normal reading", "temperature", f(21.5), nil, false, statusOK},
		{"real 85.1 is ok", "temperature", f(85.1), nil, false, statusOK},
		{"exact 85 is reset", "temperature", f(85.0), nil, false, statusReset85},
		{"85 %RH is a normal humidity", "humidity", f(85.0), nil, false, statusOK},
		{"nil reading", "temperature", nil, nil, false, statusReadError},
		{"humidity nil reading", "humidity", nil, nil, false, statusReadError},
		{"read error", "temperature", f(20.0), []string{"read"}, false, statusReadError},
		{"missing", "temperature", nil, nil, true, statusMissing},
	}
	for _, c := range cases {
		if got := classifySensor(c.kind, c.v, c.errs, c.missing); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeBasePath(t *testing.T) {
	cases := map[string]string{
		"/debug":  "/debug",
		"debug":   "/debug",
		"/debug/": "/debug",
		"/":       "",
		"":        "",
	}
	for in, want := range cases {
		if got := normalizeBasePath(in); got != want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadConfigEndpoints(t *testing.T) {
	t.Setenv("SHELLY_1_HOST", "192.168.1.10")
	t.Setenv("SHELLY_1_NAME", "Pool")
	t.Setenv("SHELLY_2_HOST", "https://shelly2.example.com/")
	t.Setenv("SHELLY_2_PASSWORD", "override")
	t.Setenv("SHELLY_PASSWORD", "shared")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoints) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(cfg.Endpoints))
	}
	e1, e2 := cfg.Endpoints[0], cfg.Endpoints[1]
	if e1.BaseURL != "http://192.168.1.10" || e1.Name != "Pool" || e1.Password != "shared" || e1.User != "admin" {
		t.Errorf("endpoint 1 wrong: %+v", e1)
	}
	if e2.BaseURL != "https://shelly2.example.com" || e2.Password != "override" || e2.Name != "shelly2.example.com" {
		t.Errorf("endpoint 2 wrong: %+v", e2)
	}
}

func TestLocaleFilesValid(t *testing.T) {
	if _, err := buildLocaleIndex(); err != nil {
		t.Fatalf("locale files invalid: %v", err)
	}
}
