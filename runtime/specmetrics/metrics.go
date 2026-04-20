// Package specmetrics provides Prometheus histograms for measuring the
// runtime of consensus-spec functions implemented in Prysm. The intent is to
// make it easy to compare hot-path timings across forks and, eventually,
// across client implementations.
//
// Typical usage, at the top of a spec function:
//
//	defer specmetrics.Observe("process_block", state.Version())()
package specmetrics

import (
	"time"

	"github.com/OffchainLabs/prysm/v7/runtime/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var specFunctionDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "prysm_spec_function_duration_seconds",
		Help: "Wall-clock latency of consensus-spec functions, labeled by spec function name and fork.",
		// Buckets requested by the benchmarking effort: (0,10ms], (10ms,100ms],
		// (100ms,500ms], (500ms,1s], (1s,+Inf). +Inf is implicit.
		Buckets: []float64{0.010, 0.100, 0.500, 1.000},
	},
	[]string{"function", "fork"},
)

// Observe starts a timer and returns a closure that records the elapsed time
// when invoked. The canonical pattern is:
//
//	defer specmetrics.Observe("process_block", state.Version())()
//
// function should be the snake_case name used in the consensus-specs (e.g.
// "process_block", "process_epoch"). fork is a version constant from
// runtime/version (version.Phase0, version.Altair, ...).
func Observe(function string, fork int) func() {
	start := time.Now()
	return func() {
		specFunctionDuration.
			WithLabelValues(function, version.String(fork)).
			Observe(time.Since(start).Seconds())
	}
}
