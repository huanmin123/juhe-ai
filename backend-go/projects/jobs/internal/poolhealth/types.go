// Package poolhealth implements the independent proxy-pool availability probe
// (docs/functions/代理池设计.md §7.2): one fixed high-quota 204 endpoint, one
// probe per referenced proxy every 60s, 5s timeout, batch 128/concurrency 8.
// It writes PG pool_health_* (fact source) and the shared Redis member-health
// projection read by the gateway (C7-R). It never consumes or rewrites the
// J3a vendor matrix state.
package poolhealth

import "time"

const (
	// DefaultEndpoint is the fixed high-quota 204 endpoint. The probe only
	// answers "can this proxy carry an HTTP request", not vendor reachability.
	DefaultEndpoint = "https://cp.cloudflare.com/generate_204"
	// DefaultProbeTimeout is the per-probe timeout (§7.2).
	DefaultProbeTimeout = 5 * time.Second
	// DefaultBatchSize / DefaultConcurrency are the §7.2 round limits.
	DefaultBatchSize   = 128
	DefaultConcurrency = 8
	// FailureThreshold is the consecutive-failure count entering dead.
	FailureThreshold = 3
	// HealthKeyTTL is the Redis projection TTL (refreshed every probe).
	HealthKeyTTL = 5 * time.Minute
	// DueInterval is the per-proxy probe cadence.
	DueInterval = 60 * time.Second
)

// HealthState mirrors proxy_profiles.pool_health_status.
type HealthState string

const (
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded"
	HealthDead     HealthState = "dead"
	HealthUnknown  HealthState = "unknown"
)

// Target is the proxy row the probe needs.
type Target struct {
	ProfileID         string
	Name              string
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
}

// RecordResult is the projected outcome of one completed probe.
type RecordResult struct {
	ProfileID    string
	Status       HealthState
	Streak       int
	Transitioned bool
	// Skipped means the row changed (connection config or enabled) while the
	// probe was in flight, so the snapshot result was discarded.
	Skipped bool
}
