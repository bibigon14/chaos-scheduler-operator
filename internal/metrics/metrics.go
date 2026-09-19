// Package metrics exposes Prometheus counters and gauges for the operator
// itself, so runs, aborts, and guardrail latency are observable from the
// same Prometheus that reconciles the CRs against.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// RunsTotal counts every attempted run of a ChaosExperiment, broken down
	// by outcome. Skipped runs (suspended, not due) are not counted here to
	// keep the denominator meaningful for burn-rate style dashboards.
	RunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "chaos_experiment_runs_total",
			Help: "Total number of ChaosExperiment runs by result.",
		},
		[]string{"namespace", "name", "result"},
	)

	// GuardrailCheckDuration measures how long the guardrail PromQL query
	// takes end-to-end (HTTP + parse). Useful to catch a slow Prometheus
	// becoming the reason experiments miss their window.
	GuardrailCheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "chaos_experiment_guardrail_check_duration_seconds",
			Help:    "Latency of guardrail Prometheus queries.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace", "name", "outcome"},
	)

	// LastRunTimestamp is set to the Unix time of the most recent run per
	// experiment, so `time() - chaos_experiment_last_run_timestamp` gives
	// staleness for alerting.
	LastRunTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "chaos_experiment_last_run_timestamp",
			Help: "Unix timestamp of the last run per experiment.",
		},
		[]string{"namespace", "name"},
	)
)

func init() {
	metrics.Registry.MustRegister(RunsTotal, GuardrailCheckDuration, LastRunTimestamp)
}
