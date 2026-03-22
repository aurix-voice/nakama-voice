package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// HealthChecker manages an HTTP health check endpoint that reports the
// operational status of all critical subsystems.
type HealthChecker struct {
	server *http.Server

	// mu protects the checks map.
	mu     sync.RWMutex
	checks map[string]HealthCheckFunc
}

// HealthCheckFunc is a function that returns nil if the subsystem is healthy,
// or an error describing the problem.
type HealthCheckFunc func() error

// HealthResponse is the JSON response from the health endpoint.
type HealthResponse struct {
	Status    string            `json:"status"`
	Checks   map[string]string `json:"checks"`
	Timestamp int64            `json:"timestamp"`
}

// NewHealthChecker creates a health check server on the specified port.
// Call Start to begin listening. Call Stop for graceful shutdown.
func NewHealthChecker(port int) *HealthChecker {
	h := &HealthChecker{
		checks: make(map[string]HealthCheckFunc, 8),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/ready", h.handleReady)

	h.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	return h
}

// RegisterCheck adds a named health check function.
func (h *HealthChecker) RegisterCheck(name string, fn HealthCheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = fn
}

// Start begins listening for health check requests in a background goroutine.
// The provided context controls the server lifecycle.
func (h *HealthChecker) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", h.server.Addr)
	if err != nil {
		return fmt.Errorf("health: listen %s: %w", h.server.Addr, err)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Log would go here, but we don't have access to logger in this goroutine.
				// The panic is recovered to prevent server crash.
			}
		}()
		if sErr := h.server.Serve(ln); sErr != nil && sErr != http.ErrServerClosed {
			// Server error is non-fatal; health checks simply won't be available.
		}
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in shutdown goroutine.
			}
		}()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.server.Shutdown(shutdownCtx)
	}()

	return nil
}

// Stop gracefully shuts down the health check server.
func (h *HealthChecker) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.server.Shutdown(ctx)
}

func (h *HealthChecker) handleHealth(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	checks := make(map[string]HealthCheckFunc, len(h.checks))
	for k, v := range h.checks {
		checks[k] = v
	}
	h.mu.RUnlock()

	resp := HealthResponse{
		Status:    "ok",
		Checks:   make(map[string]string, len(checks)),
		Timestamp: time.Now().UnixMilli(),
	}

	for name, fn := range checks {
		if err := fn(); err != nil {
			resp.Status = "degraded"
			resp.Checks[name] = err.Error()
		} else {
			resp.Checks[name] = "ok"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HealthChecker) handleReady(w http.ResponseWriter, r *http.Request) {
	h.handleHealth(w, r)
}