package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

func main() {
	// variables
	var apiCfg apiConfig

	// setting up multiplex
	mux := http.NewServeMux()
	mux.Handle("/app/", http.StripPrefix("/app", apiCfg.middlewareMetrics(http.FileServer(http.Dir(".")))))
	mux.HandleFunc("GET /api/healthz", endpointHandler)
	mux.HandleFunc("POST /api/validate_chirp", validateHandler)
	mux.HandleFunc("GET /admin/metrics", apiCfg.hitHandler)
	mux.HandleFunc("POST /admin/reset", apiCfg.resetHandler)

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

func validateHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Chirp string `json:"body"`
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		log.Printf("Error decoding parameters: %s", err)
		w.WriteHeader(500)
		return
	}

	if len(params.Chirp) > 140 {
		respondError(w, 400, "Chirp is too long")
		return
	}

	type cleanResp struct {
		CleanedBody string `json:"cleaned_body"`
	}
	cleanBody := cleanResp{
		CleanedBody: cleanChirp(params.Chirp),
	}
	respondJSON(w, 200, cleanBody)
}

func respondJSON(w http.ResponseWriter, statusCode int, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error marshalling JSON: %s", err)
		w.WriteHeader(statusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(data)
}

func respondError(w http.ResponseWriter, statusCode int, msg string) {
	type returnError struct {
		Error string `json:"error"`
	}
	errorRes := returnError{
		Error: msg,
	}
	data, err := json.Marshal(errorRes)
	if err != nil {
		log.Printf("Error marshalling JSON: %s", err)
		w.WriteHeader(statusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(data)
}

func cleanChirp(dirtyChirp string) string {
	bad := map[string]struct{}{
		"kerfuffle": {},
		"sharbert":  {},
		"fornax":    {},
	}
	stringSlice := strings.Fields(dirtyChirp)
	for i, s := range stringSlice {
		if _, ok := bad[strings.ToLower(s)]; ok {
			stringSlice[i] = "****"
		}
	}
	return strings.Join(stringSlice, " ")
}

func (cfg *apiConfig) hitHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(fmt.Sprintf("<html><body><h1>Welcome, Chirpy Admin</h1><p>Chirpy has been visited %d times!</p></body></html>", cfg.serverHits.Load())))
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
