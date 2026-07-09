package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// Response is the JSON shape returned by the api service.
type Response struct {
	Service string          `json:"service"`
	Version string          `json:"version"`
	Message string          `json:"message"`
	Backend json.RawMessage `json:"backend,omitempty"`
	Error   string          `json:"error,omitempty"`
}

var (
	serviceName = "api"
	version     = getEnv("APP_VERSION", "v1")
	port        = getEnv("PORT", "8080")
	backendURL  = getEnv("BACKEND_URL", "http://backend:8080")
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	resp := Response{
		Service: serviceName,
		Version: version,
		Message: fmt.Sprintf("Hello from %s %s!", serviceName, version),
	}

	// Call backend and attach its response.
	backendResp, err := callBackend()
	if err != nil {
		resp.Error = fmt.Sprintf("backend call failed: %v", err)
		log.Printf("[%s] backend error: %v", serviceName, err)
	} else {
		resp.Backend = backendResp
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[%s] error encoding response: %v", serviceName, err)
	}
}

// callBackend fetches the backend root endpoint and returns the raw JSON body.
func callBackend() (json.RawMessage, error) {
	url := backendURL + "/"
	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned status %d: %s", res.StatusCode, string(body))
	}

	return json.RawMessage(body), nil
}

func main() {
	log.Printf("Starting %s version=%s port=%s backend=%s",
		serviceName, version, port, backendURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", loggingMiddleware(healthHandler))
	mux.HandleFunc("/", loggingMiddleware(rootHandler))

	addr := ":" + port
	log.Printf("[%s] Listening on %s", serviceName, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[%s] server error: %v", serviceName, err)
	}
}
