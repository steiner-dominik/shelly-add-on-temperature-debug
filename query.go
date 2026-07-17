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

// Sensor / endpoint status codes. The frontend maps these to colors and the
// guidance strings below explain each one in plain language.
const (
	statusOK          = "ok"
	statusReset85     = "reset85"
	statusReadError   = "read_error"
	statusMissing     = "missing"
	statusUnreachable = "unreachable"
	statusAuthFailed  = "auth_failed"
	statusNoSensors   = "no_sensors"
)

var sensorGuidance = map[string]string{
	statusReadError: "The sensor did not answer the last read. This is almost always a wiring or contact problem: re-seat the plug on the add-on, check the three terminals (GND / DATA / 3V3) for loose or corroded wires, and inspect the cable for damage or moisture. If all sensors on this device fail at once, suspect the shared cable or the add-on board itself.",
	statusReset85:   "Exactly 85.0 °C is the DS18B20 power-on default: the sensor rebooted and was read before it finished a real measurement. This points to an unstable supply or data line — cable too long or low quality, intermittent contact, or interference. Query again: if 85.0 keeps coming back, treat it like a wiring problem. (If ~85 °C is physically plausible here, verify with a second reading.)",
	statusMissing:   "This sensor is configured on the Shelly but its component did not appear in the live status. It may have been physically disconnected, or the add-on lost it. Open the Shelly web UI → Add-on / Peripherals and re-detect sensors, and check the wiring.",
}

var endpointGuidance = map[string]string{
	statusUnreachable: "The Shelly could not be reached at all (timeout or connection refused). Check: is the device powered and online, is the IP/FQDN correct, and can this server actually route to it (VPN/firewall)? A weak Wi-Fi link on the Shelly can also cause intermittent timeouts.",
	statusAuthFailed:  "The Shelly rejected the credentials (HTTP 401). Check the configured password (SHELLY_n_PASSWORD / SHELLY_PASSWORD) and user.",
	statusNoSensors:   "The Shelly answered, but reported no add-on temperature sensors (no temperature:100+ components). Is the Sensor Add-on physically attached and enabled, and have the DS18B20 sensors been detected in the Shelly web UI (Add-on / Peripherals settings)?",
}

// SensorResult is the live reading of one DS18B20 sensor.
type SensorResult struct {
	Key      string   `json:"key"` // e.g. "temperature:100"
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	TC       *float64 `json:"tC"` // nil when the sensor gave no reading
	Errors   []string `json:"errors,omitempty"`
	Status   string   `json:"status"`
	Guidance string   `json:"guidance,omitempty"`
}

// EndpointResult is the outcome of querying one Shelly device.
type EndpointResult struct {
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
	Guidance string         `json:"guidance,omitempty"`
	Device   string         `json:"device,omitempty"` // "SNSN-0043X (gen3, fw 1.4.4)"
	WifiRSSI *int           `json:"wifiRssi,omitempty"`
	Uptime   *int64         `json:"uptimeSec,omitempty"`
	Notes    []string       `json:"notes,omitempty"`
	Sensors  []SensorResult `json:"sensors"`
}

func classifySensor(tC *float64, errs []string, missing bool) (string, string) {
	switch {
	case missing:
		return statusMissing, sensorGuidance[statusMissing]
	case len(errs) > 0 || tC == nil:
		return statusReadError, sensorGuidance[statusReadError]
	case *tC == 85.0:
		return statusReset85, sensorGuidance[statusReset85]
	default:
		return statusOK, ""
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
		res.Guidance = endpointGuidance[res.Status]
		return res
	}

	var comps map[string]json.RawMessage
	if err := json.Unmarshal(raw, &comps); err != nil {
		res.Status = statusUnreachable
		res.Error = "unexpected response from device: " + err.Error()
		res.Guidance = endpointGuidance[statusUnreachable]
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
		status, guidance := classifySensor(t.TC, t.Errors, false)
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: t.ID, Name: sensorName(dm, key, t.ID),
			TC: t.TC, Errors: t.Errors, Status: status, Guidance: guidance,
		})
	}
	// Sensors that are configured but absent from the live status.
	for key := range dm.SensorNames {
		var id int
		if _, err := fmt.Sscanf(key, "temperature:%d", &id); err != nil || id < 100 || present[key] {
			continue
		}
		status, guidance := classifySensor(nil, nil, true)
		res.Sensors = append(res.Sensors, SensorResult{
			Key: key, ID: id, Name: sensorName(dm, key, id),
			Status: status, Guidance: guidance,
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
		if sys.Wifi.RSSI != nil && *sys.Wifi.RSSI <= -75 {
			res.Notes = append(res.Notes, fmt.Sprintf("Weak Wi-Fi signal (%d dBm) — intermittent sensor dropouts can also be caused by the Shelly itself losing connectivity.", *sys.Wifi.RSSI))
		}
	}

	if len(res.Sensors) == 0 {
		res.Status = statusNoSensors
		res.Guidance = endpointGuidance[statusNoSensors]
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
