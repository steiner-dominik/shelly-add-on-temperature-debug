package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sensor / endpoint status codes. The frontend maps these to localized
// labels, colors, and guidance texts (see locales/*.json).
const (
	statusOK          = "ok"
	statusReset85     = "reset85"
	statusReadError   = "read_error"
	statusMissing     = "missing"
	statusUnreachable = "unreachable"
	statusAuthFailed  = "auth_failed"
	statusNoSensors   = "no_sensors"
)

// SensorResult is the live reading of one DS18B20 sensor.
type SensorResult struct {
	Key    string   `json:"key"` // e.g. "temperature:100"
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	TC     *float64 `json:"tC"` // nil when the sensor gave no reading
	Errors []string `json:"errors,omitempty"`
	Status string   `json:"status"`
}

// EndpointResult is the outcome of querying one Shelly device.
type EndpointResult struct {
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
	Device   string         `json:"device,omitempty"` // "S4SW-002P16EU (gen4, fw 1.7.5)"
	WifiRSSI *int           `json:"wifiRssi,omitempty"`
	Uptime   *int64         `json:"uptimeSec,omitempty"`
	Sensors  []SensorResult `json:"sensors"`
}

func classifySensor(tC *float64, errs []string, missing bool) string {
	switch {
	case missing:
		return statusMissing
	case len(errs) > 0 || tC == nil:
		return statusReadError
	case *tC == 85.0:
		return statusReset85
	default:
		return statusOK
	}
}

func queryEndpoint(ctx context.Context, ep EndpointConfig, timeout time.Duration, meta *metaCache) EndpointResult {
	res := EndpointResult{Name: ep.Name, Host: ep.Host, Sensors: []SensorResult{}}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := newShellyClient(ep, timeout)
	raw, err := client.rpc(ctx, "Shelly.GetStatus")
	if err != nil {
		if errors.Is(err, errAuth) {
			res.Status = statusAuthFailed
		} else {
			res.Status = statusUnreachable
		}
		res.Error = err.Error()
		return res
	}

	var comps map[string]json.RawMessage
	if err := json.Unmarshal(raw, &comps); err != nil {
		res.Status = statusUnreachable
		res.Error = "unexpected response from device: " + err.Error()
		return res
	}

	dm := meta.get(ctx, client, ep.BaseURL)
	if dm.Model != "" {
		res.Device = fmt.Sprintf("%s (gen%d, fw %s)", dm.Model, dm.Gen, dm.Firmware)
	}

	// Add-on peripherals get component IDs starting at 100; the device's
	// internal temperature lives elsewhere (switch:*), so this only sees
	// the external DS18B20 sensors.
	present := map[string]bool{}
	for key, v := range comps {
		if !strings.HasPrefix(key, "temperature:") {
			continue
		}
		var t struct {
			ID     int      `json:"id"`
			TC     *float64 `json:"tC"`
			Errors []string `json:"errors"`
		}
		if err := json.Unmarshal(v, &t); err != nil || t.ID < 100 {
			continue
		}
		present[key] = true
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: t.ID, Name: sensorName(dm, key, t.ID),
			TC: t.TC, Errors: t.Errors, Status: classifySensor(t.TC, t.Errors, false),
		})
	}
	// Sensors that are configured but absent from the live status.
	for key := range dm.SensorNames {
		var id int
		if _, err := fmt.Sscanf(key, "temperature:%d", &id); err != nil || id < 100 || present[key] {
			continue
		}
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: id, Name: sensorName(dm, key, id),
			Status: classifySensor(nil, nil, true),
		})
	}
	sort.Slice(res.Sensors, func(i, j int) bool { return res.Sensors[i].ID < res.Sensors[j].ID })

	var sys struct {
		Sys struct {
			Uptime *int64 `json:"uptime"`
		} `json:"sys"`
		Wifi struct {
			RSSI *int `json:"rssi"`
		} `json:"wifi"`
	}
	if json.Unmarshal(raw, &sys) == nil {
		res.Uptime = sys.Sys.Uptime
		res.WifiRSSI = sys.Wifi.RSSI
	}

	if len(res.Sensors) == 0 {
		res.Status = statusNoSensors
	} else {
		res.Status = statusOK
	}
	return res
}

// queryAll queries every configured endpoint in parallel.
func queryAll(ctx context.Context, cfg *Config, meta *metaCache) []EndpointResult {
	results := make([]EndpointResult, len(cfg.Endpoints))
	var wg sync.WaitGroup
	for i, ep := range cfg.Endpoints {
		wg.Add(1)
		go func(i int, ep EndpointConfig) {
			defer wg.Done()
			results[i] = queryEndpoint(ctx, ep, cfg.Timeout, meta)
		}(i, ep)
	}
	wg.Wait()
	return results
}

func sensorName(dm *deviceMeta, key string, id int) string {
	if n := dm.SensorNames[key]; n != "" {
		return n
	}
	return fmt.Sprintf("Sensor %d", id-99)
}
