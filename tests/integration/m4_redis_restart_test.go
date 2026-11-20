package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

// The explicit container ID enables this disruptive test. Run it separately
// against disposable services; ordinary integration tests do not restart Redis.
func TestM4ReplayAcrossRedisContainerRestart(t *testing.T) {
	id := os.Getenv("EVENTRELAY_TEST_REDIS_CONTAINER")
	if id == "" {
		t.Skip("set EVENTRELAY_TEST_REDIS_CONTAINER to a disposable Redis container's full ID")
	}
	if _, err := hex.DecodeString(id); err != nil || len(id) != 64 {
		t.Fatal("EVENTRELAY_TEST_REDIS_CONTAINER must be a full hexadecimal container ID")
	}
	if os.Getenv("EVENTRELAY_TEST_DATABASE_URL") == "" || os.Getenv("EVENTRELAY_TEST_REDIS_URL") == "" {
		t.Fatal("the container restart test requires both disposable service URLs")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("the container restart test requires the Docker CLI")
	}
	h := testutil.New(t)
	before := m4InspectRedisContainer(t, id)
	m4CheckRedisContainer(t, before, id, h.RedisURL)
	// Restore the verified container even if an outage assertion fails. Cleanup
	// runs before the harness removes its own Redis key and PostgreSQL schema.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, "docker", "start", id).CombinedOutput(); err != nil {
			t.Errorf("restore disposable Redis container: %v: %s", err, output)
		}
	})

	ctx := context.Background()
	var calls atomic.Int32
	bodies := make(chan []byte, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case bodies <- body:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	if _, err := h.SQL.Exec(ctx, `UPDATE endpoints SET max_attempts=1 WHERE id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	e := h.Event(t, ep)
	m4Finish(t, h, e, "retryable", http.StatusServiceUnavailable)
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonAttemptsExhausted)
	prior, _, err := h.Store.ListAttempts(ctx, e.TenantID, e.ID, 0, 50)
	if err != nil || len(prior) != 1 {
		t.Fatalf("initial exhausted history: %+v, err=%v", prior, err)
	}

	m4Request(t, m4Router(h), "POST", "/v1/events/"+e.ID.String()+"/replay", `{"expected_replay_count":0}`, true, http.StatusAccepted)
	if err := h.Stream.Publish(ctx, store.Dispatch{TenantID: e.TenantID, EventID: e.ID}); err != nil {
		t.Fatal(err)
	}
	m4RedisDocker(t, id, "stop", "--timeout", "1")
	if m4InspectRedisContainer(t, id).State.Running {
		t.Fatal("Redis container did not stop")
	}
	outageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.Stream.Ping(outageCtx); err == nil {
		t.Fatal("the real Redis outage was not observed")
	}
	w := worker.Worker{Store: h.Store, Stream: h.Stream, LeaseDuration: time.Second, SweepInterval: 20 * time.Millisecond}
	if err := w.Sweep(outageCtx); err == nil {
		t.Fatal("the durable dispatcher did not observe the Redis outage")
	}
	pending, err := h.Store.EventDetailByID(ctx, e.TenantID, e.ID)
	if err != nil || pending.Status != store.StatusPending || pending.ReplayCount != 1 || pending.AttemptCount != 1 || pending.CycleAttemptCount != 0 || calls.Load() != 0 {
		t.Fatalf("outage changed accepted replay or created a delivery: %+v, err=%v, calls=%d", pending.Event, err, calls.Load())
	}

	m4RedisDocker(t, id, "start")
	after := m4InspectRedisContainer(t, id)
	if !after.State.Running || after.State.StartedAt == before.State.StartedAt {
		t.Fatal("Redis did not acquire a new container start time")
	}
	// Docker can assign a different host port when a randomly published service
	// restarts. Reconnect only to a port belonging to this same verified container.
	restoredURL := m4RedisURLAfterRestart(t, after, h.RedisURL)
	m4CheckRedisContainer(t, after, id, restoredURL)
	if restoredURL != h.RedisURL {
		if err := h.Stream.Close(); err != nil {
			t.Fatal(err)
		}
		restored, err := stream.Open(restoredURL)
		if err != nil {
			t.Fatal(err)
		}
		restored.Key = h.Stream.Key
		h.Stream, h.RedisURL = restored, restoredURL
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = restored.Client.Del(cleanupCtx, restored.Key).Err()
			_ = restored.Close()
		})
	}
	m4WaitForRedis(t, h)
	// Remove only this fixture's notification/group after the real restart so
	// recovery must use PostgreSQL even when Redis AOF preserved the notification.
	if err := h.Stream.Client.Del(ctx, h.Stream.Key).Err(); err != nil {
		t.Fatal(err)
	}
	h.Start(t, 1)
	h.Wait(t, e.ID, store.StatusDelivered, 5*time.Second)
	final, err := h.Store.EventDetailByID(ctx, e.TenantID, e.ID)
	if err != nil || final.ReplayCount != 1 || final.AttemptCount != 2 || final.CycleAttemptCount != 1 || final.CycleMaxAttempts != 1 {
		t.Fatalf("restart recovery counters: %+v, err=%v", final.Event, err)
	}
	if final.ID != e.ID || final.EndpointID != e.EndpointID || final.EventType != e.EventType || !final.ReceivedAt.Equal(e.ReceivedAt) || !bytes.Equal(final.Payload, e.Payload) {
		t.Fatal("restart recovery changed captured event identity or payload")
	}
	attempts, _, err := h.Store.ListAttempts(ctx, e.TenantID, e.ID, 0, 50)
	if err != nil || len(attempts) != 2 || !reflect.DeepEqual(prior[0], attempts[0]) || attempts[1].AttemptNo != 2 || attempts[1].CycleNo != 1 || attempts[1].FinishedAt == nil || attempts[1].Outcome != "success" {
		t.Fatalf("restart recovery lost or rewrote attempt history: %+v, err=%v", attempts, err)
	}
	cycles, _, err := h.Store.ListCycles(ctx, e.TenantID, e.ID, -1, 50)
	if err != nil || len(cycles) != 1 || cycles[0].FinalStatus != store.StatusDeadLettered || cycles[0].TerminalReason == nil || *cycles[0].TerminalReason != retry.ReasonAttemptsExhausted {
		t.Fatalf("restart recovery lost exhausted-cycle evidence: %+v, err=%v", cycles, err)
	}
	select {
	case body := <-bodies:
		if calls.Load() != 1 || !bytes.Equal(body, e.Payload) {
			t.Fatalf("replay delivery calls=%d, payload=%q", calls.Load(), body)
		}
	default:
		t.Fatal("no real replay delivery was observed")
	}
	t.Logf("Redis stopped and started: %s -> %s; replay_count=1, lifetime_attempts=2, retained_cycles=1, destination_calls=1", before.State.StartedAt, after.State.StartedAt)
	t.Logf("Redis published ports: %v -> %v", before.NetworkSettings.Ports["6379/tcp"], after.NetworkSettings.Ports["6379/tcp"])
}

type m4RedisContainer struct {
	ID     string
	Config struct{ Image string }
	State  struct {
		Running   bool
		StartedAt string
	}
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func m4RedisDocker(t *testing.T, id string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", append(args, id)...).CombinedOutput()
	if err != nil {
		t.Fatalf("disposable Redis Docker operation %s: %v: %s", args[0], err, output)
	}
	return output
}

func m4InspectRedisContainer(t *testing.T, id string) m4RedisContainer {
	t.Helper()
	var container m4RedisContainer
	if err := json.Unmarshal(m4RedisDocker(t, id, "inspect", "--format", "{{json .}}"), &container); err != nil {
		t.Fatal(err)
	}
	return container
}

func m4CheckRedisContainer(t *testing.T, container m4RedisContainer, id, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "redis" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		t.Fatal("the restart fixture requires a loopback Redis URL")
	}
	if container.ID != id || container.Config.Image != "redis:7.2-alpine" || !container.State.Running {
		t.Fatal("the selected running container must be the disposable Redis 7.2 service")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	for _, mapping := range container.NetworkSettings.Ports["6379/tcp"] {
		if mapping.HostPort == port && (mapping.HostIP == "0.0.0.0" || mapping.HostIP == "127.0.0.1" || mapping.HostIP == "::" || mapping.HostIP == "::1") {
			return
		}
	}
	t.Fatal("the selected Redis container does not publish the fixture's loopback port")
}

func m4RedisURLAfterRestart(t *testing.T, container m4RedisContainer, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	var replacement string
	for _, mapping := range container.NetworkSettings.Ports["6379/tcp"] {
		ipv6 := u.Hostname() == "::1"
		if ipv6 && mapping.HostIP != "::" && mapping.HostIP != "::1" || !ipv6 && mapping.HostIP != "0.0.0.0" && mapping.HostIP != "127.0.0.1" {
			continue
		}
		value, err := strconv.Atoi(mapping.HostPort)
		if err != nil || value < 1 || value > 65535 {
			continue
		}
		if mapping.HostPort == port {
			return rawURL
		}
		replacement = mapping.HostPort
	}
	if replacement == "" {
		t.Fatal("restarted Redis container has no compatible published loopback port")
	}
	u.Host = net.JoinHostPort(u.Hostname(), replacement)
	return u.String()
}

func m4WaitForRedis(t *testing.T, h *testutil.Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		pingCtx, pingCancel := context.WithTimeout(ctx, time.Second)
		err := h.Stream.Ping(pingCtx)
		pingCancel()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Redis did not become reachable after its physical restart")
}
