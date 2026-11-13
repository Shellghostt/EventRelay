package load

import (
	"context"
	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
	"github.com/shellghostt/eventrelay/tests/testutil"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSustainedDelivery is opt-in and exercises signed HTTP -> PostgreSQL -> Redis
// -> four independent Worker instances -> HTTP, including durable completion.
func TestSustainedDelivery(t *testing.T) {
	if os.Getenv("EVENTRELAY_LOAD") != "1" {
		t.Skip("set EVENTRELAY_LOAD=1 for the 60 second load run")
	}
	old := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(old)
	h := testutil.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(204) }))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	for i := 0; i < 4; i++ {
		h.StartWithTiming(t, 1, 60*time.Second, 5*time.Second)
	}
	router := api.NewRouter(api.Deps{Store: h.Store, Publisher: h.Stream, Config: config.Config{AdminToken: "load", SignatureTolerance: time.Minute}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	latencies := make([]time.Duration, 0, 10000)
	heaps := []uint64{}
	start := time.Now()

	jobs := make(chan int, 64)
	samples := make(chan time.Duration, 10000)
	var producers sync.WaitGroup
	for producer := 0; producer < 4; producer++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for range jobs {
				body := `{"load":true}`
				req := httptest.NewRequest("POST", "/v1/endpoints/"+ep.ID.String()+"/events", strings.NewReader(body))
				req.Header.Set(hmacsign.HeaderName, hmacsign.Header(ep.IngestSecret, time.Now().Unix(), []byte(body)))
				began := time.Now()
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				samples <- time.Since(began)
				if rec.Code != 202 {
					t.Errorf("ingest status %d", rec.Code)
				}
			}
		}()
	}
	for i := 0; i < 10000; i++ {
		scheduled := start.Add(time.Duration(i) * (time.Minute / 10200))
		if delay := time.Until(scheduled); delay > 0 {
			time.Sleep(delay)
		}
		jobs <- i
		if (i+1)%2000 == 0 {
			runtime.GC()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			heaps = append(heaps, mem.HeapAlloc)
		}
	}
	close(jobs)
	producers.Wait()
	close(samples)
	for sample := range samples {
		latencies = append(latencies, sample)
	}
	deadline := time.Now().Add(10 * time.Second)
	var delivered int
	for time.Now().Before(deadline) {
		if err := h.SQL.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE status='delivered'`).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if delivered == 10000 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if delivered != 10000 {
		t.Fatalf("delivered %d/10000", delivered)
	}
	elapsed := time.Since(start)
	if elapsed > time.Minute {
		t.Errorf("throughput target missed: 10000 deliveries in %s", elapsed)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var attempts int
	if err := h.SQL.QueryRow(context.Background(), `SELECT count(*) FROM delivery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	t.Logf("delivered=%d attempts=%d elapsed=%s rate_per_min=%.2f ingest_p50=%s ingest_p99=%s heap_after_gc=%v", delivered, attempts, elapsed, float64(delivered)/elapsed.Minutes(), latencies[5000], latencies[9900], heaps)
	if attempts != 10000 {
		t.Fatal("unexpected duplicate attempts")
	}
	if heaps[len(heaps)-1] > heaps[0]+32<<20 {
		t.Fatal("heap grew by more than 32 MiB after warmup")
	}
}
