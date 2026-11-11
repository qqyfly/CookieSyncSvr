# Requirements Specification: CookieSyncSvr

## 1. Overview
`CookieSyncSvr` is a lightweight Go-based HTTP API web service that connects:
1. **Producer (Push)**: Multiple Google Chrome Extension instances (`CookieSyncChromeExtension`) that capture browser cookies via `chrome.cookies.getAll({ url: origin })` in the exact `SiteCookies` format (`{ origin: string, cookies: chrome.cookies.Cookie[] }`) and push them to the server.
2. **Consumer (Pull)**: A **Python client program** that pulls the stored cookies for a website from the server.

Both sides exchange the **exact same data structure** (`SiteCookies`: `origin` + `cookies: chrome.cookies.Cookie[]`) used by `CookieSyncChromeExtension/src/cookies/cookies.ts`.

The service is developed and debugged on **Arch Linux** and deployed to a **Debian VPS** (which already hosts PostgreSQL and MQTT).

---

## 2. Shared Data Format & Business Rules

### 2.1 Identical Data Format (`SiteCookies` in `src/cookies/cookies.ts`)
In `CookieSyncChromeExtension/src/cookies/cookies.ts`, the extension uses:
```ts
export interface SiteCookies {
  origin: string;
  cookies: chrome.cookies.Cookie[];
}
```
And `normalizeSiteUrl(input)` in `src/sites/site.ts` normalizes any website input (e.g. `example.com` or `https://example.com/path`) to its canonical `origin` (`https://example.com`).

Accordingly, `CookieSyncSvr` accepts and returns the exact same structure (supporting single `SiteCookies`, an array `SiteCookies[]` as returned by `readSiteListCookies()`, or `{ results: SiteCookies[] }`):
```json
{
  "origin": "https://example.com",
  "updatedAt": 1731340000000,
  "cookies": [
    {
      "name": "session_id",
      "value": "xyz123",
      "domain": ".example.com",
      "hostOnly": false,
      "path": "/",
      "secure": true,
      "httpOnly": true,
      "sameSite": "lax",
      "session": false,
      "expirationDate": 1762876000,
      "storeId": "0"
    }
  ]
}
```

### 2.2 Multi-Extension Push Policy (Latest Overwrites Old)
- **Requirement**: If multiple Chrome Extension instances push cookies for the same website (`origin`, e.g., `https://example.com`), the server **uses the latest push and completely overwrites the old cookie snapshot** for that origin.
- **Behavior**: Pushing a `SiteCookies` payload for `https://example.com` atomically replaces the entire stored `cookies` array and updates `updatedAt` to the latest timestamp.

### 2.3 Server-Side Expiration Check When Python Client Pulls Cookies
- **Requirement**: When the Python client requests cookies for a website (`GET /api/v1/cookies?origin=https://example.com` or `?site=example.com`), the server **must check if the stored cookies are expired**. If expired, the server **must send a failure message** instead of returning expired cookies.
- **Expiration Rules**:
  1. For each cookie in `cookies`, if `!cookie.session` and `cookie.expirationDate != nil`:
     - The server compares `cookie.expirationDate` (Unix seconds) against `time.Now().Unix()`.
     - If `expirationDate <= now`, that cookie is **expired**.
  2. **If expired**: The server responds with HTTP `410 Gone` and a structured failure payload:
     ```json
     {
       "ok": false,
       "error": "COOKIE_EXPIRED",
       "origin": "https://example.com",
       "message": "Cookies for https://example.com have expired",
       "expiredCookies": ["session_id"],
       "updatedAt": 1731340000000
     }
     ```
  3. **If not found**: The server responds with HTTP `404 Not Found`:
     ```json
     {
       "ok": false,
       "error": "SITE_NOT_FOUND",
       "origin": "https://example.com",
       "message": "No cookies found for https://example.com"
     }
     ```
  4. **If valid (not expired)**: The server responds with HTTP `200 OK` and the exact `SiteCookies` payload (`origin`, `updatedAt`, `cookies`).

---

## 3. Functional Requirements

### FR-1: Push Site Cookies (Single or Batch `SiteCookies`)
- **Endpoint**: `POST /api/v1/cookies`
- **Accepted Payloads**:
  - Single `SiteCookies`: `{ "origin": "https://example.com", "cookies": [...] }`
  - Batch `SiteCookies[]` (direct output of `readSiteListCookies()`): `[ { "origin": "https://example.com", "cookies": [...] } ]`
  - Message response wrapper: `{ "results": [ { "origin": "https://example.com", "cookies": [...] } ] }`
