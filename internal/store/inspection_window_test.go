package store

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/migrations"
)

func TestStatisticsExactUTCBoundaries(t *testing.T) {
	dsn := os.Getenv("EVENTRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EVENTRELAY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "er_stats_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tenant := uuid.MustParse(DefaultTenantID)
	ep, err := s.CreateEndpoint(ctx, CreateEndpointInput{TenantID: tenant, URL: "https://boundary.example", IngestSecret: []byte("synthetic"), DeliverySecret: []byte("synthetic"), MaxAttempts: 8})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.InsertEvent(ctx, tenant, ep.ID, "boundary", []byte("{}"), "")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from := end.Add(-24 * time.Hour)
	for i, finish := range []time.Time{from.Add(-time.Microsecond), from, end.Add(-time.Microsecond), end} {
		if _, err = s.pool.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,cycle_attempt_no,started_at,finished_at,outcome,response_status)
			VALUES($1,$2,1,$3,$3,'success',204)`, e.ID, i+1, finish); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.endpointStatsAt(ctx, tenant, ep.ID, &end)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Available || stats.HTTPResponseAttempts == nil || *stats.HTTPResponseAttempts != 2 || *stats.SuccessHTTPAttempts != 2 || *stats.NoResponseAttempts != 0 || stats.SuccessRate == nil || *stats.SuccessRate != 1 || !stats.WindowFrom.Equal(from) || !stats.WindowTo.Equal(end) {
		t.Fatalf("UTC [from,to) boundaries incorrect: %+v", stats)
	}
}
