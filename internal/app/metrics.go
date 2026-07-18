package app

import (
	"fmt"
	"strings"
	"time"
)

// renderMetrics produces Prometheus text exposition format (version 0.0.4)
// from the latest query results. No client library needed for gauges.
func renderMetrics(results []EndpointResult, ts time.Time, version string) []byte {
	var b strings.Builder

	b.WriteString("# HELP shelly_debug_info Build information.\n# TYPE shelly_debug_info gauge\n")
	fmt.Fprintf(&b, "shelly_debug_info{version=%q} 1\n", version)

	b.WriteString("# HELP shelly_debug_last_query_timestamp_seconds Unix time of the device query these metrics come from.\n# TYPE shelly_debug_last_query_timestamp_seconds gauge\n")
	fmt.Fprintf(&b, "shelly_debug_last_query_timestamp_seconds %d\n", ts.Unix())

	b.WriteString("# HELP shelly_debug_endpoint_up 1 if the Shelly answered the query (regardless of sensor health).\n# TYPE shelly_debug_endpoint_up gauge\n")
	for _, ep := range results {
		up := 1
		if ep.Status == statusUnreachable || ep.Status == statusAuthFailed {
			up = 0
		}
		fmt.Fprintf(&b, "shelly_debug_endpoint_up{endpoint=%s} %d\n", label(ep.Name), up)
	}

	b.WriteString("# HELP shelly_debug_endpoint_wifi_rssi_dbm Wi-Fi signal strength reported by the Shelly.\n# TYPE shelly_debug_endpoint_wifi_rssi_dbm gauge\n")
	for _, ep := range results {
		if ep.WifiRSSI != nil {
			fmt.Fprintf(&b, "shelly_debug_endpoint_wifi_rssi_dbm{endpoint=%s} %d\n", label(ep.Name), *ep.WifiRSSI)
		}
	}

	b.WriteString("# HELP shelly_debug_endpoint_uptime_seconds Device uptime reported by the Shelly.\n# TYPE shelly_debug_endpoint_uptime_seconds gauge\n")
	for _, ep := range results {
		if ep.Uptime != nil {
			fmt.Fprintf(&b, "shelly_debug_endpoint_uptime_seconds{endpoint=%s} %d\n", label(ep.Name), *ep.Uptime)
		}
	}

	b.WriteString("# HELP shelly_debug_temperature_celsius DS18B20 reading; absent while the sensor gives no value.\n# TYPE shelly_debug_temperature_celsius gauge\n")
	for _, ep := range results {
		for _, s := range ep.Sensors {
			if s.Kind == "temperature" && s.Value != nil {
				fmt.Fprintf(&b, "shelly_debug_temperature_celsius{endpoint=%s,sensor=%s,key=%s} %g\n",
					label(ep.Name), label(s.Name), label(s.Key), *s.Value)
			}
		}
	}

	b.WriteString("# HELP shelly_debug_humidity_percent DHT22 relative-humidity reading; absent while the sensor gives no value.\n# TYPE shelly_debug_humidity_percent gauge\n")
	for _, ep := range results {
		for _, s := range ep.Sensors {
			if s.Kind == "humidity" && s.Value != nil {
				fmt.Fprintf(&b, "shelly_debug_humidity_percent{endpoint=%s,sensor=%s,key=%s} %g\n",
					label(ep.Name), label(s.Name), label(s.Key), *s.Value)
			}
		}
	}

	b.WriteString("# HELP shelly_debug_sensor_ok 1 if the sensor reported a plausible value, 0 on any problem (85 °C reset, read error, missing).\n# TYPE shelly_debug_sensor_ok gauge\n")
	for _, ep := range results {
		for _, s := range ep.Sensors {
			ok := 0
			if s.Status == statusOK {
				ok = 1
			}
			fmt.Fprintf(&b, "shelly_debug_sensor_ok{endpoint=%s,sensor=%s,key=%s,kind=%s} %d\n",
				label(ep.Name), label(s.Name), label(s.Key), label(s.Kind), ok)
		}
	}

	// State-set pattern: one stable series per possible status, exactly one is 1.
	b.WriteString("# HELP shelly_debug_sensor_status Sensor state as a state set; the series with value 1 is the current status.\n# TYPE shelly_debug_sensor_status gauge\n")
	sensorStatuses := []string{statusOK, statusReset85, statusReadError, statusMissing}
	for _, ep := range results {
		for _, s := range ep.Sensors {
			for _, st := range sensorStatuses {
				v := 0
				if s.Status == st {
					v = 1
				}
				fmt.Fprintf(&b, "shelly_debug_sensor_status{endpoint=%s,key=%s,status=%s} %d\n",
					label(ep.Name), label(s.Key), label(st), v)
			}
		}
	}

	return []byte(b.String())
}

// label quotes and escapes a Prometheus label value.
func label(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return `"` + v + `"`
}
