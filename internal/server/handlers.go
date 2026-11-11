package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/qqyfly/CookieSyncSvr/internal/config"
	"github.com/qqyfly/CookieSyncSvr/internal/model"
	"github.com/qqyfly/CookieSyncSvr/internal/mqtt"
	"github.com/qqyfly/CookieSyncSvr/internal/repository"
)

const maxRequestBodyBytes = 4 * 1024 * 1024 // 4 MB limit for cookie payloads

// Server bundles HTTP routing, storage repository, and optional MQTT publisher.
type Server struct {
	cfg       *config.Config
	repo      repository.SiteCookieRepository
	publisher *mqtt.Publisher
	logger    *slog.Logger
	mux       http.Handler
}

// New creates and configures the HTTP Server.
func New(cfg *config.Config, repo repository.SiteCookieRepository, publisher *mqtt.Publisher, logger *slog.Logger) *Server {
	s := &Server{
		cfg:       cfg,
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
	s.mux = s.buildRouter()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) buildRouter() http.Handler {
	mux := http.NewServeMux()

	// Public health check
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Protected API v1 routes
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /api/v1/cookies", s.handlePushCookies)
	apiMux.HandleFunc("PUT /api/v1/cookies", s.handlePushCookies)
	apiMux.HandleFunc("GET /api/v1/cookies", s.handlePullCookies)
	apiMux.HandleFunc("DELETE /api/v1/cookies", s.handleDeleteCookies)
	apiMux.HandleFunc("GET /api/v1/sites", s.handleListSites)

	protected := AuthMiddleware(s.cfg.APIKey)(apiMux)
	mux.Handle("/api/", protected)

	// Wrap with CORS and RequestLogger
	var handler http.Handler = mux
	handler = CORSMiddleware(s.cfg.AllowedOrigins)(handler)
	handler = RequestLogger(s.logger)(handler)
	return handler
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"status":  "unhealthy",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"status":   "healthy",
		"dbDriver": s.cfg.DBDriver,
	})
}

// handlePushCookies accepts cookie snapshots pushed from the Google Chrome Extension.
//
// Supported identical formats from CookieSyncChromeExtension:
//  1. Single SiteCookies object:
//     { "origin": "https://example.com", "cookies": [ ... ] }
//     (also accepts "site" as an alias for "origin")
//  2. Array of SiteCookies (direct output of readSiteListCookies() in src/cookies/cookies.ts):
//     [ { "origin": "https://example.com", "cookies": [ ... ] } ]
//  3. Wrapped message response from background/index.ts (`READ_SITE_LIST_COOKIES`):
//     { "results": [ { "origin": "https://example.com", "cookies": [ ... ] } ] }
//
// Conflict resolution: If multiple Chrome Extensions push cookies for the same origin,
// the server uses the latest push and completely overwrites the old cookie snapshot.
func (s *Server) handlePushCookies(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "INVALID_BODY",
			"message": "Failed to read request body",
		})
		return
	}

	items, err := parsePushPayload(body, r.URL.Query().Get("origin"), r.URL.Query().Get("site"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "INVALID_PAYLOAD",
			"message": err.Error(),
		})
		return
	}

	nowMs := time.Now().UnixMilli()
	savedSites := make([]map[string]any, 0, len(items))

	for _, item := range items {
		normOrigin, err := model.NormalizeSiteUrl(item.Origin)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":      false,
				"error":   "INVALID_ORIGIN",
				"message": fmt.Sprintf("invalid origin %q: %v", item.Origin, err),
			})
			return
		}

		item.Origin = normOrigin
		item.UpdatedAt = nowMs
		if item.Cookies == nil {
			item.Cookies = []model.ChromeCookie{}
		}

		// Atomically overwrite the old snapshot for this origin with the latest push
		if err := s.repo.UpsertSiteCookies(r.Context(), item); err != nil {
			s.logger.Error("failed to upsert site cookies", "origin", normOrigin, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":      false,
				"error":   "STORAGE_ERROR",
				"message": "Failed to store cookies",
			})
			return
		}

		if s.publisher != nil {
			s.publisher.PublishUpdate(mqtt.UpdateEvent{
				Origin:      normOrigin,
				CookieCount: len(item.Cookies),
				UpdatedAt:   nowMs,
			})
		}

		expiredNames, isExpired := model.CheckExpired(item.Cookies, time.Now())
		savedSites = append(savedSites, map[string]any{
			"origin":         normOrigin,
			"cookieCount":    len(item.Cookies),
			"updatedAt":      nowMs,
			"expired":        isExpired,
			"expiredCookies": expiredNames,
		})
	}

	if len(savedSites) == 1 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"origin":      savedSites[0]["origin"],
			"cookieCount": savedSites[0]["cookieCount"],
			"updatedAt":   savedSites[0]["updatedAt"],
			"expired":     savedSites[0]["expired"],
			"results":     savedSites,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"updated": len(savedSites),
		"results": savedSites,
	})
}

