# CookieSyncSvr

A lightweight, statically-compiled Go web service that accepts website cookies pushed from [`CookieSyncChromeExtension`](https://github.com/qqyfly/CookieSyncChromeExtension), stores them in **PostgreSQL** (Debian VPS production) or **SQLite** (Arch Linux local debugging), verifies cookie expiration on the server side, and serves valid cookies to a **Python client**.

- **Requirements Doc**: [`docs/requirements.md`](docs/requirements.md)
- **Architecture Doc**: [`docs/architecture.md`](docs/architecture.md)

---

## Features

1. **100% Identical Data Format (`SiteCookies`)**:
   - Uses the exact same JSON structure (`{ "origin": "https://example.com", "cookies": [ ... ] }`) as `CookieSyncChromeExtension/src/cookies/cookies.ts` (`readSiteCookies` and `readSiteListCookies`).
   - Uses the exact same URL origin normalizer as `CookieSyncChromeExtension/src/sites/site.ts` (`normalizeSiteUrl`).
2. **Multi-Extension Push Overwrite (Latest Wins)**:
   - If multiple Chrome Extension instances push cookies for the same website (`origin`), the server atomically overwrites the old cookie snapshot with the latest push.
3. **Server-Side Expiration Verification on Pull**:
   - When the Python client requests cookies for a website (`GET /api/v1/cookies?origin=example.com`), the server checks every non-session cookie's `expirationDate`.
   - If expired, the server returns `HTTP 410 Gone` with `{ "ok": false, "error": "COOKIE_EXPIRED", "message": "...", "expiredCookies": [...] }`.
4. **Storage & Notification**:
   - **PostgreSQL** (`DB_DRIVER=postgres`) for Debian VPS production.
   - **SQLite** (`DB_DRIVER=sqlite`, pure Go, zero CGO) for local debugging on Arch Linux.
   - **MQTT** (`MQTT_BROKER_URL`) optional update event notification (`cookiesync/updated`).
5. **Cross-Distribution Build & Packaging**:
   - **Original Static Executable**: `dist/cookiesyncsvr` (`CGO_ENABLED=0`, runs on both Arch Linux and Debian VPS without `glibc` conflicts).
   - **Way 1 (Arch Linux)**: `dist/cookiesyncsvr-0.1.0-1-x86_64.pkg.tar.zst` (built via `makepkg`).
   - **Way 2 (Debian VPS)**: `dist/cookiesyncsvr_0.1.0-1_amd64.deb` (installable via `apt` / `dpkg`).

---

## Quick Start (Local Debugging on Arch Linux)

```bash
# Build static binary, run tests, and build both Arch & Debian packages
make all

# Run locally with SQLite (creates ./cookiesync.db automatically)
./dist/cookiesyncsvr -listen :8080 -db-driver sqlite -db-url ./cookiesync.db
```

---

## API Reference

### 1. Push Cookies (From Chrome Extension)
`POST /api/v1/cookies`

Accepts either a single `SiteCookies` object or an array `SiteCookies[]` (direct output of `readSiteListCookies()`):
```json
{
  "origin": "https://example.com",
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
      "expirationDate": 1893456000,
      "storeId": "0"
    }
  ]
}
```

### 2. Pull Cookies (From Python Client)
`GET /api/v1/cookies?origin=https://example.com` (or `?origin=example.com`)

- **Success (`200 OK` - cookies valid)**:
  ```json
  {
    "ok": true,
    "origin": "https://example.com",
    "updatedAt": 1731340000000,
    "cookies": [ ... ]
  }
  ```
- **Failure (`410 Gone` - cookies expired on server)**:
  ```json
  {
    "ok": false,
    "error": "COOKIE_EXPIRED",
    "origin": "https://example.com",
    "expiredCookies": ["session_id"],
    "message": "Cookies for https://example.com have expired (session_id). Please push fresh cookies from the Chrome Extension."
  }
  ```

#### Python Client Example
```python
import requests

SERVER_URL = "http://127.0.0.1:8080"
API_KEY = ""  # Set if API_KEY is configured on server

headers = {"X-API-Key": API_KEY} if API_KEY else {}
resp = requests.get(f"{SERVER_URL}/api/v1/cookies", params={"origin": "example.com"}, headers=headers)
payload = resp.json()

if not resp.ok or not payload.get("ok"):
    raise RuntimeError(f"[{payload.get('error')}] {payload.get('message')}")

session = requests.Session()
for c in payload["cookies"]:
    session.cookies.set(c["name"], c["value"], domain=c["domain"], path=c["path"])
```

### 3. List Synced Sites
`GET /api/v1/sites`

### 4. Delete Site Cookies
`DELETE /api/v1/cookies?origin=https://example.com`

---

## Installing Packages on Arch Linux & Debian VPS

### Way 1: Install on Arch Linux (`.pkg.tar.zst`)
```bash
sudo pacman -U dist/cookiesyncsvr-0.1.0-1-x86_64.pkg.tar.zst
sudo systemctl enable --now cookiesyncsvr
```

### Way 2: Install on Debian VPS (`.deb`)
Copy `dist/cookiesyncsvr_0.1.0-1_amd64.deb` to your Debian VPS:
```bash
sudo apt install ./cookiesyncsvr_0.1.0-1_amd64.deb
sudo nano /etc/cookiesyncsvr/config.env   # Set DB_DRIVER=postgres, DATABASE_URL, API_KEY, etc.
sudo systemctl enable --now cookiesyncsvr
```
