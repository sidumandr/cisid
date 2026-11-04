// Package metrics exposes cisid's Prometheus metrics. Labels never carry
// repository names or other unbounded values.
package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every cisid metric plus Go runtime and process metrics.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

var (
	HTTPRequests = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "cisid_http_requests_total",
		Help: "HTTP requests by route pattern, method and status code.",
	}, []string{"route", "method", "code"})

	HTTPDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "cisid_http_request_duration_seconds",
		Help:    "HTTP request latency by route pattern, excluding long-lived streams.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	JobsFinished = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "cisid_jobs_finished_total",
		Help: "Jobs finished on this server, by final status.",
	}, []string{"status"})

	Webhooks = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "cisid_webhook_deliveries_total",
		Help: "GitHub webhook deliveries by outcome.",
	}, []string{"result"})

	CommitStatuses = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "cisid_commit_statuses_total",
		Help: "Commit statuses posted to the forges by outcome: sent, retry (forge unavailable), failed or skipped (statuses off for the forge).",
	}, []string{"result"})

	Notifications = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "cisid_notifications_total",
		Help: "Build results announced by kind (slack, webhook, email) and outcome: sent or failed.",
	}, []string{"kind", "result"})

	LogChunks = factory.NewCounter(prometheus.CounterOpts{
		Name: "cisid_log_chunks_total",
		Help: "Log chunks received from agents.",
	})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler serves the metrics in the Prometheus text format. A metric that
// cannot be collected (say, job counts while the database is down) is left
// out instead of failing the scrape: metrics matter most during an outage.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

// The collectors below read state of a running server; registering again
// (a restarted server in the same process, as in tests) replaces them.
var (
	regMu       sync.Mutex
	agentsGauge prometheus.Collector
	jobCounts   prometheus.Collector
)

func replace(slot *prometheus.Collector, c prometheus.Collector) {
	regMu.Lock()
	defer regMu.Unlock()
	if *slot != nil {
		Registry.Unregister(*slot)
	}
	Registry.MustRegister(c)
	*slot = c
}

// RegisterAgents reports the agents connected to this server.
func RegisterAgents(connected func() int) {
	replace(&agentsGauge, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "cisid_agents_connected",
		Help: "Agents connected to this server.",
	}, func() float64 { return float64(connected()) }))
}

// RegisterJobCounts reports the number of jobs per status, read from the
// database at every scrape.
func RegisterJobCounts(count func(ctx context.Context) (map[string]int, error), log *slog.Logger) {
	replace(&jobCounts, &jobsCollector{
		count: count,
		log:   log,
		desc:  prometheus.NewDesc("cisid_jobs", "Jobs in the database by status.", []string{"status"}, nil),
	})
}

type jobsCollector struct {
	count func(ctx context.Context) (map[string]int, error)
	log   *slog.Logger
	desc  *prometheus.Desc
}

func (c *jobsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *jobsCollector) Collect(ch chan<- prometheus.Metric) {
	// Prometheus' Collect has no context: a scrape is the root of this call
	// chain, like main, so it starts one with a timeout here.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := c.count(ctx)
	if err != nil {
		c.log.Warn("count jobs for metrics", "error", err)
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	for status, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n), status)
	}
}
