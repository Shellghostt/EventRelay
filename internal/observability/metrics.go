package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every metric exposed by EventRelay. A dedicated registry is
// used instead of the default one so that tests can assert on metric deltas.
var Registry = prometheus.NewRegistry()

var (
	PublishFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "eventrelay_publish_failures_total", Help: "Ingest publish failures; durable events are recovered by the sweeper."})
	// IngestTotal counts accepted and rejected ingest requests.
	IngestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eventrelay_events_ingested_total",
		Help: "Number of ingest requests by result.",
	}, []string{"result"})

	// IngestDuration observes end-to-end ingest handler duration.
	IngestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "eventrelay_ingest_duration_seconds",
		Help:    "Ingest handler duration in seconds.",
		Buckets: prometheus.DefBuckets,
	})

	// HTTPRequestsTotal counts requests by route, method and status class.
	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "eventrelay_http_requests_total",
		Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})

	// HTTPRequestDuration observes request duration by route.
	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "eventrelay_http_request_duration_seconds",
		Help:    "HTTP request duration by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
)

func init() {
	Registry.MustRegister(
		PublishFailures,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		IngestTotal,
		IngestDuration,
		HTTPRequestsTotal,
		HTTPRequestDuration,
	)
}

// MetricsHandler exposes the registry in Prometheus text format.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
