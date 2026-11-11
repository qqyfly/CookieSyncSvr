package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ChromeCookie matches Google Chrome Extension's chrome.cookies.Cookie interface 1:1.
// See CookieSyncChromeExtension/src/cookies/cookies.ts.
type ChromeCookie struct {
	Domain         string   `json:"domain"`
	ExpirationDate *float64 `json:"expirationDate,omitempty"` // Seconds since UNIX epoch (nil for session cookies)
	HostOnly       bool     `json:"hostOnly"`
	HttpOnly       bool     `json:"httpOnly"`
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	SameSite       string   `json:"sameSite"` // "no_restriction", "lax", "strict", "unspecified"
	Secure         bool     `json:"secure"`
	Session        bool     `json:"session"`
	StoreID        string   `json:"storeId,omitempty"`
	Value          string   `json:"value"`
}

// SiteCookies matches CookieSyncChromeExtension's SiteCookies interface in src/cookies/cookies.ts:
//
//	export interface SiteCookies {
//	  origin: string;
//	  cookies: chrome.cookies.Cookie[];
//	}
type SiteCookies struct {
	Origin    string         `json:"origin"`
	UpdatedAt int64          `json:"updatedAt"` // Unix timestamp in milliseconds
	Cookies   []ChromeCookie `json:"cookies"`
}

// SiteSummary matches CookieSyncChromeExtension's Site interface in src/sites/site.ts
// (`{ origin: string, addedAt: number }`) plus server-side status fields.
type SiteSummary struct {
	Origin            string   `json:"origin"`
	AddedAt           int64    `json:"addedAt"`           // Matches Site.addedAt in src/sites/site.ts
	UpdatedAt         int64    `json:"updatedAt"`         // Latest push timestamp (ms)
	CookieCount       int      `json:"cookieCount"`       // Number of cookies stored
	Expired           bool     `json:"expired"`           // True if any persistent cookie is expired
	MinExpirationDate *float64 `json:"minExpirationDate"` // Earliest expirationDate (Unix seconds)
}

var (
	schemeRegex     = regexp.MustCompile(`(?i)^[a-z][a-z\d+.-]*://`)
	bareSchemeRegex = regexp.MustCompile(`(?i)^[a-z][a-z\d+.-]*:`)
	hostPortRegex   = regexp.MustCompile(`(?i)^[a-z][a-z\d.-]*:\d+(/|$)`)
)

// NormalizeSiteUrl replicates CookieSyncChromeExtension/src/sites/site.ts `normalizeSiteUrl(input)`
// so both the Chrome Extension, the Go server, and the Python client resolve any domain or URL
// (e.g. "example.com", "https://example.com/login") to the exact same canonical origin ("https://example.com").
func NormalizeSiteUrl(input string) (string, error) {
	candidate := strings.TrimSpace(input)
	if candidate == "" {
		return "", errors.New("enter a website address")
	}

	hasScheme := schemeRegex.MatchString(candidate) ||
		(bareSchemeRegex.MatchString(candidate) && !hostPortRegex.MatchString(candidate))

	rawURL := candidate
	if !hasScheme {
		rawURL = "https://" + candidate
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("enter a valid website address, such as https://example.com")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("only HTTP and HTTPS websites can be added")
	}
	if parsed.User != nil {
		return "", errors.New("website addresses must not contain a username or password")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.Contains(hostname, " ") {
		return "", errors.New("enter a valid website hostname")
	}

	return fmt.Sprintf("%s://%s", scheme, strings.ToLower(parsed.Host)), nil
}

// NormalizeUnixSeconds normalizes an expirationDate to Unix epoch seconds.
// Chrome's chrome.cookies.Cookie.expirationDate is in seconds, but if a client
// accidentally passes milliseconds (> 1e11), we normalize to seconds.
func NormalizeUnixSeconds(exp float64) float64 {
	if exp > 1e11 {
		return exp / 1000.0
	}
	return exp
}

// MinExpirationSeconds computes the earliest expirationDate (in Unix seconds)
// among all non-session cookies in the list. Returns nil if all cookies are session cookies.
func MinExpirationSeconds(cookies []ChromeCookie) *float64 {
	var minExp *float64
	for _, c := range cookies {
		if c.Session || c.ExpirationDate == nil {
			continue
		}
		sec := NormalizeUnixSeconds(*c.ExpirationDate)
		if minExp == nil || sec < *minExp {
			val := sec
			minExp = &val
		}
	}
	return minExp
}

// CheckExpired inspects all cookies in the snapshot against `now` and returns
// the list of expired cookie names and whether the site snapshot is expired.
func CheckExpired(cookies []ChromeCookie, now time.Time) (expiredNames []string, isExpired bool) {
	nowSec := float64(now.Unix())
	for _, c := range cookies {
		if c.Session || c.ExpirationDate == nil {
			continue
		}
		expSec := NormalizeUnixSeconds(*c.ExpirationDate)
		if expSec <= nowSec {
			expiredNames = append(expiredNames, c.Name)
		}
	}
	return expiredNames, len(expiredNames) > 0
}
