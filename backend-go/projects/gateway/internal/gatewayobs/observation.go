package gatewayobs

import (
	"context"
	"errors"
)

// 路由观测事件模型，逐字段对齐
// backend/src/modules/gateway/observability/routing-observability-store.ts。

// 观测事件 kind 常量（GatewayRoutingObservation 判别字段）。
const (
	KindCircuitTransition  = "circuit_transition"
	KindCircuitMutation    = "circuit_mutation"
	KindCircuitDispatch    = "circuit_dispatch"
	KindHotQualityMutation = "hot_quality_mutation"
	KindExploration        = "exploration"
	KindTierEscape         = "tier_escape"
	KindAttempt            = "attempt"
	KindBudget             = "budget"
)

// MaxSafeInteger mirrors Number.MAX_SAFE_INTEGER: all Node counters saturate
// here instead of overflowing.
const MaxSafeInteger int64 = 9007199254740991

// GatewayRoutingObservabilityMetricCapacity mirrors
// gatewayRoutingObservabilityMetricCapacity.
const GatewayRoutingObservabilityMetricCapacity = 512

var (
	errPositiveCount = errors.New("routing observability count 必须是正安全整数")
	errNormalizedNow = errors.New("routing observability nowMs 必须是非负安全整数")
)

// Observation mirrors the GatewayRoutingObservation union: one struct with a
// Kind discriminator plus the union's per-variant fields. Only the fields of
// the active variant carry meaning, exactly like the Node union.
type Observation struct {
	Kind      string // Kind* 常量
	Outcome   string // attempt / circuit_dispatch / exploration / tier_escape / budget
	Phase     string // circuit_dispatch: 'SUSPECT' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'
	Operation string // circuit_mutation / hot_quality_mutation
	Status    string // circuit_mutation / hot_quality_mutation
	LeaseKind string // circuit_mutation 可选: 'confirmation' | 'half_open' | 'recovery'
	From      string // circuit_transition
	To        string // circuit_transition
	Source    string // circuit_transition: 'transport' | 'explicit_policy' | 'recovery' | 'configuration'
}

// RoutingObservation mirrors gatewayhotquality.RoutingObservation (the
// no-ctx hot-quality observer port payload).
type RoutingObservation struct {
	Kind      string // 'attempt' | 'hot_quality_mutation' | 'exploration'
	Outcome   string
	Operation string // hot_quality_mutation only
	Status    string // hot_quality_mutation only
}

// BatchEntry mirrors GatewayRoutingObservationBatchEntry.
type BatchEntry struct {
	Observation Observation
	Count       int64
}

// Snapshot mirrors GatewayRoutingObservabilitySnapshot; the JSON shape
// ({"version":1,"recordedEvents":..,"updatedAtMs":..,"counters":{..}}) is the
// diagnostic contract.
type Snapshot struct {
	Version        int              `json:"version"`
	RecordedEvents int64            `json:"recordedEvents"`
	UpdatedAtMs    int64            `json:"updatedAtMs"`
	Counters       map[string]int64 `json:"counters"`
}

// Store mirrors GatewayRoutingObservabilityStore.
type Store interface {
	Record(ctx context.Context, observation Observation, nowMs int64) error
	RecordBatch(ctx context.Context, entries []BatchEntry, nowMs int64) error
	Snapshot(ctx context.Context) (Snapshot, error)
}

// GatewayRoutingObservationMetricKey mirrors
// gatewayRoutingObservationMetricKey byte for byte.
func GatewayRoutingObservationMetricKey(observation Observation) string {
	switch observation.Kind {
	case KindCircuitTransition:
		return "circuit.transition." + lowerASCII(observation.From) + "." + lowerASCII(observation.To) + "." + observation.Source
	case KindCircuitMutation:
		key := "circuit.mutation." + observation.Operation + "." + observation.Status
		if observation.LeaseKind != "" {
			key += "." + observation.LeaseKind
		}
		return key
	case KindCircuitDispatch:
		return "circuit.dispatch." + observation.Outcome + "." + lowerASCII(observation.Phase)
	case KindHotQualityMutation:
		return "hot_quality." + observation.Operation + "." + observation.Status
	case KindExploration:
		return "exploration." + observation.Outcome
	case KindTierEscape:
		return "tier_escape." + observation.Outcome
	case KindAttempt:
		return "attempt." + observation.Outcome
	case KindBudget:
		return "budget." + observation.Outcome
	default:
		return ""
	}
}

func lowerASCII(value string) string {
	// JS toLowerCase() over the ASCII phase/state literals used here.
	out := []byte(value)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}

// saturatedAdd mirrors saturatedAdd: clamp at Number.MAX_SAFE_INTEGER.
func saturatedAdd(left, right int64) int64 {
	sum := left + right
	if sum > MaxSafeInteger || sum < left { // beyond safe range or int64 overflow
		return MaxSafeInteger
	}
	return sum
}

// isSafeInteger mirrors Number.isSafeInteger.
func isSafeInteger(value int64) bool {
	return value >= -MaxSafeInteger && value <= MaxSafeInteger
}

// positiveCount mirrors positiveCount.
func positiveCount(value int64) (int64, error) {
	if !isSafeInteger(value) || value <= 0 {
		return 0, errPositiveCount
	}
	return value, nil
}

// normalizedNow mirrors normalizedNow.
func normalizedNow(value int64) (int64, error) {
	if !isSafeInteger(value) || value < 0 {
		return 0, errNormalizedNow
	}
	return value, nil
}
