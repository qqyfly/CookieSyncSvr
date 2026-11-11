package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CORSMiddleware configures CORS headers so Chrome Extensions (`chrome-extension://...`)
// and local development tools can call the API.
func CORSMiddleware(allowedOrigins string) func(http.Handler) http.Handler {
	origins := strings.Split(allowedOrigins, ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqOrigin := r.Header.Get("Origin")
			allowOrigin := "*"

			if allowedOrigins != "*" && reqOrigin != "" {
				allowOrigin = ""
				for _, o := range origins {
					if o == "*" || o == reqOrigin || (strings.HasSuffix(o, "*") && strings.HasPrefix(reqOrigin, strings.TrimSuffix(o, "*"))) {
						allowOrigin = reqOrigin
						break
					}
				}
			} else if reqOrigin != "" {
				allowOrigin = reqOrigin
			}

			if allowOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-API-Key")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AuthMiddleware verifies the API key if one is configured on the server.
// Accepts either `X-API-Key: <key>` or `Authorization: Bearer <key>`.
func AuthMiddleware(expectedKey string) func(http.Handler) http.Handler {
	expectedKey = strings.TrimSpace(expectedKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expectedKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			provided := strings.TrimSpace(r.Header.Get("X-API-Key"))
			if provided == "" {
				authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
				if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
					provided = strings.TrimSpace(authHeader[7:])
				}
			}

			if subtle.ConstantTimeCompare([]byte(provided), []byte(expectedKey)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"ok":      false,
					"error":   "UNAUTHORIZED",
					"message": "Invalid or missing API key",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger logs HTTP method, path, status, and duration without logging sensitive cookie values.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)
			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
