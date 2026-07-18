package app

import (
	"sync"
	"time"
)

// Sample is one recorded reading of one sensor.
type Sample struct {
	TS     int64    `json:"ts"` // unix seconds
	V      *float64 `json:"v"`  // °C or %RH depending on the sensor kind
	Status string   `json:"status"`
}

// bytesPerSample is the estimated in-memory cost of one Sample: 32 bytes in
// the slice (int64 + pointer + string header), ~16 bytes for the heap float,
// plus slack for slice growth. The status strings are shared constants and
// cost nothing per sample. Deliberately conservative so HISTORY_MAX_MB is an
// upper bound, not a target.
const bytesPerSample = 64

type sensorHistory struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Samples []Sample `json:"samples"`
}

// history is an in-memory buffer of samples per endpoint/sensor with one
// global memory budget: when the total sample count would exceed it, the
// oldest sample across all sensors is dropped. It is the only state the
// application holds; it is lost on restart by design.
type history struct {
	mu    sync.Mutex
	max   int // total samples across all sensors (budget / bytesPerSample)
	total int
	data  map[string]map[string]*sensorHistory // endpoint name -> sensor key -> history
}

// newHistory sizes the buffer from a byte budget shared by all sensors.
func newHistory(maxBytes int64) *history {
	max := int(maxBytes / bytesPerSample)
	if max < 2 {
		max = 2
	}
	return &history{max: max, data: map[string]map[string]*sensorHistory{}}
}

// record appends the sensors of one query result to the buffer.
func (h *history) record(results []EndpointResult) {
	now := time.Now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ep := range results {
		for _, s := range ep.Sensors {
			h.append(ep.Name, s, now)
		}
	}
}

// recordSensor appends a single-sensor query result to the buffer.
func (h *history) recordSensor(endpoint string, s SensorResult, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.append(endpoint, s, at.Unix())
}

// append adds one sample and evicts the oldest sample(s) overall while the
// budget is exceeded; the caller must hold h.mu.
func (h *history) append(endpoint string, s SensorResult, now int64) {
	byKey := h.data[endpoint]
	if byKey == nil {
		byKey = map[string]*sensorHistory{}
		h.data[endpoint] = byKey
	}
	sh := byKey[s.Key]
	if sh == nil {
		sh = &sensorHistory{}
		byKey[s.Key] = sh
	}
	sh.Name = s.Name
	sh.Kind = s.Kind
	sh.Samples = append(sh.Samples, Sample{TS: now, V: s.Value, Status: s.Status})
	h.total++
	for h.total > h.max {
		if !h.evictOldest() {
			break
		}
	}
}

// evictOldest drops the single oldest sample across all sensors, so the
// budget is spent on the most recent data no matter how it is distributed
// between sensors. Returns false when there was nothing to evict.
func (h *history) evictOldest() bool {
	var victim *sensorHistory
	for _, byKey := range h.data {
		for _, sh := range byKey {
			if len(sh.Samples) == 0 {
				continue
			}
			if victim == nil || sh.Samples[0].TS < victim.Samples[0].TS {
				victim = sh
			}
		}
	}
	if victim == nil {
		return false
	}
	victim.Samples = victim.Samples[1:]
	h.total--
	return true
}

// clear drops all recorded samples.
func (h *history) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data = map[string]map[string]*sensorHistory{}
	h.total = 0
}

// snapshot returns a deep copy safe for JSON serialization. limit > 0 keeps
// only the newest limit samples per sensor (used by the page charts so a
// large buffer doesn't produce huge responses); limit <= 0 returns everything.
func (h *history) snapshot(limit int) map[string]map[string]sensorHistory {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]map[string]sensorHistory, len(h.data))
	for ep, byKey := range h.data {
		m := make(map[string]sensorHistory, len(byKey))
		for key, sh := range byKey {
			src := sh.Samples
			if limit > 0 && len(src) > limit {
				src = src[len(src)-limit:]
			}
			samples := make([]Sample, len(src))
			copy(samples, src)
			m[key] = sensorHistory{Name: sh.Name, Kind: sh.Kind, Samples: samples}
		}
		out[ep] = m
	}
	return out
}
