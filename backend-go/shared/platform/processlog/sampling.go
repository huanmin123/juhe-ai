package processlog

import (
	"fmt"
	"strconv"
	"strings"
)

// TimingDetailSamplePermilleEnvName mirrors
// JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE (Node config/runtime.ts:894,
// default 50‰ in performance mode and 1000‰ otherwise).
const TimingDetailSamplePermilleEnvName = "JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE"

// DefaultTimingDetailSamplePermillePerformance /
// DefaultTimingDetailSamplePermilleFull mirror the Node numberConfig fallback
// (configuredRuntimeMode === 'performance' ? 50 : 1000).
const (
	DefaultTimingDetailSamplePermillePerformance = 50
	DefaultTimingDetailSamplePermilleFull        = 1000
)

// StageDetailGate is the compiled shouldWriteGatewayStageDetail policy
// (shared/request-context.ts:318-340): the outcome whitelist first, then the
// performance-mode gate, then the pending-log pressure gate and finally the
// stable traceId sampling decision.
type StageDetailGate struct {
	// PerformanceMode mirrors runtimeConfig.runtimeMode === 'performance'.
	PerformanceMode bool
	// SamplePermille mirrors
	// runtimeConfig.log.gatewayTimingDetailSamplePermille (0..1000).
	SamplePermille int
}

// LoadStageDetailGate resolves the performance mode and the sampling permille
// the way Node builds runtimeConfig.log.gatewayTimingDetailSamplePermille:
// empty falls back to 50‰ (performance) / 1000‰ (standalone), a non-numeric
// or out-of-range 0..1000 value fails startup (numberConfig contract).
func LoadStageDetailGate(getenv func(string) string, performanceMode bool) (StageDetailGate, error) {
	raw := ""
	if getenv != nil {
		raw = strings.TrimSpace(getenv(TimingDetailSamplePermilleEnvName))
	}
	permille := DefaultTimingDetailSamplePermilleFull
	if performanceMode {
		permille = DefaultTimingDetailSamplePermillePerformance
	}
	if raw != "" {
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return StageDetailGate{}, fmt.Errorf("%s 必须配置为数字", TimingDetailSamplePermilleEnvName)
		}
		permille = int(value)
		if float64(permille) != value {
			return StageDetailGate{}, fmt.Errorf("%s 必须配置为数字", TimingDetailSamplePermilleEnvName)
		}
		if permille < 0 || permille > 1000 {
			return StageDetailGate{}, fmt.Errorf("%s 必须在 0-1000 范围内", TimingDetailSamplePermilleEnvName)
		}
	}
	return StageDetailGate{PerformanceMode: performanceMode, SamplePermille: permille}, nil
}

// TimingDetailSampled mirrors isGatewayTimingDetailSampled: outside
// performance mode every request is sampled; inside, the traceId decides via
// a stable FNV-1a (UTF-16 code unit) hash against the permille budget.
func (g StageDetailGate) TimingDetailSampled(traceID string) bool {
	if !g.PerformanceMode {
		return true
	}
	if g.SamplePermille <= 0 {
		return false
	}
	if g.SamplePermille >= 1000 {
		return true
	}
	return uint32(fnv1aUTF16(traceID))%1000 < uint32(g.SamplePermille)
}

// ShouldWriteStageDetail mirrors shouldWriteGatewayStageDetail:
// unexpected_failure / expected_failure / aborted always write; standalone
// mode writes everything; performance mode applies the pending-log pressure
// gate and the traceId sampling gate in the Node order. Go keeps the pressure
// parameter for order fidelity — the in-process log publisher queue of Node
// does not exist here, so the composition passes 0 (the gate never trips).
func (g StageDetailGate) ShouldWriteStageDetail(outcome, traceID string, pendingLogBytes, pressureMaxPendingBytes int64) bool {
	switch outcome {
	case "unexpected_failure", "expected_failure", "aborted":
		return true
	}
	if !g.PerformanceMode {
		return true
	}
	if pressureMaxPendingBytes > 0 && pendingLogBytes >= pressureMaxPendingBytes {
		return false
	}
	return g.TimingDetailSampled(traceID)
}

// fnv1aUTF16 mirrors the Node FNV-1a loop over charCodeAt (UTF-16 code
// units), so a hash computed by either runtime for the same traceId agrees.
func fnv1aUTF16(value string) uint32 {
	hash := uint32(2166136261)
	for _, r := range value {
		if r >= 0x10000 {
			// Surrogate pair, exactly the two code units charCodeAt returns.
			adjusted := r - 0x10000
			hash ^= uint32(0xD800 + (adjusted >> 10))
			hash *= 16777619
			hash ^= uint32(0xDC00 + (adjusted & 0x3FF))
			hash *= 16777619
			continue
		}
		hash ^= uint32(r)
		hash *= 16777619
	}
	return hash
}
