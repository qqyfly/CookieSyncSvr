package repository

import (
	"context"
	"errors"

	"github.com/qqyfly/CookieSyncSvr/internal/model"
)

// ErrSiteNotFound is returned when no cookie snapshot exists for the requested origin.
var ErrSiteNotFound = errors.New("site not found")

// SiteCookieRepository defines the storage contract for saving and retrieving
// per-site cookie snapshots.
type SiteCookieRepository interface {
	// UpsertSiteCookies atomically saves (or overwrites) the latest cookie snapshot for sc.Origin.
	UpsertSiteCookies(ctx context.Context, sc model.SiteCookies) error

	// GetSiteCookies retrieves the latest cookie snapshot for the given normalized origin.
	GetSiteCookies(ctx context.Context, origin string) (*model.SiteCookies, error)

	// ListSites returns all stored sites ordered by updatedAt descending (newest first,
	// matching sortSitesNewestFirst in CookieSyncChromeExtension/src/sites/site-list.ts).
	ListSites(ctx context.Context) ([]model.SiteSummary, error)

	// DeleteSiteCookies removes the stored cookie snapshot for the given normalized origin.
	DeleteSiteCookies(ctx context.Context, origin string) (bool, error)

	// Ping checks database connectivity.
	Ping(ctx context.Context) error

	// Close releases database resources.
	Close() error
}
