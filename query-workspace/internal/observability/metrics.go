package observability

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type QueryLatencies struct {
	generation         prometheus.Observer
	execution          prometheus.Observer
	generationFailures prometheus.Counter
	executionFailures  prometheus.Counter
}

func NewQueryLatencies(registry prometheus.Registerer) (*QueryLatencies, error) {
	generation := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "query_workspace_generation_duration_seconds",
		Help:    "Duration of Vertex AI query generation requests.",
		Buckets: prometheus.DefBuckets,
	})
	execution := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "query_workspace_execution_duration_seconds",
		Help:    "Duration of Neo4j query execution requests.",
		Buckets: prometheus.DefBuckets,
	})
	generationFailures := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "query_workspace_generation_failures_total",
		Help: "Number of failed Vertex AI query generation requests.",
	})
	executionFailures := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "query_workspace_execution_failures_total",
		Help: "Number of failed Neo4j query execution requests.",
	})
	if err := registry.Register(generation); err != nil {
		return nil, fmt.Errorf("registering generation latency metric: %w", err)
	}
	if err := registry.Register(execution); err != nil {
		return nil, fmt.Errorf("registering execution latency metric: %w", err)
	}
	if err := registry.Register(generationFailures); err != nil {
		return nil, fmt.Errorf("registering generation failure metric: %w", err)
	}
	if err := registry.Register(executionFailures); err != nil {
		return nil, fmt.Errorf("registering execution failure metric: %w", err)
	}
	return &QueryLatencies{
		generation:         generation,
		execution:          execution,
		generationFailures: generationFailures,
		executionFailures:  executionFailures,
	}, nil
}

func (m *QueryLatencies) ObserveGeneration(duration time.Duration) {
	m.generation.Observe(duration.Seconds())
}

func (m *QueryLatencies) ObserveExecution(duration time.Duration) {
	m.execution.Observe(duration.Seconds())
}

func (m *QueryLatencies) GenerationFailed() {
	m.generationFailures.Inc()
}

func (m *QueryLatencies) ExecutionFailed() {
	m.executionFailures.Inc()
}
