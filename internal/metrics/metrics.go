package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// OperatorUp is a gauge metric that is always 1 when the operator is running.
	// Absence of this metric indicates the operator process is dead.
	// This metric is exposed on the /metrics endpoint (default :8080/metrics)
	// and should be scraped by Prometheus for alerting on operator availability.
	OperatorUp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "dbupgrade_operator_up",
			Help: "Indicates if the DBUpgrade operator is running (always 1 when process is alive)",
		},
	)
)

func init() {
	// Register custom metrics with controller-runtime's metrics registry
	metrics.Registry.MustRegister(OperatorUp)
}

// SetOperatorUp sets the operator up metric to 1 to indicate the process is alive.
// This should be called once at startup after successful initialization.
func SetOperatorUp() {
	OperatorUp.Set(1)
}
