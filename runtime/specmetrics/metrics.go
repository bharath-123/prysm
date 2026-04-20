// Package specmetrics exposes the Prometheus histogram used to time
// consensus-spec functions implemented in Prysm. Call sites time themselves
// directly against SpecFunctionDuration — there is intentionally no helper
// wrapper, so the metric emission is visible inline wherever it happens.
//
// Typical use at the top of a spec function:
//
//	start := time.Now()
//	defer func() {
//	    specmetrics.SpecFunctionDuration.
//	        WithLabelValues("process_block", version.String(state.Version())).
//	        Observe(time.Since(start).Seconds())
//	}()
package specmetrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SpecFunctionDuration is the histogram of wall-clock latencies for
// consensus-spec functions. Labels: "function" (snake_case spec name) and
// "fork" (phase0/altair/.../gloas, from runtime/version.String).
// Buckets: 10ms, 100ms, 500ms, 1s, +Inf.
var SpecFunctionDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "prysm_spec_function_duration_seconds",
		Help:    "Wall-clock latency of consensus-spec functions, labeled by spec function name and fork.",
		Buckets: []float64{0.010, 0.100, 0.500, 1.000},
	},
	[]string{"function", "fork"},
)
