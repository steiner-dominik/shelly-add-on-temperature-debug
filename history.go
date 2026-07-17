package main

import (
	"sync"
	"time"
)

// Sample is one recorded reading of one sensor.
type Sample struct {
	TS     int64    `json:"ts"` // unix seconds
	TC     *float64 `json:"tC"`
	Status string   `json:"status"`
}

type sensorHistory struct {
	Name    string   `json:"name"`
	Samples []Sample `json:"samples"`
}

// history is an in-memory ring buffer of samples per endpoint/sensor.
// It is the only state the application holds; it is lost on restart by design.
type history struct {
	mu   sync.Mutex
	size int
	data map[string]map[string]*sensorHistory // endpoint name -> sensor key -> history
}

func newHistory(size int) *history {
	return &history{size: size, data: map[string]map[string]*sensorHistory{}}
}

// record appends the sensors of one query result to the buffer.
func (h *history) record(results []EndpointResult) {
	now := time.Now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ep := range results {
		for _, s := range ep.Sensors {
			byKey := h.data[ep.Name]
			if byKey == nil {
				byKey = map[string]*sensorHistory{}
				h.data[ep.Name] = byKey
			}
			sh := byKey[s.Key]
			if sh == nil {
				sh = &sensorHistory{}
				byKey[s.Key] = sh
			}
			sh.Name = s.Name
			sh.Samples = append(sh.Samples, Sample{TS: now, TC: s.TC, Status: s.Status})
			if len(sh.Samples) > h.size {
				sh.Samples = sh.Samples[len(sh.Samples)-h.size:]
			}
		}
	}
}

// snapshot returns a deep copy safe for JSON serialization.
func (h *history) snapshot() map[string]map[string]sensorHistory {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]map[string]sensorHistory, len(h.data))
	for ep, byKey := range h.data {
		m := make(map[string]sensorHistory, len(byKey))
		for key, sh := range byKey {
			samples := make([]Sample, len(sh.Samples))
			copy(samples, sh.Samples)
			m[key] = sensorHistory{Name: sh.Name, Samples: samples}
		}
		out[ep] = m
	}
	return out
}
