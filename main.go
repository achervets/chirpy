package main

import (
	"chirpy/internal/auth"
	"chirpy/internal/database"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	// load environment variables
	godotenv.Load()

	// open database
	dbURL := os.Getenv("DB_URL")
	dbPlatform := os.Getenv("PLATFORM")
	dbSecret := os.Getenv("SECRET")
	dbPolka := os.Getenv("POLKA_KEY")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	dbQueries := database.New(db)

	// variables
	apiCfg := apiConfig{
		db:       dbQueries,
		platform: dbPlatform,
		secret:   dbSecret,
		polka:    dbPolka,
	}

	// setting up multiplex
	mux := http.NewServeMux()
	mux.Handle("/app/", http.StripPrefix("/app", apiCfg.middlewareMetrics(http.FileServer(http.Dir(".")))))
	mux.HandleFunc("POST /api/users", apiCfg.userHandler)
	mux.HandleFunc("PUT /api/users", apiCfg.updateUserHandler)
	mux.HandleFunc("POST /api/login", apiCfg.loginHandler)
	mux.HandleFunc("POST /api/refresh", apiCfg.refreshHandler)
	mux.HandleFunc("POST /api/revoke", apiCfg.revokeHandler)
	mux.HandleFunc("POST /api/polka/webhooks", apiCfg.webhookHandler)
	mux.HandleFunc("GET /api/chirps", apiCfg.getChirpHandler)
	mux.HandleFunc("GET /api/chirps/{chirpID}", apiCfg.chirpByIDHandler)
	mux.HandleFunc("POST /api/chirps", apiCfg.chirpHandler)
	mux.HandleFunc("DELETE /api/chirps/{chirpID}", apiCfg.chirpDeleteHandler)
	mux.HandleFunc("GET /api/healthz", endpointHandler)
	mux.HandleFunc("GET /admin/metrics", apiCfg.hitHandler)
	mux.HandleFunc("POST /admin/reset", apiCfg.resetHandler)

	// setting up server
	server := &http.Server{}
	server.Handler = mux
	server.Addr = ":8080"
	server.ListenAndServe()
}

// handlers

func (cfg *apiConfig) userHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		log.Printf("Error decoding parameters: %v", err)
		w.WriteHeader(500)
		return
	}

	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		log.Printf("Error hashing password: %v", err)
		return
	}
	dbUser, err := cfg.db.CreateUser(r.Context(), database.CreateUserParams{
		Email:          params.Email,
		HashedPassword: hashedPassword,
	})
	if err != nil {
		log.Printf("Error creating user: %v", err)
		w.WriteHeader(500)
		return
	}

	user := User{
		ID:          dbUser.ID,
		CreatedAt:   dbUser.CreatedAt,
		UpdatedAt:   dbUser.UpdatedAt,
		Email:       dbUser.Email,
		IsChirpyRed: dbUser.IsChirpyRed,
	}
	respondJSON(w, 201, user)

}

func (cfg *apiConfig) updateUserHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	type response struct {
		User
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Couldn't find JWT")
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Couldn't validate JWT")
		return
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Couldn't decode parameters")
		return
	}

	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Couldn't hash password")
		return
	}

	user, err := cfg.db.UpdateUser(r.Context(), database.UpdateUserParams{
		ID:             userID,
		Email:          params.Email,
		HashedPassword: hashedPassword,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Couldn't update user")
		return
	}

	respondJSON(w, http.StatusOK, response{
		User: User{
			ID:          user.ID,
			CreatedAt:   user.CreatedAt,
			UpdatedAt:   user.UpdatedAt,
			Email:       user.Email,
			IsChirpyRed: user.IsChirpyRed,
		},
	})
}

func (cfg *apiConfig) loginHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		log.Printf("Error decoding params: %v", err)
		return
	}

	dbUser, err := cfg.db.GetUserByEmail(r.Context(), params.Email)
	if err != nil {
		log.Printf("Error creating user: %v", err)
		w.WriteHeader(500)
		return
	}

	match, err := auth.CheckPasswordHash(params.Password, dbUser.HashedPassword)
	if err != nil || !match {
		log.Printf("Incorrect Email or Password")
		w.WriteHeader(401)
		return
	}

	accessToken, err := auth.MakeJWT(
		dbUser.ID,
		cfg.secret,
		time.Hour,
	)
	if err != nil {
		log.Printf("Error creating access token: %v", err)
		return
	}

	refreshToken := auth.MakeRefreshToken()

	_, err = cfg.db.CreateRefreshToken(r.Context(), database.CreateRefreshTokenParams{
		UserID:    dbUser.ID,
		Token:     refreshToken,
		ExpiresAt: time.Now().UTC().Add(time.Hour * 24 * 60),
	})
	if err != nil {
		log.Printf("Error making web token: %v", err)
		return
	}

	user := User{
		ID:           dbUser.ID,
		CreatedAt:    dbUser.CreatedAt,
		UpdatedAt:    dbUser.UpdatedAt,
		Email:        dbUser.Email,
		Token:        accessToken,
		RefreshToken: refreshToken,
		IsChirpyRed:  dbUser.IsChirpyRed,
	}
	respondJSON(w, 200, user)
}

