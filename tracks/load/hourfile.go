package load

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/parquet-go/parquet-go"
)

// An hour file is one UTC day of one hourly count table, as a Parquet file in
// the archive (decision 0023): one row per row of the table, the same
// columns and types, sorted by hour and key. Anyone can read one with DuckDB
// or pandas; the loader reads them back to check them and to bring a day's
// hours back into the database.

// hourlyRow is a row of tracks.ad_hourly.
type hourlyRow struct {
	Hour            time.Time `parquet:"hour,timestamp(microsecond)"`
	AdID            int32     `parquet:"ad_id"`
	PublisherID     int32     `parquet:"publisher_id"`
	DeviceID        int16     `parquet:"device_id"`
	Sightings       int32     `parquet:"sightings"`
	Scrapes         int32     `parquet:"scrapes"`
	FeedPositionSum int64     `parquet:"feed_position_sum"`
	FeedPositionMin *int16    `parquet:"feed_position_min,optional"`
	FeedPositionMax *int16    `parquet:"feed_position_max,optional"`
	FirstSeenAt     time.Time `parquet:"first_seen_at,timestamp(microsecond)"`
	LastSeenAt      time.Time `parquet:"last_seen_at,timestamp(microsecond)"`
}

// brandRow is a row of tracks.ad_account_brand_hourly.
type brandRow struct {
	Hour        time.Time `parquet:"hour,timestamp(microsecond)"`
	AdID        int32     `parquet:"ad_id"`
	AccountID   *int32    `parquet:"account_id,optional"`
	BrandID     *int32    `parquet:"brand_id,optional"`
	PublisherID int32     `parquet:"publisher_id"`
	DeviceID    int16     `parquet:"device_id"`
	Sightings   int32     `parquet:"sightings"`
}

// hourRecord is what the loader needs from a row type.
type hourRecord interface {
	dest() []any   // scan targets, in the table's column order
	values() []any // COPY values, in the same order
	hourKey() (hour time.Time, sightings int64, print uint64)
}

func (r *hourlyRow) dest() []any {
	return []any{&r.Hour, &r.AdID, &r.PublisherID, &r.DeviceID, &r.Sightings, &r.Scrapes, &r.FeedPositionSum,
		&r.FeedPositionMin, &r.FeedPositionMax, &r.FirstSeenAt, &r.LastSeenAt}
}

func (r *hourlyRow) values() []any {
	return []any{r.Hour, r.AdID, r.PublisherID, r.DeviceID, r.Sightings, r.Scrapes, r.FeedPositionSum,
		r.FeedPositionMin, r.FeedPositionMax, r.FirstSeenAt, r.LastSeenAt}
}

func (r *hourlyRow) hourKey() (time.Time, int64, uint64) {
	var p printer
	p.time(r.Hour)
	p.int(int64(r.AdID))
	p.int(int64(r.PublisherID))
	p.int(int64(r.DeviceID))
	p.int(int64(r.Sightings))
	p.int(int64(r.Scrapes))
	p.int(r.FeedPositionSum)
	p.opt16(r.FeedPositionMin)
	p.opt16(r.FeedPositionMax)
	p.time(r.FirstSeenAt)
	p.time(r.LastSeenAt)
	return r.Hour.UTC(), int64(r.Sightings), p.sum()
}

func (r *brandRow) dest() []any {
	return []any{&r.Hour, &r.AdID, &r.AccountID, &r.BrandID, &r.PublisherID, &r.DeviceID, &r.Sightings}
}

func (r *brandRow) values() []any {
	return []any{r.Hour, r.AdID, r.AccountID, r.BrandID, r.PublisherID, r.DeviceID, r.Sightings}
}

func (r *brandRow) hourKey() (time.Time, int64, uint64) {
	var p printer
	p.time(r.Hour)
	p.int(int64(r.AdID))
	p.opt32(r.AccountID)
	p.opt32(r.BrandID)
	p.int(int64(r.PublisherID))
	p.int(int64(r.DeviceID))
	p.int(int64(r.Sightings))
	return r.Hour.UTC(), int64(r.Sightings), p.sum()
}

// hourTable describes one hourly table for the generic code.
type hourTable struct {
	name  string
	cols  []string
	order string
}

