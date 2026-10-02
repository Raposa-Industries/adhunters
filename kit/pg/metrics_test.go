package pg

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Raposa-Industries/adhunters/kit/ops"
)

func TestPoolsShowOnMetrics(t *testing.T) {
	// Pools connect lazily, so these need no database.
	for _, max := range []int32{3, 2} {
		pc, err := pgxpool.ParseConfig("postgres://nobody@127.0.0.1:1/none")
		if err != nil {
			t.Fatal(err)
		}
		pc.MaxConns = max
		p, err := pgxpool.NewWithConfig(context.Background(), pc)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		pools.add("metrics-test", p)
	}
	rec := httptest.NewRecorder()
	ops.New("metrics-test", "v1").Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`adhunters_db_pool_max_connections{pool="metrics-test"} 5`,
		`adhunters_db_pool_connections{pool="metrics-test",state="idle"} 0`,
		`adhunters_db_pool_waits_total{pool="metrics-test"} 0`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in:\n%s", want, b)
		}
	}
}