func (cfg *apiConfig) refreshHandler(w http.ResponseWriter, r *http.Request) {
	type response struct {
		Token string `json:"token"`
	}

	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Couldn't find token")
		return
	}

	user, err := cfg.db.GetUserFromRefreshToken(r.Context(), refreshToken)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Couldn't get user for refresh token")
		return
	}

	accessToken, err := auth.MakeJWT(
		user.ID,
		cfg.secret,
		time.Hour,
	)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Couldn't validate token")
		return
	}

	respondJSON(w, http.StatusOK, response{
		Token: accessToken,
	})
}

func (cfg *apiConfig) revokeHandler(w http.ResponseWriter, r *http.Request) {
	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Couldn't find token")
		return
	}

	_, err = cfg.db.RevokeRefreshToken(r.Context(), refreshToken)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Couldn't revoke session")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) webhookHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Event string `json:"event"`
		Data  struct {
			UserID uuid.UUID `json:"user_id"`
		}
	}

	apiKey, err := auth.GetAPIKey(r.Header)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "Error finding key")
		return
	}
	if apiKey != cfg.polka {
		respondError(w, http.StatusUnauthorized, "Invalid API Key")
		return
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Error decoding parameters")
		return
	}

	if params.Event != "user.upgraded" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	_, err = cfg.db.UpgradeToChirpyRed(r.Context(), params.Data.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondError(w, http.StatusNotFound, "Error finding user")
			return
		}
		respondError(w, http.StatusInternalServerError, "Error updating user")
		return
	}

	w.WriteHeader(http.StatusNoContent)

}

func (cfg *apiConfig) chirpHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Body string `json:"body"`
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		log.Printf("Error location web token: %v", err)
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		log.Printf("Error validating web token: %v", err)
		w.WriteHeader(401)
		return
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		log.Printf("Error decoding parameters: %s", err)
		w.WriteHeader(500)
		return
	}

	if len(params.Body) > 140 {
		respondError(w, 400, "Chirp is too long")
		return
	}

	arg := database.CreateChirpParams{
		Body:   cleanChirp(params.Body),
		UserID: userID,
	}
	dbChirp, err := cfg.db.CreateChirp(r.Context(), arg)
	if err != nil {
		log.Printf("Error creating chirp: %v", err)
		w.WriteHeader(500)
		return
	}
	chirp := Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		UserID:    dbChirp.UserID,
	}
	respondJSON(w, 201, chirp)
}

func (cfg *apiConfig) chirpDeleteHandler(w http.ResponseWriter, r *http.Request) {
	chirpIDString := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDString)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid chirp ID")
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "JWT Not Found")
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "JWT Not Valid")
		return
	}

	dbChirp, err := cfg.db.GetChirpByID(r.Context(), chirpID)
	if err != nil {
		respondError(w, http.StatusNotFound, "Chirp Not Found")
		return
	}
	if dbChirp.UserID != userID {
		respondError(w, http.StatusForbidden, "Unauthorized Delete")
		return
	}

	err = cfg.db.DeleteChirp(r.Context(), chirpID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Delete Failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)

}

func (cfg *apiConfig) getChirpHandler(w http.ResponseWriter, r *http.Request) {
	chirps, err := cfg.db.GetChirps(r.Context())
	if err != nil {
		log.Printf("Error retrieving chirps: %v", err)
		w.WriteHeader(500)
		return
	}
	newChirps := make([]Chirp, 0, len(chirps))
	for _, chirp := range chirps {
		fixedChirp := Chirp{
			ID:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserID:    chirp.UserID,
		}
		newChirps = append(newChirps, fixedChirp)
	}
	respondJSON(w, 200, newChirps)
}

func (cfg *apiConfig) chirpByIDHandler(w http.ResponseWriter, r *http.Request) {
	pathName := r.PathValue("chirpID")
	id, err := uuid.Parse(pathName)
	if err != nil {
		log.Printf("Invalid UUID: %v", err)
		w.WriteHeader(400)
		return
	}
	dbChirp, err := cfg.db.GetChirpByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("ChirpID not found")
		w.WriteHeader(404)
		return
	}
	if err != nil {
		log.Printf("Error querying db: %v", err)
		w.WriteHeader(500)
		return
	}
	chirp := Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		UserID:    dbChirp.UserID,
	}
	respondJSON(w, 200, chirp)
}

func endpointHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
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
	if cfg.platform != "dev" {
		respondJSON(w, 403, "Forbidden")
		return
	}
	err := cfg.db.Reset(r.Context())
	if err != nil {
		log.Printf("Error deleting table: %v", err)
		w.WriteHeader(500)
		return
	}
	cfg.serverHits.Store(0)
}

// structs and inherited functions
type apiConfig struct {
	serverHits atomic.Int32
	db         *database.Queries
	platform   string
	secret     string
	polka      string
}

type User struct {
	ID           uuid.UUID `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Email        string    `json:"email"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token"`
	IsChirpyRed  bool      `json:"is_chirpy_red"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	UserID    uuid.UUID `json:"user_id"`
	Password  string    `json:"-"`
}

func (cfg *apiConfig) middlewareMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.serverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}
