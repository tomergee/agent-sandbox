package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	SandboxClaimReadyLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sandbox_claim_ready_latency_seconds",
			Help:    "Latency from claim creation to readiness in seconds",
			Buckets: prometheus.DefBuckets, // Uses default buckets which are suitable for typical sub-second/seconds latencies.
		},
		[]string{"namespace"}, // We can segment by namespace if needed
	)
	SandboxClaimCreatedLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sandbox_claim_created_latency_seconds",
			Help:    "Latency from claim creation in API to controller creating the Sandbox",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace"},
	)
	SandboxCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sandbox_created_total",
			Help: "Total number of Sandbox resources created",
		},
		[]string{"namespace"},
	)
	SandboxClaimCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sandbox_claim_created_total",
			Help: "Total number of SandboxClaim resources created",
		},
		[]string{"namespace"},
	)
)

func init() {
	// Register the custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(
		SandboxClaimReadyLatency,
		SandboxClaimCreatedLatency,
		SandboxCreatedTotal,
		SandboxClaimCreatedTotal,
	)
}
