// shelly-add-on-temperature-debug is a small, stateless web app that gives
// people a safe troubleshooting view of DS18B20 temperature sensors attached
// to Shelly Sensor Add-ons — without exposing the Shelly web UI or password.
package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

//go:embed static/index.html
var indexHTML []byte

type server struct {
	cfg     *Config
	meta    *metaCache
	history *history
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	s := &server{cfg: cfg, meta: newMetaCache(), history: newHistory(cfg.HistorySize)}

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
	mux.HandleFunc(base+"/api/query", s.requireToken(s.handleQuery))
	mux.HandleFunc(base+"/api/history", s.requireToken(s.handleHistory))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	log.Printf("serving %d Shelly endpoint(s) on :%s%s/ (token auth: %v)",
		len(cfg.Endpoints), cfg.Port, base, cfg.Token != "")
	log.Fatal(srv.ListenAndServe())
}

func (s *server) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(indexHTML)
}

// requireToken gates the API with a shared token when DEBUG_TOKEN is set.
// The HTML shell itself is always served; it contains no data.
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
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout+2*time.Second)
	defer cancel()
	results := queryAll(ctx, s.cfg, s.meta)
	s.history.record(results)
	writeJSON(w, http.StatusOK, map[string]any{
		"ts":        time.Now().Unix(),
		"endpoints": results,
	})
}

func (s *server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"size":      s.cfg.HistorySize,
		"endpoints": s.history.snapshot(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
