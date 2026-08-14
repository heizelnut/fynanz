// Package metrics exposes Prometheus collectors for the sync job (AGENTS.md
// §8). Mounted at /metrics via promhttp from the main server.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// SyncRuns counts completed sync attempts per link and result.
	// result is one of: success | error | reauth_needed
	SyncRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "fynanz",
		Subsystem: "sync",
		Name:      "runs_total",
		Help:      "Total sync runs per bank link and result.",
	}, []string{"bank", "result"})

	// TransactionsIngested counts transactions stored across all syncs (new
	// rows only, deduplicated re-syncs excluded), per bank link.
	TransactionsIngested = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "fynanz",
		Subsystem: "sync",
		Name:      "transactions_ingested_total",
		Help:      "New transactions stored per bank link.",
	}, []string{"bank"})

	// AccountsSynced is the current number of linked accounts per bank link.
	AccountsSynced = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "fynanz",
		Subsystem: "sync",
		Name:      "accounts",
		Help:      "Currently linked accounts per bank link.",
	}, []string{"bank"})

	// NotificationsSent counts delivered pushes per backend and event.
	NotificationsSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "fynanz",
		Subsystem: "notify",
		Name:      "sent_total",
		Help:      "Notifications delivered per backend and event.",
	}, []string{"backend", "event"})

	// NotificationsFailed counts failed sends per backend and event.
	NotificationsFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "fynanz",
		Subsystem: "notify",
		Name:      "failed_total",
		Help:      "Notifications that failed per backend and event.",
	}, []string{"backend", "event"})

	// Uncategorized is the current number of transactions awaiting a category.
	Uncategorized = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "fynanz",
		Subsystem: "categorize",
		Name:      "uncategorized",
		Help:      "Transactions currently waiting for the user to categorize.",
	})
)

func init() {
	prometheus.MustRegister(SyncRuns, TransactionsIngested, AccountsSynced, Uncategorized,
		NotificationsSent, NotificationsFailed)
}

func Handler() http.Handler { return promhttp.Handler() }
