package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/qqyfly/CookieSyncSvr/internal/config"
	"github.com/qqyfly/CookieSyncSvr/internal/crypto"
	"github.com/qqyfly/CookieSyncSvr/internal/model"
	"github.com/qqyfly/CookieSyncSvr/internal/repository"
	"github.com/qqyfly/CookieSyncSvr/internal/server"
)

func setupTestServer(t *testing.T, apiKey, encKey string) (*httptest.Server, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_cookies.db")

	cipher, err := crypto.NewCipher(encKey)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	repo, err := repository.NewSQLiteRepository(dbPath, cipher)
	if err != nil {
		t.Fatalf("NewSQLiteRepository: %v", err)
	}

	cfg := &config.Config{
		ListenAddr:     ":0",
		DBDriver:       "sqlite",
		DatabaseURL:    dbPath,
		APIKey:         apiKey,
		EncryptionKey:  encKey,
		AllowedOrigins: "*",
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, repo, nil, logger)
	ts := httptest.NewServer(srv)

	cleanup := func() {
		ts.Close()
		_ = repo.Close()
	}
	return ts, cleanup
}

// TestIdenticalFormatRoundTrip verifies AC-1: pushing Chrome extension's exact SiteCookies
// structure and pulling it from the Python client preserves every field 1:1.
func TestIdenticalFormatRoundTrip(t *testing.T) {
	ts, cleanup := setupTestServer(t, "secret-token", "my-encryption-passphrase")
	defer cleanup()

	futureExp := float64(time.Now().Add(24 * time.Hour).Unix())
	pushed := model.SiteCookies{
		Origin: "https://example.com",
		Cookies: []model.ChromeCookie{
			{
				Domain:         ".example.com",
				ExpirationDate: &futureExp,
				HostOnly:       false,
				HttpOnly:       true,
				Name:           "session_id",
				Path:           "/",
				SameSite:       "lax",
				Secure:         true,
				Session:        false,
				StoreID:        "0",
				Value:          "abc123xyz",
			},
		},
	}

	body, _ := json.Marshal(pushed)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/cookies", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Pull cookies as the Python client would (using bare domain "example.com" or full origin)
	pullReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/cookies?origin=example.com", nil)
	pullReq.Header.Set("Authorization", "Bearer secret-token")
	pullResp, err := http.DefaultClient.Do(pullReq)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer pullResp.Body.Close()

	if pullResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on pull, got %d", pullResp.StatusCode)
	}

	var pulled struct {
		OK        bool                 `json:"ok"`
		Origin    string               `json:"origin"`
		UpdatedAt int64                `json:"updatedAt"`
		Cookies   []model.ChromeCookie `json:"cookies"`
	}
	if err := json.NewDecoder(pullResp.Body).Decode(&pulled); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !pulled.OK || pulled.Origin != "https://example.com" {
		t.Fatalf("unexpected pull metadata: %+v", pulled)
	}
	if len(pulled.Cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(pulled.Cookies))
	}

	got := pulled.Cookies[0]
	want := pushed.Cookies[0]
	if got.Name != want.Name || got.Value != want.Value || got.Domain != want.Domain ||
		got.Path != want.Path || got.Secure != want.Secure || got.HttpOnly != want.HttpOnly ||
		got.SameSite != want.SameSite || got.Session != want.Session || got.StoreID != want.StoreID ||
		*got.ExpirationDate != *want.ExpirationDate {
		t.Fatalf("cookie mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestMultiExtensionOverwrite verifies AC-2: if multiple Chrome Extensions push cookies
// for the same website, the server uses the latest push and completely overwrites the old one.
func TestMultiExtensionOverwrite(t *testing.T) {
	ts, cleanup := setupTestServer(t, "", "")
	defer cleanup()

	futureExp := float64(time.Now().Add(2 * time.Hour).Unix())

	// Extension 1 pushes 2 cookies for https://github.com
	ext1Push := model.SiteCookies{
		Origin: "https://github.com",
		Cookies: []model.ChromeCookie{
			{Name: "old_cookie_1", Value: "val1", Domain: ".github.com", Path: "/", ExpirationDate: &futureExp},
			{Name: "user_session", Value: "old_session_token", Domain: ".github.com", Path: "/", ExpirationDate: &futureExp},
		},
	}
	b1, _ := json.Marshal(ext1Push)
	res1, err := http.Post(ts.URL+"/api/v1/cookies", "application/json", bytes.NewReader(b1))
	if err != nil || res1.StatusCode != http.StatusOK {
		t.Fatalf("ext1 push failed: %v", err)
	}
	_ = res1.Body.Close()

	// Extension 2 later pushes 1 updated cookie for https://github.com
	ext2Push := model.SiteCookies{
		Origin: "https://github.com",
		Cookies: []model.ChromeCookie{
			{Name: "user_session", Value: "latest_session_token_from_ext2", Domain: ".github.com", Path: "/", ExpirationDate: &futureExp},
		},
	}
	b2, _ := json.Marshal(ext2Push)
	res2, err := http.Post(ts.URL+"/api/v1/cookies", "application/json", bytes.NewReader(b2))
	if err != nil || res2.StatusCode != http.StatusOK {
		t.Fatalf("ext2 push failed: %v", err)
	}
	_ = res2.Body.Close()

	// Python client pulls cookies for https://github.com
	pullRes, err := http.Get(ts.URL + "/api/v1/cookies?origin=https://github.com")
	if err != nil {
		t.Fatalf("pull failed: %v", err)
	}
	defer pullRes.Body.Close()

	var pulled struct {
		OK      bool                 `json:"ok"`
		Cookies []model.ChromeCookie `json:"cookies"`
	}
	_ = json.NewDecoder(pullRes.Body).Decode(&pulled)

	if len(pulled.Cookies) != 1 {
		t.Fatalf("expected old cookies to be overwritten (want 1 cookie, got %d)", len(pulled.Cookies))
	}
	if pulled.Cookies[0].Name != "user_session" || pulled.Cookies[0].Value != "latest_session_token_from_ext2" {
		t.Fatalf("expected latest cookie value from ext2, got %+v", pulled.Cookies[0])
	}
}

// TestExpiredCookieReturnsFailure verifies AC-3: when stored cookies are expired,
// pulling cookies for that website returns HTTP 410 Gone with a failure message.
func TestExpiredCookieReturnsFailure(t *testing.T) {
	ts, cleanup := setupTestServer(t, "", "")
	defer cleanup()

	pastExp := float64(time.Now().Add(-10 * time.Minute).Unix())
	futureExp := float64(time.Now().Add(1 * time.Hour).Unix())

	pushed := model.SiteCookies{
		Origin: "https://expired-example.com",
		Cookies: []model.ChromeCookie{
			{Name: "valid_pref", Value: "dark", Domain: ".expired-example.com", Path: "/", ExpirationDate: &futureExp},
			{Name: "auth_sid", Value: "expired_token", Domain: ".expired-example.com", Path: "/", ExpirationDate: &pastExp},
		},
	}
	body, _ := json.Marshal(pushed)
	res, err := http.Post(ts.URL+"/api/v1/cookies", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("push failed: %v", err)
	}
	_ = res.Body.Close()

	// Python client requests cookies for https://expired-example.com
	pullRes, err := http.Get(ts.URL + "/api/v1/cookies?origin=https://expired-example.com")
	if err != nil {
		t.Fatalf("pull failed: %v", err)
	}
	defer pullRes.Body.Close()

	if pullRes.StatusCode != http.StatusGone {
		t.Fatalf("expected HTTP 410 Gone for expired cookie, got %d", pullRes.StatusCode)
	}

	var errResp struct {
		OK             bool     `json:"ok"`
		Error          string   `json:"error"`
		Origin         string   `json:"origin"`
		Message        string   `json:"message"`
		ExpiredCookies []string `json:"expiredCookies"`
	}
	if err := json.NewDecoder(pullRes.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.OK {
		t.Fatalf("expected ok=false when cookie is expired")
	}
	if errResp.Error != "COOKIE_EXPIRED" {
		t.Fatalf("expected error=COOKIE_EXPIRED, got %q", errResp.Error)
	}
	if len(errResp.ExpiredCookies) != 1 || errResp.ExpiredCookies[0] != "auth_sid" {
		t.Fatalf("expected expiredCookies=[auth_sid], got %v", errResp.ExpiredCookies)
	}
}

// TestAuthAndCORS verifies AC-4: unauthorized requests are rejected and Chrome Extension CORS works.
func TestAuthAndCORS(t *testing.T) {
	ts, cleanup := setupTestServer(t, "top-secret", "")
	defer cleanup()

	// 1. Unauthenticated GET returns 401
	res, err := http.Get(ts.URL + "/api/v1/sites")
	if err != nil {
		t.Fatalf("GET /api/v1/sites: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", res.StatusCode)
	}

	// 2. OPTIONS preflight from Chrome extension returns 204 with CORS headers
	optReq, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/v1/cookies", nil)
	optReq.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	optRes, err := http.DefaultClient.Do(optReq)
	if err != nil {
		t.Fatalf("OPTIONS failed: %v", err)
	}
	_ = optRes.Body.Close()
	if optRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", optRes.StatusCode)
	}
	if got := optRes.Header.Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdefghijklmnop" {
		t.Fatalf("expected Access-Control-Allow-Origin for chrome-extension, got %q", got)
	}
}
