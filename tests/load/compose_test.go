package load

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
)

// TestComposeSustainedDelivery exercises a running API and separate worker
// processes. Use a disposable Compose project; only its new endpoint is removed.
func TestComposeSustainedDelivery(t *testing.T) {
	apiURL := os.Getenv("EVENTRELAY_LOAD_API_URL")
	if apiURL == "" {
		t.Skip("set EVENTRELAY_LOAD_API_URL for the four-process Compose load test")
	}
	token := os.Getenv("EVENTRELAY_LOAD_ADMIN_TOKEN")
	targetHost := os.Getenv("EVENTRELAY_LOAD_TARGET_HOST")
	if token == "" || targetHost == "" || os.Getenv("EVENTRELAY_TEST_DATABASE_URL") == "" {
		t.Fatal("set EVENTRELAY_LOAD_ADMIN_TOKEN, EVENTRELAY_LOAD_TARGET_HOST and EVENTRELAY_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("EVENTRELAY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 16
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	var deliverySecret atomic.Value
	var calls atomic.Int32
	var received sync.Map
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
		secret := deliverySecret.Load()
		if err != nil || secret == nil || hmacsign.Verify(secret.([]byte), r.Header.Get(hmacsign.HeaderName), body, time.Now(), time.Minute) != nil {
			t.Error("invalid outbound signature or body")
			w.WriteHeader(400)
			return
		}
		id := r.Header.Get("X-EventRelay-Event-Id")
		if id == "" || r.Header.Get("X-EventRelay-Attempt") != "1" {
			t.Error("missing stable event ID or unexpected retry")
		}
		if _, duplicate := received.LoadOrStore(id, true); duplicate {
			t.Error("duplicate delivery in healthy load run")
		}
		calls.Add(1)
		w.WriteHeader(204)
	}))
	if err = target.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	target.Listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	target.Start()
	defer target.Close()
	targetURL := "http://" + net.JoinHostPort(targetHost, strconv.Itoa(target.Listener.Addr().(*net.TCPAddr).Port))

	var endpoint struct {
		ID             string `json:"id"`
		IngestSecret   string `json:"ingest_secret"`
		DeliverySecret string `json:"delivery_secret"`
	}
	createBody, err := json.Marshal(map[string]string{"url": targetURL})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL+"/v1/endpoints", bytes.NewReader(createBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		_ = resp.Body.Close()
		t.Fatalf("endpoint creation status %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&endpoint)
	_ = resp.Body.Close()
	if err != nil || endpoint.ID == "" {
		t.Fatalf("endpoint response: %v", err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM endpoints WHERE id=$1::uuid`, endpoint.ID); err != nil {
			t.Errorf("remove load-test endpoint: %v", err)
		}
	}()
	ingestSecret, err := base64.StdEncoding.DecodeString(endpoint.IngestSecret)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(endpoint.DeliverySecret)
	if err != nil {
		t.Fatal(err)
	}
	deliverySecret.Store(decoded)
	jobs := make(chan int, 64)
	samples := make(chan time.Duration, 10000)
	var producers sync.WaitGroup
	for i := 0; i < 4; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for range jobs {
				body := []byte(`{"load":true}`)
				r, err := http.NewRequestWithContext(ctx, "POST", apiURL+"/v1/endpoints/"+endpoint.ID+"/events", bytes.NewReader(body))
				if err != nil {
					t.Error(err)
					continue
				}
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set(hmacsign.HeaderName, hmacsign.Header(ingestSecret, time.Now().Unix(), body))
				began := time.Now()
				response, err := client.Do(r)
				samples <- time.Since(began)
				if err != nil {
					t.Error(err)
					continue
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 202 {
					t.Errorf("ingest status %d", response.StatusCode)
				}
			}
		}()
	}
	start := time.Now()
	for i := 0; i < 10000; i++ {
		if delay := time.Until(start.Add(time.Duration(i) * (time.Minute / 10200))); delay > 0 {
			time.Sleep(delay)
		}
		jobs <- i
	}
	close(jobs)
	producers.Wait()
	close(samples)
	var latencies []time.Duration
	for sample := range samples {
		latencies = append(latencies, sample)
	}
	var delivered, attempts int
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE endpoint_id=$1::uuid AND status='delivered'`, endpoint.ID).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if delivered == 10000 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM delivery_attempts a JOIN events e ON e.id=a.event_id WHERE e.endpoint_id=$1::uuid`, endpoint.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if delivered != 10000 || attempts != 10000 || calls.Load() != 10000 || len(latencies) != 10000 {
		t.Fatalf("delivered=%d attempts=%d receiver_calls=%d samples=%d", delivered, attempts, calls.Load(), len(latencies))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("four_processes delivered=%d attempts=%d elapsed=%s rate_per_min=%.2f ingest_p50=%s ingest_p99=%s", delivered, attempts, elapsed, float64(delivered)/elapsed.Minutes(), latencies[5000], latencies[9900])
	if elapsed > time.Minute {
		t.Fatalf("10000/min target missed: %s", elapsed)
	}
}