// handlePullCookies handles `GET /api/v1/cookies?origin=<website>` (or `?site=<website>`)
// called by the Python client program.
//
// Server-Side Expiration Check:
//   - If the stored cookies for the requested website are expired, the server returns
//     HTTP 410 Gone with `{ "ok": false, "error": "COOKIE_EXPIRED", "message": "..." }`.
//   - If valid, returns HTTP 200 OK with the identical SiteCookies format:
//     `{ "ok": true, "origin": "https://example.com", "updatedAt": ..., "cookies": [ ... ] }`.
func (s *Server) handlePullCookies(w http.ResponseWriter, r *http.Request) {
	rawTarget := strings.TrimSpace(r.URL.Query().Get("origin"))
	if rawTarget == "" {
		rawTarget = strings.TrimSpace(r.URL.Query().Get("site"))
	}
	if rawTarget == "" {
		rawTarget = strings.TrimSpace(r.URL.Query().Get("domain"))
	}

	if rawTarget == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "MISSING_ORIGIN",
			"message": "Query parameter 'origin' (or 'site') is required, e.g. /api/v1/cookies?origin=https://example.com",
		})
		return
	}

	normOrigin, err := model.NormalizeSiteUrl(rawTarget)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "INVALID_ORIGIN",
			"message": err.Error(),
		})
		return
	}

	sc, err := s.repo.GetSiteCookies(r.Context(), normOrigin)
	if errors.Is(err, repository.ErrSiteNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":      false,
			"error":   "SITE_NOT_FOUND",
			"origin":  normOrigin,
			"message": fmt.Sprintf("No cookies found for %s", normOrigin),
		})
		return
	}
	if err != nil {
		s.logger.Error("failed to get site cookies", "origin", normOrigin, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"error":   "STORAGE_ERROR",
			"message": "Failed to retrieve cookies from storage",
		})
		return
	}

	if len(sc.Cookies) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":      false,
			"error":   "EMPTY_COOKIES",
			"origin":  normOrigin,
			"message": fmt.Sprintf("Stored cookie list for %s is empty", normOrigin),
		})
		return
	}

	// Mandatory server-side expiration check before returning cookies to the Python client
	expiredNames, isExpired := model.CheckExpired(sc.Cookies, time.Now())
	if isExpired {
		writeJSON(w, http.StatusGone, map[string]any{
			"ok":             false,
			"error":          "COOKIE_EXPIRED",
			"origin":         sc.Origin,
			"updatedAt":      sc.UpdatedAt,
			"expiredCookies": expiredNames,
			"message":        fmt.Sprintf("Cookies for %s have expired (%s). Please push fresh cookies from the Chrome Extension.", sc.Origin, strings.Join(expiredNames, ", ")),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"origin":    sc.Origin,
		"updatedAt": sc.UpdatedAt,
		"cookies":   sc.Cookies,
	})
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.repo.ListSites(r.Context())
	if err != nil {
		s.logger.Error("failed to list sites", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"error":   "STORAGE_ERROR",
			"message": "Failed to list sites",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"sites": sites,
	})
}

