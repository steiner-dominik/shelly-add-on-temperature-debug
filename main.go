// shelly-add-on-temperature-debug is a small, stateless web app that gives
// people a safe troubleshooting view of DS18B20 temperature sensors attached
// to Shelly Sensor Add-ons — without exposing the Shelly web UI or password.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// version is the CalVer release (e.g. "2026.07.17"), injected at build time
// via -ldflags "-X main.version=…".
var version = "dev"

//go:embed static/index.html
var indexHTML []byte

//go:embed locales/*.json
var localeFS embed.FS

type server struct {
	cfg     *Config
	meta    *metaCache
	history *history

	// last-result cache: device queries are rate-limited so an internet-facing
	// page cannot be used to hammer the Shellys. Concurrent and rapid repeat
	// requests share one result.
	queryMu   chan struct{} // buffered(1), used as a mutex that respects ctx
	lastAt    time.Time
	lastReply []byte
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	s := &server{
		cfg: cfg, meta: newMetaCache(), history: newHistory(cfg.HistorySize),
		queryMu: make(chan struct{}, 1),
	}
	indexHTML = bytes.ReplaceAll(indexHTML, []byte("__VERSION__"), []byte(version))
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
	mux.HandleFunc(base+"/locales/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(localeIndex)
	})
	mux.HandleFunc(base+"/locales/{file}", serveLocale)
	mux.HandleFunc(base+"/api/query", s.requireToken(s.handleQuery))
	mux.HandleFunc(base+"/api/history", s.requireToken(s.handleHistory))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	log.Printf("shelly-add-on-temperature-debug %s: serving %d Shelly endpoint(s) on :%s%s/ (token auth: %v)",
		version, len(cfg.Endpoints), cfg.Port, base, cfg.Token != "")
	log.Fatal(srv.ListenAndServe())
}

// securityHeaders sets conservative defaults suitable for an internet-facing page.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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

// requireToken gates the API with a shared token when DEBUG_TOKEN is set.
// The HTML shell and locale files are always served; they contain no data.
func (s *server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := r.Header.Get("X-Debug-Token")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid token"})
				return
			}
		}
		next(w, r)
	}
}

func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET or POST"})
		return
	}

	// Serialize queries; a caller whose request is canceled stops waiting.
	select {
	case s.queryMu <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	defer func() { <-s.queryMu }()

	// Within the rate-limit window, everyone gets the shared cached result.
	if s.lastReply != nil && time.Since(s.lastAt) < s.cfg.MinInterval {
		writeJSONRaw(w, s.lastReply)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout+2*time.Second)
	defer cancel()
	results := queryAll(ctx, s.cfg, s.meta)
	s.history.record(results)

	reply, err := json.Marshal(map[string]any{
		"ts":        time.Now().Unix(),
		"version":   version,
		"endpoints": results,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.lastAt = time.Now()
	s.lastReply = reply
	writeJSONRaw(w, reply)
}

func (s *server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"size":      s.cfg.HistorySize,
		"endpoints": s.history.snapshot(),
	})
}

// --- locales ---

// buildLocaleIndex validates every embedded locale file and returns the
// index the language picker uses: [{"code":"en","name":"English"},…].
func buildLocaleIndex() ([]byte, error) {
	entries, err := fs.Glob(localeFS, "locales/*.json")
	if err != nil {
		return nil, err
	}
	type meta struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	var metas []meta
	for _, p := range entries {
		data, err := localeFS.ReadFile(p)
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
	data, err := localeFS.ReadFile("locales/" + file)
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

func writeJSONRaw(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}