- **Behavior**: Normalizes each `origin` using the exact same rules as `normalizeSiteUrl` in `src/sites/site.ts`, atomically overwrites the previous cookies for that `origin`, updates `updatedAt`, and optionally publishes a notification to MQTT (`cookiesync/updated`).

### FR-2: Pull Site Cookies (Python Client Consumer)
- **Endpoint**: `GET /api/v1/cookies?origin=<url_or_domain>` (also accepts `?site=<url_or_domain>`)
- **Behavior**:
  1. Normalizes `<url_or_domain>` to canonical origin (e.g., `example.com` -> `https://example.com`).
  2. Loads the stored `SiteCookies` snapshot.
  3. Runs server-side expiration check on all cookies in the snapshot.
  4. Returns `410 Gone` failure JSON if expired, `404 Not Found` if missing, or `200 OK` with `SiteCookies` JSON if valid.

### FR-3: List Stored Sites & Status
- **Endpoint**: `GET /api/v1/sites`
- **Behavior**: Returns all stored origins (`origin`, `addedAt`/`updatedAt`, `cookieCount`, `expired` boolean, `minExpirationDate`) compatible with `Site` (`{ origin, addedAt }`) in `src/sites/site.ts`.

### FR-4: Delete Site Cookies
- **Endpoint**: `DELETE /api/v1/cookies?origin=<url_or_domain>`
- **Behavior**: Deletes the stored cookie snapshot for that origin.

### FR-5: Health Check
- **Endpoint**: `GET /healthz`
- **Behavior**: Returns `200 OK` with storage health status.

---

## 4. Non-Functional Requirements

### NFR-1: Security & CORS
- **API Key Authentication**: Optional/configurable via `API_KEY` environment variable (`X-API-Key` or `Authorization: Bearer <token>`).
- **CORS**: Allows `chrome-extension://*` origins and standard HTTP headers.
- **Encryption at Rest**: Optional AES-256-GCM encryption for stored cookie JSON via `ENCRYPTION_KEY`.

### NFR-2: Cross-Distribution Build & Packaging
- **Debug OS**: Arch Linux (`x86_64`).
- **Target Production OS**: Debian VPS (`x86_64`).
- **Deliverables**:
  1. **Original Static Executable**: `dist/cookiesyncsvr` (`CGO_ENABLED=0`, zero dynamic `glibc` links).
  2. **Packaging Way 1 — Arch Linux (`PKGBUILD`)**: Builds `cookiesyncsvr-<ver>-1-x86_64.pkg.tar.zst` via `makepkg`.
  3. **Packaging Way 2 — Debian (`.deb`)**: Builds `cookiesyncsvr_<ver>_amd64.deb` using standard `ar` + `tar` (works directly on Arch Linux even without `dpkg-deb` installed!) for installation on Debian VPS via `apt install ./cookiesyncsvr_*.deb`.

---

## 5. Testable Acceptance Criteria

| ID | Criterion | Verification Method |
|---|---|---|
| **AC-1** | **Identical `SiteCookies` Format**: Pushing `{ "origin": "https://example.com", "cookies": [...] }` or `SiteCookies[]` from `readSiteListCookies()` and pulling it back preserves every `chrome.cookies.Cookie` field identically. | Automated unit & integration tests. |
| **AC-2** | **Multi-Extension Overwrite**: Extension A pushes cookies for `https://example.com`; Extension B later pushes new cookies for `https://example.com`. Pulling returns only Extension B's cookies. | Automated test verifying overwrite behavior. |
| **AC-3** | **Expired Cookie Failure Message**: When any persistent cookie for `https://example.com` has `expirationDate <= now`, `GET /api/v1/cookies?origin=https://example.com` returns `410 Gone` with `{"ok": false, "error": "COOKIE_EXPIRED", "message": "..."}`. | Automated test with expired cookie timestamp. |
| **AC-4** | **Origin Normalization**: Both `?origin=https://example.com` and `?origin=example.com` resolve to `https://example.com`, matching `normalizeSiteUrl()` in `src/sites/site.ts`. | Unit test for `NormalizeSiteOrigin`. |
| **AC-5** | **Static Binary**: `file dist/cookiesyncsvr` / `ldd dist/cookiesyncsvr` confirms a statically linked ELF binary. | Build check on Arch Linux. |
| **AC-6** | **Arch & Debian Packages**: Both `.pkg.tar.zst` and `.deb` packages are generated cleanly in `dist/`. | Run `make all` and verify package contents. |
