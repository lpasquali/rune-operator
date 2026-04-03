package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	once sync.Once

	ReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rune",
		Subsystem: "operator",
		Name:      "reconcile_total",
		Help:      "Total reconcile loops by result.",
	}, []string{"result"})

	RunDurationMillis = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "rune",
		Subsystem: "operator",
		Name:      "run_duration_millis",
		Help:      "Duration of completed benchmark runs in milliseconds.",
		Buckets:   []float64{100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 300000},
	})

	ActiveSchedules = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "rune",
		Subsystem: "operator",
		Name:      "active_schedules",
		Help:      "Number of active non-suspended RuneBenchmark resources.",
	})
)

func Register() {
	once.Do(func() {
		ctrlmetrics.Registry.MustRegister(ReconcileTotal, RunDurationMillis, ActiveSchedules)
	})
}