var (
	hourlyTable = hourTable{"ad_hourly", []string{"hour", "ad_id", "publisher_id", "device_id", "sightings", "scrapes",
		"feed_position_sum", "feed_position_min", "feed_position_max", "first_seen_at", "last_seen_at"},
		"hour, ad_id, publisher_id, device_id"}
	brandTable = hourTable{"ad_account_brand_hourly", []string{"hour", "ad_id", "account_id", "brand_id", "publisher_id",
		"device_id", "sightings"}, "hour, ad_id, publisher_id, device_id, account_id, brand_id"}
	hourTables = []hourTable{hourlyTable, brandTable}
)

func (t hourTable) partition(month time.Time) string { return t.name + "_" + month.Format("200601") }

func (t hourTable) key(day time.Time, version int) string {
	return fmt.Sprintf("hourly/%s/%s-v%d.parquet", t.name, day.Format("2006/01/02"), version)
}

// printer fingerprints one row's values (FNV-1a over a fixed encoding), so
// two copies of an hour match only when every value of every row does.
type printer struct{ b []byte }

func (p *printer) int(v int64) { p.b = binary.LittleEndian.AppendUint64(p.b, uint64(v)) }
func (p *printer) time(t time.Time) {
	p.int(t.UnixMicro())
}
func (p *printer) opt16(v *int16) {
	if v == nil {
		p.b = append(p.b, 0)
		return
	}
	p.b = append(p.b, 1)
	p.int(int64(*v))
}
func (p *printer) opt32(v *int32) {
	if v == nil {
		p.b = append(p.b, 0)
		return
	}
	p.b = append(p.b, 1)
	p.int(int64(*v))
}
func (p *printer) sum() uint64 {
	h := fnv.New64a()
	_, _ = h.Write(p.b)
	return h.Sum64()
}

// hourTally is what one hour of a table adds up to: rows, sightings, and the
// sum of its rows' fingerprints (order does not matter, every value does).
type hourTally struct {
	Rows      int64
	Sightings int64
	Print     uint64
}

// dayTally is a day of hourTally, by hour (UTC).
type dayTally map[time.Time]hourTally

func (t dayTally) add(r hourRecord) {
	h, s, p := r.hourKey()
	x := t[h]
	x.Rows++
	x.Sightings += s
	x.Print += p
	t[h] = x
}

func (t dayTally) totals() (rows, sightings int64) {
	for _, x := range t {
		rows += x.Rows
		sightings += x.Sightings
	}
	return rows, sightings
}

