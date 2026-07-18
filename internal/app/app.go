// Package app implements the shelly-add-on-temperature-debug server: a
// stateless troubleshooting view of DS18B20 temperature and DHT22 humidity
// sensors attached to Shelly Sensor Add-ons — without exposing the Shelly
// web UI or password.
package app

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steiner-dominik/shelly-add-on-temperature-debug/web"
)

// appVersion is the CalVer release string passed to Run by package main.
var appVersion = "dev"

// indexHTML / swJS are web/static files with __VERSION__ substituted at
// startup (the service worker uses the version as its cache name).
var indexHTML []byte
var swJS []byte

// staticAssets are the fixed-name frontend files served next to the page.
var staticAssets = map[string]string{
	"app.css":              "text/css; charset=utf-8",
	"app.js":               "text/javascript; charset=utf-8",
	"favicon.svg":          "image/svg+xml",
	"manifest.webmanifest": "application/manifest+json",
	"icon-192.png":         "image/png",
	"icon-512.png":         "image/png",
	"icon-maskable.png":    "image/png",
}

type server struct {
	cfg     *Config
	meta    *metaCache
	history *history

	// last-result cache: device queries are rate-limited so an internet-facing
	// page cannot be used to hammer the Shellys. Concurrent and rapid repeat
	// requests (including Prometheus scrapes) share one result.
	queryMu     chan struct{} // buffered(1), used as a mutex that respects ctx
	lastAt      time.Time
	lastResults []EndpointResult

	// per-sensor query cache, same rate-limit contract as the full query
	sensorMu    sync.Mutex
	sensorCache map[string]sensorCacheEntry // "epIdx/sensorKey" -> last result
}

type sensorCacheEntry struct {
	res SensorResult
	at  time.Time
}

// Run loads the configuration, wires up the HTTP server, and blocks forever.
func Run(version string) {
	appVersion = version
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	s := &server{
		cfg: cfg, meta: newMetaCache(), history: newHistory(int64(cfg.HistoryMaxMB) << 20),
		queryMu:     make(chan struct{}, 1),
		sensorCache: map[string]sensorCacheEntry{},
	}
	rawIndex, err := web.FS.ReadFile("static/index.html")
	if err != nil {
		log.Fatalf("embedded index.html missing: %v", err)
	}
	indexHTML = bytes.ReplaceAll(rawIndex, []byte("__VERSION__"), []byte(version))
	rawSW, err := web.FS.ReadFile("static/sw.js")
	if err != nil {
		log.Fatalf("embedded sw.js missing: %v", err)
	}
	swJS = bytes.ReplaceAll(rawSW, []byte("__VERSION__"), []byte(version))
	localeIndex, err := buildLocaleIndex()
	if err != nil {
		log.Fatalf("invalid locale files: %v", err)
	}

	base := cfg.BasePath
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	if base != "" {
		mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, base+"/", http.StatusTemporaryRedirect)
		})
	}
	mux.HandleFunc(base+"/{$}", s.serveIndex)
	if base != "" {
		mux.HandleFunc(base, s.serveIndex) // also without trailing slash
	}
	for name, ctype := range staticAssets {
		data, err := web.FS.ReadFile("static/" + name)
		if err != nil {
			log.Fatalf("embedded asset %s missing: %v", name, err)
		}
		mux.HandleFunc(base+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(data)
		})
	}
	mux.HandleFunc(base+"/sw.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(swJS)
	})
	mux.HandleFunc(base+"/locales/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(localeIndex)
	})
	mux.HandleFunc(base+"/locales/{file}", serveLocale)
	mux.HandleFunc(base+"/api/query", s.requireToken(s.handleQuery))
	mux.HandleFunc(base+"/api/query/sensor", s.requireToken(s.handleQuerySensor))
	mux.HandleFunc(base+"/api/history", s.requireToken(s.handleHistory))
	if cfg.Metrics {
		mux.HandleFunc(base+"/metrics", s.requireToken(s.handleMetrics))
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	// Background polling: the server itself queries the Shellys on a fixed
	// interval, so history exists before anyone opens the page. Goes through
	// queryCached, i.e. shares the rate limit and cache with the UI.
	if cfg.BackgroundPollSec > 0 {
		go func() {
			tick := time.NewTicker(time.Duration(cfg.BackgroundPollSec) * time.Second)
			defer tick.Stop()
			for {
				s.queryCached(context.Background())
				<-tick.C
			}
		}()
	}

	auth := "token auth required"
	if cfg.Token == "" {
		auth = "TOKEN AUTH DISABLED — protect this port at your reverse proxy"
	}
	poll := "no background polling"
	if cfg.BackgroundPollSec > 0 {
		poll = fmt.Sprintf("background polling every %ds", cfg.BackgroundPollSec)
	}
	log.Printf("shelly-add-on-temperature-debug %s: serving %d Shelly endpoint(s) on :%s%s/ (%s, %s)",
		version, len(cfg.Endpoints), cfg.Port, base, auth, poll)
	log.Fatal(srv.ListenAndServe())
}

// securityHeaders sets conservative defaults suitable for an internet-facing page.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *server) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(indexHTML)
}

