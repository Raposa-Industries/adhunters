// Package spyad reads one Spy ad for Create (decisions/0022): its picture's
// address, newest headline, brand and vertical, through tracks_api and
// spy_api only, and downloads the picture.
package spyad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPicture is the most an ad's picture may weigh.
const MaxPicture = 20 << 20

// ErrNotFound means Spy has no creative with that id.
var ErrNotFound = errors.New("spy: no such creative")

// Ad is what Create takes from a Spy ad.
type Ad struct {
	CreativeID int64  `json:"creative_id"`
	ImageURL   string `json:"image_url"`
	Format     string `json:"format"`
	Headline   string `json:"headline"`
	Brand      string `json:"brand"`
	// VerticalID is Spy's vertical for it; empty when Spy has none, or
	// Create cannot read spy_api.
	VerticalID string `json:"vertical_id"`
}

// Reader reads ads from the database Create shares with Spy.
type Reader struct {
	db   *pgxpool.Pool
	http *http.Client
	log  *slog.Logger
}

// New returns a reader.
func New(db *pgxpool.Pool, log *slog.Logger) *Reader {
	return &Reader{db: db, http: publicOnly(), log: log}
}

// publicOnly is a client that reaches public addresses only: an ad's
// picture address comes from a scraped page, so it must not reach the
// box's own services.
func publicOnly() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !Public(ip) {
			return fmt.Errorf("spy: %s is not a public address", host)
		}
		return nil
	}}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = d.DialContext
	t.Proxy = nil
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}

// Public reports whether ip is on the public internet.
func Public(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		// Tailscale's and carrier-grade NAT's 100.64.0.0/10.
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64))
}

// Ad reads one creative.
func (r *Reader) Ad(ctx context.Context, id int64) (Ad, error) {
	a := Ad{CreativeID: id}
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(cr.image_url, ''), COALESCE(cr.format_type, ''),
		       COALESCE((SELECT a.headline FROM tracks_api.ad_v1 a
		                 WHERE a.creative_id = cr.id AND btrim(COALESCE(a.headline, '')) <> ''
		                 ORDER BY a.last_seen_at DESC NULLS LAST LIMIT 1), ''),
		       COALESCE((SELECT b.name FROM tracks_api.ad_v1 a JOIN tracks_api.brand_v1 b ON b.id = a.brand_id
		                 WHERE a.creative_id = cr.id ORDER BY a.last_seen_at DESC NULLS LAST LIMIT 1), '')
		FROM tracks_api.creative_v1 cr WHERE cr.id = $1`, id).Scan(&a.ImageURL, &a.Format, &a.Headline, &a.Brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ad{}, ErrNotFound
	}
	if err != nil {
		return Ad{}, err
	}
	err = r.db.QueryRow(ctx, `SELECT COALESCE(vertical_id, '') FROM spy_api.creative_class_v1 WHERE creative_id = $1`, id).Scan(&a.VerticalID)
	var pe *pgconn.PgError
	switch {
	case err == nil, errors.Is(err, pgx.ErrNoRows):
	case errors.As(err, &pe) && (pe.Code == "42501" || pe.Code == "42P01" || pe.Code == "3F000"):
		// No grant or no spy_api yet: the person picks the vertical.
		r.log.Warn("spy vertical not readable", "creative", id, "err", err)
	default:
		return Ad{}, err
	}
	return a, nil
}

// Picture downloads an ad's picture.
func (r *Reader) Picture(ctx context.Context, url string) ([]byte, error) {
	if url == "" {
		return nil, errors.New("spy: the ad has no picture")
	}
	if u, err := neturl.Parse(url); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("spy: %q is not a web address", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("spy: picture address: %w", err)
	}
	res, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spy: picture: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spy: picture: %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxPicture+1))
	if err != nil {
		return nil, fmt.Errorf("spy: picture: %w", err)
	}
	if len(b) > MaxPicture {
		return nil, fmt.Errorf("spy: picture over %d MB", MaxPicture>>20)
	}
	return b, nil
}
