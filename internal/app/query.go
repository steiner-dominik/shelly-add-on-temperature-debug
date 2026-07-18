package app

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

// Sensor kinds supported on the add-on. The map value is the JSON field
// holding the reading in the device status.
var sensorKinds = map[string]string{
	"temperature": "tC", // DS18B20
	"humidity":    "rh", // DHT22
}

// SensorResult is the live reading of one add-on sensor.
type SensorResult struct {
	Key    string   `json:"key"` // e.g. "temperature:100"
	ID     int      `json:"id"`
	Kind   string   `json:"kind"` // "temperature" | "humidity"
	Name   string   `json:"name"`
	Value  *float64 `json:"value"` // °C or %RH; nil when the sensor gave no reading
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

func classifySensor(kind string, v *float64, errs []string, missing bool) string {
	switch {
	case missing:
		return statusMissing
	case len(errs) > 0 || v == nil:
		return statusReadError
	case kind == "temperature" && *v == 85.0:
		return statusReset85
	default:
		return statusOK
	}
}

// splitComponentKey parses "temperature:101" into a supported kind and id.
func splitComponentKey(key string) (kind string, id int, ok bool) {
	kind, idStr, found := strings.Cut(key, ":")
	if !found || sensorKinds[kind] == "" {
		return "", 0, false
	}
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return "", 0, false
	}
	return kind, id, true
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
	// external sensors.
	present := map[string]bool{}
	for key, v := range comps {
		kind, id, ok := splitComponentKey(key)
		if !ok || id < 100 {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(v, &fields) != nil {
			continue
		}
		var value *float64
		json.Unmarshal(fields[sensorKinds[kind]], &value)
		var errs []string
		json.Unmarshal(fields["errors"], &errs)
		present[key] = true
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: id, Kind: kind, Name: sensorName(dm, key, id),
			Value: value, Errors: errs, Status: classifySensor(kind, value, errs, false),
		})
	}
	// Sensors that are configured but absent from the live status.
	for key := range dm.SensorNames {
		kind, id, ok := splitComponentKey(key)
		if !ok || id < 100 || present[key] {
			continue
		}
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: id, Kind: kind, Name: sensorName(dm, key, id),
			Status: classifySensor(kind, nil, nil, true),
		})
	}
	// Temperature sensors first (the primary use case), then humidity.
	kindOrder := map[string]int{"temperature": 0, "humidity": 1}
	sort.Slice(res.Sensors, func(i, j int) bool {
		a, b := res.Sensors[i], res.Sensors[j]
		if a.Kind != b.Kind {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		return a.ID < b.ID
	})

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

// querySensor reads one single add-on sensor via its dedicated component RPC
// (Temperature.GetStatus / Humidity.GetStatus), so a suspect sensor can be
// polled without touching the whole device status. An RPC failure is reported
// as read_error with the transport error attached — for troubleshooting the
// distinction "this one sensor did not answer" is what matters.
func querySensor(ctx context.Context, ep EndpointConfig, timeout time.Duration, meta *metaCache, key string) SensorResult {
	kind, id, _ := splitComponentKey(key)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := newShellyClient(ep, timeout)
	dm := meta.get(ctx, client, ep.BaseURL)
	res := SensorResult{Key: key, ID: id, Kind: kind, Name: sensorName(dm, key, id)}

	method := map[string]string{"temperature": "Temperature.GetStatus", "humidity": "Humidity.GetStatus"}[kind]
	raw, err := client.rpc(ctx, fmt.Sprintf("%s?id=%d", method, id))
	if err != nil {
		res.Errors = []string{err.Error()}
		res.Status = classifySensor(kind, nil, res.Errors, false)
		return res
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		res.Errors = []string{"unexpected response from device: " + e.Error()}
		res.Status = classifySensor(kind, nil, res.Errors, false)
		return res
	}
	var value *float64
	json.Unmarshal(fields[sensorKinds[kind]], &value)
	var errs []string
	json.Unmarshal(fields["errors"], &errs)
	res.Value = value
	res.Errors = errs
	res.Status = classifySensor(kind, value, errs, false)
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