// requireToken gates the API with the shared token. It is accepted only via
// headers (X-Debug-Token, or Authorization: Bearer for Prometheus and
// friends) — never as a URL parameter, which would leak into proxy and
// access logs and browser history. An explicitly empty DEBUG_TOKEN disables
// the gate (for setups that authenticate at the reverse proxy instead). The
// HTML shell, static assets, and locale files are always served; they
// contain no data.
func (s *server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := r.Header.Get("X-Debug-Token")
			if got == "" {
				if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
					got = strings.TrimPrefix(ah, "Bearer ")
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid token"})
				return
			}
		}
		next(w, r)
	}
}

// queryCached performs a rate-limited device query: within MinInterval of the
// previous query every caller (page button, auto-refresh, wiggle mode,
// Prometheus scrape) shares the cached result. Returns ok=false when the
// request was canceled while waiting its turn.
func (s *server) queryCached(reqCtx context.Context) (results []EndpointResult, at time.Time, ok bool) {
	select {
	case s.queryMu <- struct{}{}:
	case <-reqCtx.Done():
		return nil, time.Time{}, false
	}
	defer func() { <-s.queryMu }()

	if s.lastResults != nil && time.Since(s.lastAt) < s.cfg.MinInterval {
		return s.lastResults, s.lastAt, true
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout+2*time.Second)
	defer cancel()
	s.lastResults = queryAll(ctx, s.cfg, s.meta)
	s.lastAt = time.Now()
	s.history.record(s.lastResults)
	return s.lastResults, s.lastAt, true
}

func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET or POST"})
		return
	}
	results, at, ok := s.queryCached(r.Context())
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ts":                 at.Unix(),
		"version":            appVersion,
		"minIntervalSec":     int(s.cfg.MinInterval / time.Second),
		"autoRefreshSec":     s.cfg.AutoRefreshSec,
		"autoRefreshDefault": s.cfg.AutoRefreshDefault,
		"endpoints":          results,
	})
}

// handleQuerySensor queries one single sensor on one Shelly (its dedicated
// Temperature/Humidity.GetStatus RPC), rate-limited per sensor like the full
// query. The endpoint is addressed by its config index to avoid ambiguity.
func (s *server) handleQuerySensor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET or POST"})
		return
	}
	idx, err := strconv.Atoi(r.URL.Query().Get("ep"))
	if err != nil || idx < 0 || idx >= len(s.cfg.Endpoints) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ep must be a valid endpoint index"})
		return
	}
	key := r.URL.Query().Get("key")
	if _, id, ok := splitComponentKey(key); !ok || id < 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key must be an add-on sensor key like temperature:100"})
		return
	}
	res, at := s.querySensorCached(idx, key)
	writeJSON(w, http.StatusOK, map[string]any{
		"ts":       at.Unix(),
		"endpoint": s.cfg.Endpoints[idx].Name,
		"sensor":   res,
	})
}

// querySensorCached rate-limits per-sensor queries the same way queryCached
// rate-limits full queries: within MinInterval the cached reading is returned.
func (s *server) querySensorCached(idx int, key string) (SensorResult, time.Time) {
	cacheKey := fmt.Sprintf("%d/%s", idx, key)
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	if c, ok := s.sensorCache[cacheKey]; ok && time.Since(c.at) < s.cfg.MinInterval {
		return c.res, c.at
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout+2*time.Second)
	defer cancel()
	ep := s.cfg.Endpoints[idx]
	res := querySensor(ctx, ep, s.cfg.Timeout, s.meta, key)
	at := time.Now()
	s.sensorCache[cacheKey] = sensorCacheEntry{res: res, at: at}
	s.history.recordSensor(ep.Name, res, at)
	return res, at
}

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	results, at, ok := s.queryCached(r.Context())
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(renderMetrics(results, at, appVersion))
}

// handleHistory serves the in-memory buffer. GET returns it — optionally only
// the last ?limit=N samples per sensor; the page charts use that so a large
// HISTORY_SIZE doesn't ship megabytes on every refresh. DELETE clears it.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.history.clear()
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"maxMb":     s.cfg.HistoryMaxMB,
		"endpoints": s.history.snapshot(limit),
	})
}

// --- locales ---

// buildLocaleIndex validates every embedded locale file and returns the
// index the language picker uses: [{"code":"en","name":"English"},…].
func buildLocaleIndex() ([]byte, error) {
	entries, err := fs.Glob(web.FS, "locales/*.json")
	if err != nil {
		return nil, err
	}
	type meta struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	var metas []meta
	for _, p := range entries {
		data, err := web.FS.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var l struct {
			Meta meta `json:"meta"`
		}
		if err := json.Unmarshal(data, &l); err != nil {
			return nil, err
		}
		if l.Meta.Code == "" || l.Meta.Name == "" || l.Meta.Code+".json" != path.Base(p) {
			return nil, fmt.Errorf("locale %s: meta.code must match the filename and meta.name must be set", p)
		}
		metas = append(metas, l.Meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Code < metas[j].Code })
	return json.Marshal(metas)
}

func serveLocale(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !strings.HasSuffix(file, ".json") || strings.Contains(file, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := web.FS.ReadFile("locales/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
