package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

func main() {
	// variables
	var apiCfg apiConfig

	// setting up multiplex
	mux := http.NewServeMux()
	mux.Handle("/app/", http.StripPrefix("/app", apiCfg.middlewareMetrics(http.FileServer(http.Dir(".")))))
	mux.HandleFunc("GET /healthz", endpointHandler)
	mux.HandleFunc("GET /metrics", apiCfg.hitHandler)
	mux.HandleFunc("POST /reset", apiCfg.resetHandler)

	// setting up server
	server := &http.Server{}
	server.Handler = mux
	server.Addr = ":8080"
	server.ListenAndServe()
}

// handlers

func endpointHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
}

func (cfg *apiConfig) hitHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hits: %d", cfg.serverHits.Load())
}

func (cfg *apiConfig) resetHandler(w http.ResponseWriter, r *http.Request) {
	cfg.serverHits.Store(0)
}

// structs and inherited functions
type apiConfig struct {
	serverHits atomic.Int32
}

func (cfg *apiConfig) middlewareMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.serverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}
