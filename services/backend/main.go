package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Response is the JSON shape returned by the backend.
type Response struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Message string `json:"message"`
}

var (
	serviceName = "backend"
	version     = getEnv("APP_VERSION", "v1")
	port        = getEnv("PORT", "8080")

	// Chaos toggles – can be overridden at runtime via env vars.
	// FAILURE_RATE: 0.0–1.0 probability of returning a 500 error.
	// DELAY_MS: artificial delay in milliseconds added to every request.
	failureRate = parseFloat(getEnv("FAILURE_RATE", "0"))
	delayMS     = parseInt(getEnv("DELAY_MS", "0"))
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("[%s] %s %s from %s", serviceName, r.Method, r.URL.Path, r.RemoteAddr)
		next(w, r)
		log.Printf("[%s] %s %s completed in %s", serviceName, r.Method, r.URL.Path, time.Since(start))
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","service":%q,"version":%q}`, serviceName, version)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	// Simulate configurable delay for chaos/latency demos.
	if delayMS > 0 {
		time.Sleep(time.Duration(delayMS) * time.Millisecond)
	}

	// Simulate configurable failure rate for circuit-breaking / chaos demos.
	if failureRate > 0 && rand.Float64() < failureRate {
		http.Error(w, fmt.Sprintf(`{"error":"simulated failure","service":%q,"version":%q}`, serviceName, version), http.StatusInternalServerError)
		return
	}

	resp := Response{
		Service: serviceName,
		Version: version,
		Message: fmt.Sprintf("Hello from %s %s!", serviceName, version),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[%s] error encoding response: %v", serviceName, err)
	}
}

func main() {
	log.Printf("Starting %s version=%s port=%s failure_rate=%.2f delay_ms=%d",
		serviceName, version, port, failureRate, delayMS)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", loggingMiddleware(healthHandler))
	mux.HandleFunc("/", loggingMiddleware(rootHandler))

	addr := ":" + port
	log.Printf("[%s] Listening on %s", serviceName, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[%s] server error: %v", serviceName, err)
	}
}
