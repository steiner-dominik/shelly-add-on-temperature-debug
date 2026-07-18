package main

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
