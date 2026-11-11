package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/qqyfly/CookieSyncSvr/internal/crypto"
	"github.com/qqyfly/CookieSyncSvr/internal/model"
)

// SQLiteRepository implements SiteCookieRepository using pure-Go SQLite (modernc.org/sqlite).
type SQLiteRepository struct {
	db     *sql.DB
	cipher *crypto.Cipher
}

// NewSQLiteRepository opens or creates a SQLite database at dbPath and initializes the schema.
func NewSQLiteRepository(dbPath string, cipher *crypto.Cipher) (*SQLiteRepository, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)

	repo := &SQLiteRepository{
		db:     db,
		cipher: cipher,
	}
	if err := repo.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repo, nil
}

func (r *SQLiteRepository) initSchema(ctx context.Context) error {
	const schema = `
	PRAGMA journal_mode=WAL;
	CREATE TABLE IF NOT EXISTS site_cookies (
		origin          TEXT PRIMARY KEY,
		cookies_data    TEXT NOT NULL,
		cookie_count    INTEGER NOT NULL DEFAULT 0,
		min_expires_at  REAL,
		updated_at      INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_site_cookies_updated ON site_cookies (updated_at DESC);
	`
	_, err := r.db.ExecContext(ctx, schema)
	return err
}

// UpsertSiteCookies atomically overwrites the old cookie snapshot for sc.Origin with the latest one.
func (r *SQLiteRepository) UpsertSiteCookies(ctx context.Context, sc model.SiteCookies) error {
	rawJSON, err := json.Marshal(sc.Cookies)
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}
	storedData, err := r.cipher.Encrypt(string(rawJSON))
	if err != nil {
		return fmt.Errorf("encrypt cookies: %w", err)
	}

	minExp := model.MinExpirationSeconds(sc.Cookies)
	if sc.UpdatedAt <= 0 {
		sc.UpdatedAt = time.Now().UnixMilli()
	}

	const query = `
	INSERT INTO site_cookies (origin, cookies_data, cookie_count, min_expires_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(origin) DO UPDATE SET
		cookies_data   = excluded.cookies_data,
		cookie_count   = excluded.cookie_count,
		min_expires_at = excluded.min_expires_at,
		updated_at     = excluded.updated_at;
	`
	_, err = r.db.ExecContext(ctx, query, sc.Origin, storedData, len(sc.Cookies), minExp, sc.UpdatedAt)
	return err
}

// GetSiteCookies retrieves the stored cookie snapshot for origin.
func (r *SQLiteRepository) GetSiteCookies(ctx context.Context, origin string) (*model.SiteCookies, error) {
	const query = `
	SELECT origin, cookies_data, updated_at
	FROM site_cookies
	WHERE origin = ?;
	`
	var (
		storedOrigin string
		storedData   string
		updatedAt    int64
	)
	err := r.db.QueryRowContext(ctx, query, origin).Scan(&storedOrigin, &storedData, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}

	plainJSON, err := r.cipher.Decrypt(storedData)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookies: %w", err)
	}

	var cookies []model.ChromeCookie
	if err := json.Unmarshal([]byte(plainJSON), &cookies); err != nil {
		return nil, fmt.Errorf("unmarshal cookies: %w", err)
	}
	if cookies == nil {
		cookies = []model.ChromeCookie{}
	}

	return &model.SiteCookies{
		Origin:    storedOrigin,
		UpdatedAt: updatedAt,
		Cookies:   cookies,
	}, nil
}

// ListSites returns all stored sites ordered newest first.
func (r *SQLiteRepository) ListSites(ctx context.Context) ([]model.SiteSummary, error) {
	const query = `
	SELECT origin, cookie_count, min_expires_at, updated_at
	FROM site_cookies
	ORDER BY updated_at DESC, origin ASC;
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nowSec := float64(time.Now().Unix())
	var sites []model.SiteSummary
	for rows.Next() {
		var (
			origin      string
			cookieCount int
			minExpNull  sql.NullFloat64
			updatedAt   int64
		)
		if err := rows.Scan(&origin, &cookieCount, &minExpNull, &updatedAt); err != nil {
			return nil, err
		}
		var minExp *float64
		expired := false
		if minExpNull.Valid {
			v := minExpNull.Float64
			minExp = &v
			if v <= nowSec {
				expired = true
			}
		}
		sites = append(sites, model.SiteSummary{
			Origin:            origin,
			AddedAt:           updatedAt,
			UpdatedAt:         updatedAt,
			CookieCount:       cookieCount,
			Expired:           expired,
			MinExpirationDate: minExp,
		})
	}
	if sites == nil {
		sites = []model.SiteSummary{}
	}
	return sites, rows.Err()
}

// DeleteSiteCookies deletes the record for origin.
func (r *SQLiteRepository) DeleteSiteCookies(ctx context.Context, origin string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM site_cookies WHERE origin = ?;`, origin)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected > 0, err
}

// Ping verifies database connectivity.
func (r *SQLiteRepository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// Close closes the underlying SQLite connection.
func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}
