package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeShelly serves a minimal unauthenticated Gen2+ RPC surface with one
// healthy DS18B20, one failing DS18B20, and one DHT22 (temp+humidity).
func fakeShelly() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/Shelly.GetStatus", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"sys": {"uptime": 1234},
			"wifi": {"rssi": -80},
			"switch:0": {"id":0, "temperature": {"tC": 45.0}},
			"temperature:100": {"id":100, "tC": 21.5},
			"temperature:101": {"id":101, "tC": null, "errors": ["read"]},
			"temperature:102": {"id":102, "tC": 22.0},
			"humidity:102": {"id":102, "rh": 55.5}
		}`))
	})
	mux.HandleFunc("/rpc/Temperature.GetStatus", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "100":
			w.Write([]byte(`{"id":100, "tC": 21.5, "tF": 70.7}`))
		case "101":
			w.Write([]byte(`{"id":101, "tC": null, "errors": ["read"]}`))
		default:
			http.Error(w, `{"code":-105,"message":"no such component"}`, http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/rpc/Shelly.GetDeviceInfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"name":"fake","model":"TEST-1","gen":3,"ver":"9.9.9","app":"Test"}`))
	})
	mux.HandleFunc("/rpc/Shelly.GetConfig", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"temperature:100": {"id":100, "name": "Pool"},
			"temperature:101": {"id":101, "name": "Broken"},
			"temperature:102": {"id":102, "name": "DHT temp"},
			"humidity:102": {"id":102, "name": "DHT humidity"},
			"temperature:103": {"id":103, "name": "Gone"}
		}`))
	})
	return httptest.NewServer(mux)
}

func TestQueryEndpointWithHumidityAndMissing(t *testing.T) {
	srv := fakeShelly()
	defer srv.Close()

	ep := EndpointConfig{Name: "Fake", BaseURL: srv.URL, Host: "fake", User: "admin"}
	res := queryEndpoint(context.Background(), ep, 3*time.Second, newMetaCache())

	if res.Status != statusOK {
		t.Fatalf("endpoint status = %q, err = %q", res.Status, res.Error)
	}
	want := map[string]struct {
		kind   string
		status string
	}{
		"temperature:100": {"temperature", statusOK},
		"temperature:101": {"temperature", statusReadError},
		"temperature:102": {"temperature", statusOK},
		"humidity:102":    {"humidity", statusOK},
		"temperature:103": {"temperature", statusMissing},
	}
	if len(res.Sensors) != len(want) {
		t.Fatalf("got %d sensors, want %d: %+v", len(res.Sensors), len(want), res.Sensors)
	}
	for _, s := range res.Sensors {
		w, ok := want[s.Key]
		if !ok {
			t.Errorf("unexpected sensor %q (the switch:0 internal temperature must not appear)", s.Key)
			continue
		}
		if s.Kind != w.kind || s.Status != w.status {
			t.Errorf("%s: kind=%q status=%q, want kind=%q status=%q", s.Key, s.Kind, s.Status, w.kind, w.status)
		}
	}
	// Temperature sensors must be sorted before humidity.
	if res.Sensors[len(res.Sensors)-1].Kind != "humidity" {
		t.Errorf("humidity sensor should sort last: %+v", res.Sensors)
	}
	if res.Sensors[len(res.Sensors)-1].Name != "DHT humidity" {
		t.Errorf("configured humidity name not used: %+v", res.Sensors[len(res.Sensors)-1])
	}
}

func TestQuerySensor(t *testing.T) {
	srv := fakeShelly()
	defer srv.Close()
	ep := EndpointConfig{Name: "Fake", BaseURL: srv.URL, Host: "fake", User: "admin"}
	meta := newMetaCache()

	res := querySensor(context.Background(), ep, 3*time.Second, meta, "temperature:100")
	if res.Status != statusOK || res.Value == nil || *res.Value != 21.5 || res.Name != "Pool" {
		t.Errorf("healthy sensor: %+v", res)
	}
	res = querySensor(context.Background(), ep, 3*time.Second, meta, "temperature:101")
	if res.Status != statusReadError || res.Value != nil {
		t.Errorf("failing sensor: %+v", res)
	}
	res = querySensor(context.Background(), ep, 3*time.Second, meta, "temperature:199")
	if res.Status != statusReadError || len(res.Errors) == 0 {
		t.Errorf("unknown sensor should surface an error: %+v", res)
	}
}

func TestRequireToken(t *testing.T) {
	s := &server{cfg: &Config{Token: "secret"}}
	h := s.requireToken(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		name   string
		header map[string]string
		url    string
		want   int
	}{
		{"no token", nil, "/api/query", http.StatusUnauthorized},
		{"header token", map[string]string{"X-Debug-Token": "secret"}, "/api/query", http.StatusOK},
		{"bearer token", map[string]string{"Authorization": "Bearer secret"}, "/api/query", http.StatusOK},
		{"wrong header token", map[string]string{"X-Debug-Token": "nope"}, "/api/query", http.StatusUnauthorized},
		{"URL parameter is never accepted", nil, "/api/query?token=secret", http.StatusUnauthorized},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.url, nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}

	// Explicitly empty token disables the gate entirely.
	open := &server{cfg: &Config{Token: ""}}
	oh := open.requireToken(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	oh(w, httptest.NewRequest(http.MethodGet, "/api/query", nil))
	if w.Code != http.StatusOK {
		t.Errorf("empty token config: got %d, want 200", w.Code)
	}
}

func TestHistoryClear(t *testing.T) {
	h := newHistory(1 << 20)
	h.record([]EndpointResult{{Name: "A", Sensors: []SensorResult{{Key: "temperature:100", Kind: "temperature", Name: "S", Value: f(20)}}}})
	h.recordSensor("A", SensorResult{Key: "temperature:100", Kind: "temperature", Name: "S", Value: f(21)}, time.Now())
	if got := len(h.snapshot(0)["A"]["temperature:100"].Samples); got != 2 {
		t.Fatalf("got %d samples, want 2", got)
	}
	if got := len(h.snapshot(1)["A"]["temperature:100"].Samples); got != 1 {
		t.Fatalf("limited snapshot: got %d samples, want 1", got)
	}
	if v := h.snapshot(1)["A"]["temperature:100"].Samples[0].V; v == nil || *v != 21 {
		t.Fatalf("limited snapshot must keep the newest sample, got %+v", v)
	}
	h.clear()
	if len(h.snapshot(0)) != 0 {
		t.Fatal("history not empty after clear")
	}
}

func TestRenderMetrics(t *testing.T) {
	srv := fakeShelly()
	defer srv.Close()
	ep := EndpointConfig{Name: `Pool "1"`, BaseURL: srv.URL, Host: "fake", User: "admin"}
	res := queryEndpoint(context.Background(), ep, 3*time.Second, newMetaCache())

	out := string(renderMetrics([]EndpointResult{res}, time.Unix(1784000000, 0), "test"))
	for _, wantLine := range []string{
		`shelly_debug_endpoint_up{endpoint="Pool \"1\""} 1`,
		`shelly_debug_temperature_celsius{endpoint="Pool \"1\"",sensor="Pool",key="temperature:100"} 21.5`,
		`shelly_debug_humidity_percent{endpoint="Pool \"1\"",sensor="DHT humidity",key="humidity:102"} 55.5`,
		`shelly_debug_sensor_ok{endpoint="Pool \"1\"",sensor="Broken",key="temperature:101",kind="temperature"} 0`,
		`shelly_debug_sensor_status{endpoint="Pool \"1\"",key="temperature:101",status="read_error"} 1`,
		`shelly_debug_endpoint_wifi_rssi_dbm{endpoint="Pool \"1\""} -80`,
		`shelly_debug_last_query_timestamp_seconds 1784000000`,
	} {
		if !strings.Contains(out, wantLine) {
			t.Errorf("metrics output missing line:\n%s", wantLine)
		}
	}
	// A failed sensor must not emit a value sample.
	if strings.Contains(out, `key="temperature:101"} `) && strings.Contains(out, `shelly_debug_temperature_celsius{endpoint="Pool \"1\"",sensor="Broken"`) {
		t.Error("broken sensor should not export a temperature value")
	}
}