func (s *Server) handleDeleteCookies(w http.ResponseWriter, r *http.Request) {
	rawTarget := strings.TrimSpace(r.URL.Query().Get("origin"))
	if rawTarget == "" {
		rawTarget = strings.TrimSpace(r.URL.Query().Get("site"))
	}
	if rawTarget == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "MISSING_ORIGIN",
			"message": "Query parameter 'origin' (or 'site') is required",
		})
		return
	}

	normOrigin, err := model.NormalizeSiteUrl(rawTarget)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"error":   "INVALID_ORIGIN",
			"message": err.Error(),
		})
		return
	}

	deleted, err := s.repo.DeleteSiteCookies(r.Context(), normOrigin)
	if err != nil {
		s.logger.Error("failed to delete site cookies", "origin", normOrigin, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"error":   "STORAGE_ERROR",
			"message": "Failed to delete site cookies",
		})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":      false,
			"error":   "SITE_NOT_FOUND",
			"origin":  normOrigin,
			"message": fmt.Sprintf("No cookies found for %s", normOrigin),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"origin":  normOrigin,
		"deleted": true,
	})
}

// flexibleSiteInput accepts both `origin` (used in CookieSyncChromeExtension) and `site` as field names.
type flexibleSiteInput struct {
	Origin  string               `json:"origin"`
	Site    string               `json:"site"`
	Cookies []model.ChromeCookie `json:"cookies"`
}

func parsePushPayload(raw []byte, queryOrigin, querySite string) ([]model.SiteCookies, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("request body cannot be empty")
	}

	fallbackOrigin := strings.TrimSpace(queryOrigin)
	if fallbackOrigin == "" {
		fallbackOrigin = strings.TrimSpace(querySite)
	}

	// Case 1: JSON Array — either `SiteCookies[]` (from readSiteListCookies) or `ChromeCookie[]` (from readSiteCookies)
	if trimmed[0] == '[' {
		var siteList []flexibleSiteInput
		if err := json.Unmarshal(trimmed, &siteList); err == nil && len(siteList) > 0 && (siteList[0].Origin != "" || siteList[0].Site != "") {
			out := make([]model.SiteCookies, 0, len(siteList))
			for _, item := range siteList {
				origin := item.Origin
				if origin == "" {
					origin = item.Site
				}
				out = append(out, model.SiteCookies{
					Origin:  origin,
					Cookies: item.Cookies,
				})
			}
			return out, nil
		}

		// Check if it's a direct ChromeCookie[] array
		var rawCookies []model.ChromeCookie
		if err := json.Unmarshal(trimmed, &rawCookies); err == nil {
			origin := fallbackOrigin
			if origin == "" && len(rawCookies) > 0 {
				origin = strings.TrimPrefix(rawCookies[0].Domain, ".")
			}
			if origin == "" {
				return nil, errors.New("when pushing a raw cookie array, provide ?origin=https://example.com or a non-empty cookie domain")
			}
			return []model.SiteCookies{
				{
					Origin:  origin,
					Cookies: rawCookies,
				},
			}, nil
		}
		return nil, errors.New("invalid JSON array payload")
	}

	// Case 2: JSON Object — either `{ "origin": "...", "cookies": [...] }` or `{ "results": [ ... ] }`
	var wrapper struct {
		Origin  string               `json:"origin"`
		Site    string               `json:"site"`
		Cookies []model.ChromeCookie `json:"cookies"`
		Results []flexibleSiteInput  `json:"results"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}

	if len(wrapper.Results) > 0 {
		out := make([]model.SiteCookies, 0, len(wrapper.Results))
		for _, item := range wrapper.Results {
			origin := item.Origin
			if origin == "" {
				origin = item.Site
			}
			out = append(out, model.SiteCookies{
				Origin:  origin,
				Cookies: item.Cookies,
			})
		}
		return out, nil
	}

	origin := wrapper.Origin
	if origin == "" {
		origin = wrapper.Site
	}
	if origin == "" {
		origin = fallbackOrigin
	}
	if origin == "" && len(wrapper.Cookies) > 0 {
		origin = strings.TrimPrefix(wrapper.Cookies[0].Domain, ".")
	}
	if origin == "" {
		return nil, errors.New("missing 'origin' field in payload")
	}

	return []model.SiteCookies{
		{
			Origin:  origin,
			Cookies: wrapper.Cookies,
		},
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}
