package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/qqyfly/CookieSyncSvr/internal/crypto"
	"github.com/qqyfly/CookieSyncSvr/internal/model"
)

// PostgresRepository implements SiteCookieRepository using pure-Go pgx/v5 for PostgreSQL on Debian VPS.
type PostgresRepository struct {
	pool   *pgxpool.Pool
	cipher *crypto.Cipher
}

// NewPostgresRepository connects to PostgreSQL and ensures the site_cookies schema exists.
func NewPostgresRepository(ctx context.Context, databaseURL string, cipher *crypto.Cipher) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	repo := &PostgresRepository{
		pool:   pool,
		cipher: cipher,
	}
	if err := repo.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repo, nil
}

func (r *PostgresRepository) initSchema(ctx context.Context) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS site_cookies (
		origin          TEXT PRIMARY KEY,
		cookies_data    TEXT NOT NULL,
		cookie_count    INTEGER NOT NULL DEFAULT 0,
		min_expires_at  DOUBLE PRECISION,
		updated_at      BIGINT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_site_cookies_updated ON site_cookies (updated_at DESC);
	`
	_, err := r.pool.Exec(ctx, schema)
	return err
}

// UpsertSiteCookies atomically overwrites the old cookie snapshot for sc.Origin with the latest push.
func (r *PostgresRepository) UpsertSiteCookies(ctx context.Context, sc model.SiteCookies) error {
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
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (origin) DO UPDATE SET
		cookies_data   = EXCLUDED.cookies_data,
		cookie_count   = EXCLUDED.cookie_count,
		min_expires_at = EXCLUDED.min_expires_at,
		updated_at     = EXCLUDED.updated_at;
	`
	_, err = r.pool.Exec(ctx, query, sc.Origin, storedData, len(sc.Cookies), minExp, sc.UpdatedAt)
	return err
}

// GetSiteCookies retrieves the stored cookie snapshot for origin.
func (r *PostgresRepository) GetSiteCookies(ctx context.Context, origin string) (*model.SiteCookies, error) {
	const query = `
	SELECT origin, cookies_data, updated_at
	FROM site_cookies
	WHERE origin = $1;
	`
	var (
		storedOrigin string
		storedData   string
		updatedAt    int64
	)
	err := r.pool.QueryRow(ctx, query, origin).Scan(&storedOrigin, &storedData, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
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
func (r *PostgresRepository) ListSites(ctx context.Context) ([]model.SiteSummary, error) {
	const query = `
	SELECT origin, cookie_count, min_expires_at, updated_at
	FROM site_cookies
	ORDER BY updated_at DESC, origin ASC;
	`
	rows, err := r.pool.Query(ctx, query)
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
func (r *PostgresRepository) DeleteSiteCookies(ctx context.Context, origin string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM site_cookies WHERE origin = $1;`, origin)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Ping verifies database connectivity.
func (r *PostgresRepository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// Close closes the underlying PostgreSQL pool.
func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}