// differ returns the hours whose tallies differ between a and b. With
// totalsOnly, only rows and sightings are compared.
func differ(a, b dayTally, totalsOnly bool) []time.Time {
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, m := range []dayTally{a, b} {
		for h := range m {
			if seen[h] {
				continue
			}
			seen[h] = true
			x, y := a[h], b[h]
			if x.Rows != y.Rows || x.Sightings != y.Sightings || (!totalsOnly && x.Print != y.Print) {
				out = append(out, h)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// readDay streams one day of t from the database in file order, handing
// each row to each, and returns its tally.
func readDay[T any, P interface {
	*T
	hourRecord
}](ctx context.Context, q pgx.Tx, t hourTable, day time.Time, each func(*T) error) (dayTally, error) {
	sql := "SELECT " + joinCols(t.cols) + " FROM " + pgx.Identifier{"tracks", t.name}.Sanitize() +
		" WHERE hour >= $1 AND hour < $2 ORDER BY " + t.order
	rows, err := q.Query(ctx, sql, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tally := dayTally{}
	var r T
	for rows.Next() {
		r = *new(T)
		if err := rows.Scan(P(&r).dest()...); err != nil {
			return nil, err
		}
		tally.add(P(&r))
		if each != nil {
			if err := each(&r); err != nil {
				return nil, err
			}
		}
	}
	return tally, rows.Err()
}

// sqlTally counts one day of t per hour in SQL, apart from the Go reading.
func sqlTally(ctx context.Context, q pgx.Tx, t hourTable, day time.Time) (dayTally, error) {
	rows, err := q.Query(ctx, "SELECT hour, count(*), COALESCE(sum(sightings), 0) FROM "+
		pgx.Identifier{"tracks", t.name}.Sanitize()+" WHERE hour >= $1 AND hour < $2 GROUP BY hour", day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tally := dayTally{}
	for rows.Next() {
		var h time.Time
		var x hourTally
		if err := rows.Scan(&h, &x.Rows, &x.Sightings); err != nil {
			return nil, err
		}
		tally[h.UTC()] = x
	}
	return tally, rows.Err()
}

// writeDay writes one day of t from the database to w as Parquet.
func writeDay[T any, P interface {
	*T
	hourRecord
}](ctx context.Context, q pgx.Tx, t hourTable, day time.Time, w io.Writer) (dayTally, error) {
	pw := parquet.NewGenericWriter[T](w,
		parquet.Compression(&parquet.Zstd),
		parquet.MaxRowsPerRowGroup(250_000),
		parquet.CreatedBy("tracks-loader", "", ""),
		parquet.KeyValueMetadata("table", "tracks."+t.name),
		parquet.KeyValueMetadata("day", day.Format("2006-01-02")))
	buf := make([]T, 0, 4096)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		_, err := pw.Write(buf)
		buf = buf[:0]
		return err
	}
	tally, err := readDay[T, P](ctx, q, t, day, func(r *T) error {
		buf = append(buf, *r)
		if len(buf) == cap(buf) {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if cerr := pw.Close(); err == nil {
		err = cerr
	}
	return tally, err
}

// fileRows reads an hour file row by row.
type fileRows[T any, P interface {
	*T
	hourRecord
}] struct {
	f     *os.File
	r     *parquet.GenericReader[T]
	buf   []T
	i, n  int
	eof   bool
	err   error
	Tally dayTally
}

func openFileRows[T any, P interface {
	*T
	hourRecord
}](path string) (*fileRows[T, P], error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a readable hour file: %w", path, err)
	}
	want := parquet.SchemaOf(new(T))
	if !parquet.EqualNodes(pf.Schema(), want) {
		_ = f.Close()
		return nil, fmt.Errorf("%s has columns %s, want %s", path, pf.Schema(), want)
	}
	return &fileRows[T, P]{f: f, r: parquet.NewGenericReader[T](pf), buf: make([]T, 4096), i: -1, Tally: dayTally{}}, nil
}

// Next, Values and Err make fileRows a pgx.CopyFromSource.
func (s *fileRows[T, P]) Next() bool {
	s.i++
	for s.i >= s.n {
		if s.eof || s.err != nil {
			return false
		}
		n, err := s.r.Read(s.buf)
		if errors.Is(err, io.EOF) {
			s.eof = true
		} else if err != nil {
			s.err = err
			return false
		}
		s.n, s.i = n, 0
	}
	s.Tally.add(P(&s.buf[s.i]))
	return true
}

func (s *fileRows[T, P]) Values() ([]any, error) { return P(&s.buf[s.i]).values(), nil }

func (s *fileRows[T, P]) Err() error { return s.err }

func (s *fileRows[T, P]) Close() error {
	_ = s.r.Close()
	return s.f.Close()
}

// tallyFile reads a whole hour file and returns its tally.
func tallyFile[T any, P interface {
	*T
	hourRecord
}](path string) (dayTally, error) {
	fr, err := openFileRows[T, P](path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fr.Close() }()
	for fr.Next() {
	}
	return fr.Tally, fr.Err()
}

func joinCols(cols []string) string {
	s := ""
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += c
	}
	return s
}

// Type-specific entry points, so callers pick a table by name.

func (t hourTable) readDay(ctx context.Context, q pgx.Tx, day time.Time) (dayTally, error) {
	if t.name == hourlyTable.name {
		return readDay[hourlyRow](ctx, q, t, day, nil)
	}
	return readDay[brandRow](ctx, q, t, day, nil)
}

func (t hourTable) writeDay(ctx context.Context, q pgx.Tx, day time.Time, w io.Writer) (dayTally, error) {
	if t.name == hourlyTable.name {
		return writeDay[hourlyRow](ctx, q, t, day, w)
	}
	return writeDay[brandRow](ctx, q, t, day, w)
}

func (t hourTable) tallyFile(path string) (dayTally, error) {
	if t.name == hourlyTable.name {
		return tallyFile[hourlyRow](path)
	}
	return tallyFile[brandRow](path)
}

// copyFile COPYs an hour file into the table and returns what it copied.
func (t hourTable) copyFile(ctx context.Context, q pgx.Tx, path string) (dayTally, error) {
	ident := pgx.Identifier{"tracks", t.name}
	if t.name == hourlyTable.name {
		fr, err := openFileRows[hourlyRow](path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = fr.Close() }()
		_, err = q.CopyFrom(ctx, ident, t.cols, fr)
		return fr.Tally, err
	}
	fr, err := openFileRows[brandRow](path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fr.Close() }()
	_, err = q.CopyFrom(ctx, ident, t.cols, fr)
	return fr.Tally, err
}
