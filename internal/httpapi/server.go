// Package httpapi exposes the paseo client over HTTP endpoints. It replaces
// paseo CLI calls: instead of spawning a subprocess, a client calls this API.
package httpapi

import (
	"log/slog"
	"net/http"

	"paseo-api/internal/config"
	"paseo-api/internal/paseo"
)

// Server holds the HTTP layer's dependencies.
type Server struct {
	cfg    config.Config
	client *paseo.Client
	log    *slog.Logger
}

// New creates a server with the given client and configuration.
func New(cfg config.Config, client *paseo.Client, log *slog.Logger) *Server {
	return &Server{cfg: cfg, client: client, log: log}
}

// Handler builds the HTTP router with all endpoints and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public (no token): liveness probe only.
	mux.HandleFunc("GET /health", s.handleHealth)

	// API docs are public so the spec and Swagger UI always load; the token is
	// entered via Swagger UI's "Authorize" button and only guards the API calls.
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPISpec)
	mux.HandleFunc("GET /docs", s.handleDocs)

	// Agent lifecycle.
	mux.Handle("POST /run", s.gate(s.handleRun))
	mux.Handle("POST /agents", s.gate(s.handleRun)) // alias: create + run an agent
	mux.Handle("GET /agents", s.gate(s.handleListAgents))
	mux.Handle("GET /agents/{id}", s.gate(s.handleGetAgent))
	mux.Handle("GET /agents/{id}/logs", s.gate(s.handleAgentLogs))
	mux.Handle("GET /agents/{id}/stream", s.gate(s.handleStreamAgent)) // WebSocket: live events
	mux.Handle("POST /agents/{id}/messages", s.gate(s.handleSendMessage))
	mux.Handle("POST /agents/{id}/stop", s.gate(s.handleStopAgent))
	mux.Handle("POST /agents/{id}/mode", s.gate(s.handleSetMode))
	mux.Handle("POST /agents/{id}/archive", s.gate(s.handleArchiveAgent))
	mux.Handle("DELETE /agents/{id}", s.gate(s.handleDeleteAgent))

	// Providers & daemon introspection.
	mux.Handle("GET /providers", s.gate(s.handleProviders))
	mux.Handle("GET /providers/{provider}/models", s.gate(s.handleProviderModels))
	mux.Handle("GET /daemon/status", s.gate(s.handleDaemonStatus))

	return s.withRecovery(s.withLogging(mux))
}

// gate wraps a handler func with the API-token check.
func (s *Server) gate(h http.HandlerFunc) http.Handler {
	return s.withAPIToken(h)
}
