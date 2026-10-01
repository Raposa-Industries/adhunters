package load

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Networks are the data-center and cloud networks the bot check knows,
// sorted for lookup.
type Networks struct {
	v4, v6 []dcNet
}

type dcNet struct {
	p      netip.Prefix
	source string
}

// Match says whether the visitor's network (an address or a prefix, as the
// edge writes it: the /24 or /48) lies wholly inside a known data-center
// network, and whose.
func (n *Networks) Match(network string) (string, bool) {
	if n == nil || network == "" {
		return "", false
	}
	p, err := netip.ParsePrefix(network)
	if err != nil {
		a, aerr := netip.ParseAddr(network)
		if aerr != nil {
			return "", false
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	p = p.Masked()
	list := n.v4
	if p.Addr().Is6() {
		list = n.v6
	}
	// The list is sorted by start address; a prefix that holds p starts at
	// or before it. Prefixes do not nest much, so a short walk back from
	// the last start at or before p finds one.
	i := sort.Search(len(list), func(i int) bool { return p.Addr().Less(list[i].p.Addr()) })
	for k := i - 1; k >= 0 && k >= i-64; k-- {
		d := list[k]
		if d.p.Bits() <= p.Bits() && d.p.Contains(p.Addr()) {
			return d.source, true
		}
	}
	return "", false
}

// Len is how many networks are known.
func (n *Networks) Len() int {
	if n == nil {
		return 0
	}
	return len(n.v4) + len(n.v6)
}

func newNetworks(nets []dcNet) *Networks {
	n := &Networks{}
	for _, d := range nets {
		if d.p.Addr().Is4() {
			n.v4 = append(n.v4, d)
		} else {
			n.v6 = append(n.v6, d)
		}
	}
	for _, l := range [][]dcNet{n.v4, n.v6} {
		sort.Slice(l, func(i, k int) bool {
			if c := l[i].p.Addr().Compare(l[k].p.Addr()); c != 0 {
				return c < 0
			}
			return l[i].p.Bits() < l[k].p.Bits()
		})
	}
	return n
}

// networks reads the known networks, at most every 10 minutes.
func (l *Loader) networks(ctx context.Context, tx pgx.Tx) (*Networks, error) {
	if l.nets != nil && l.c.Now().Sub(l.netsAt) < 10*time.Minute {
		return l.nets, nil
	}
	rows, err := tx.Query(ctx, `SELECT source, prefix FROM funnels.dc_network`)
	if err != nil {
		return nil, err
	}
	var list []dcNet
	for rows.Next() {
		var d dcNet
		if err := rows.Scan(&d.source, &d.p); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, dcNet{p: d.p.Masked(), source: d.source})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	l.nets, l.netsAt = newNetworks(list), l.c.Now()
	return l.nets, nil
}

// NetworkSource is a published list of a cloud's networks.
type NetworkSource struct {
	Name  string
	URL   string
	Parse func([]byte) ([]netip.Prefix, error)
}

// NetworkSources are the lists fetched by default: the clouds that publish
// theirs in a stable place.
var NetworkSources = []NetworkSource{
	{Name: "aws", URL: "https://ip-ranges.amazonaws.com/ip-ranges.json", Parse: parseAWS},
	{Name: "gcp", URL: "https://www.gstatic.com/ipranges/cloud.json", Parse: parseGCP},
}

func parseAWS(b []byte) ([]netip.Prefix, error) {
	var doc struct {
		Prefixes []struct {
			IP string `json:"ip_prefix"`
		} `json:"prefixes"`
		IPv6 []struct {
			IP string `json:"ipv6_prefix"`
		} `json:"ipv6_prefixes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var s []string
	for _, p := range doc.Prefixes {
		s = append(s, p.IP)
	}
	for _, p := range doc.IPv6 {
		s = append(s, p.IP)
	}
	return prefixes(s)
}

func parseGCP(b []byte) ([]netip.Prefix, error) {
	var doc struct {
		Prefixes []struct {
			V4 string `json:"ipv4Prefix"`
			V6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var s []string
	for _, p := range doc.Prefixes {
		if p.V4 != "" {
			s = append(s, p.V4)
		}
		if p.V6 != "" {
			s = append(s, p.V6)
		}
	}
	return prefixes(s)
}

var cidrWord = regexp.MustCompile(`[0-9A-Fa-f:.]+/\d{1,3}`)

// ParsePlainList reads any text with one network per line (a CSV's first
// column, a list with comments): every a.b.c.d/n or IPv6 prefix found.
func ParsePlainList(b []byte) ([]netip.Prefix, error) {
	var s []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := cidrWord.FindString(line); m != "" {
			s = append(s, m)
		}
	}
	return prefixes(s)
}

func prefixes(s []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, x := range s {
		p, err := netip.ParsePrefix(strings.TrimSpace(x))
		if err != nil {
			continue
		}
		out = append(out, p.Masked())
	}
	if len(out) == 0 {
		return nil, errors.New("no networks in the list")
	}
	return out, nil
}

// FetchNetworks downloads a source's list, keeps it as received in the
// archive (networks/<source>/<yyyy>/<mm>/<dd>/<hhmmss>.txt), then replaces
// the source's networks with it.
func (l *Loader) FetchNetworks(ctx context.Context, src NetworkSource, client *http.Client) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s answered %s", src.URL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, err
	}
	return l.ImportNetworks(ctx, src.Name, b, src.Parse)
}

var sourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ImportNetworks keeps a list as received in the archive, parses it, and
// replaces the source's networks with it. It returns how many it kept.
func (l *Loader) ImportNetworks(ctx context.Context, source string, raw []byte, parse func([]byte) ([]netip.Prefix, error)) (int, error) {
	if !sourceName.MatchString(source) {
		return 0, fmt.Errorf("source %q: use lower-case letters, digits, - and _", source)
	}
	if parse == nil {
		parse = ParsePlainList
	}
	key := ""
	if l.c.Store != nil {
		key = "networks/" + source + "/" + l.c.Now().UTC().Format("2006/01/02/150405") + ".txt"
		sum := md5.Sum(raw)
		if err := l.c.Store.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), hex.EncodeToString(sum[:])); err != nil {
			return 0, fmt.Errorf("keeping the list: %w", err)
		}
	}
	ps, err := parse(raw)
	if err != nil {
		return 0, err
	}
	seen := map[netip.Prefix]bool{}
	var rows [][]any
	for _, p := range ps {
		if !seen[p] {
			seen[p] = true
			rows = append(rows, []any{source, p})
		}
	}
	tx, err := l.c.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM funnels.dc_network WHERE source = $1`, source); err != nil {
		return 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"funnels", "dc_network"}, []string{"source", "prefix"}, pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO funnels.dc_network_load (source, loaded_at, prefixes, raw_key) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source) DO UPDATE SET loaded_at = $2, prefixes = $3, raw_key = $4`,
		source, l.c.Now(), len(rows), key); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	l.nets = nil // read again at the next count
	l.c.Log.Info("data-center networks replaced", "source", source, "networks", len(rows), "raw", key)
	return len(rows), nil
}

// NetworksDue lists the sources fetched longer ago than every (or never).
func (l *Loader) NetworksDue(ctx context.Context, every time.Duration) ([]NetworkSource, error) {
	var due []NetworkSource
	for _, s := range NetworkSources {
		var at *time.Time
		err := l.c.DB.QueryRow(ctx, `SELECT loaded_at FROM funnels.dc_network_load WHERE source = $1`, s.Name).Scan(&at)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if at == nil || l.c.Now().Sub(*at) >= every {
			due = append(due, s)
		}
	}
	return due, nil
}
