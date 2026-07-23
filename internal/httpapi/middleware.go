package httpapi

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// withAPIToken is a barrier against foreign clients — it checks the x-api-token
// header against the shared token from config. When no token is configured,
// the gate is disabled.
func (s *Server) withAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("x-api-token")
		// Constant-time comparison to avoid timing attacks.
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "Missing or invalid x-api-token.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRecovery converts a panic in any handler into a logged stack trace and a
// clean JSON 500, instead of letting net/http abruptly close the connection
// (which upstream proxies surface as an opaque 502).
func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "Internal error.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack lets WebSocket upgrades work through the logging wrapper by delegating
// to the underlying ResponseWriter (the net/http server's writer supports it).
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
	}
	return hj.Hijack()
}

// withLogging logs every request (method, path, status, duration).
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}
