package gatewayrouting

import "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"

// NormalRouteFirstByteDeadlineAppliesToLane mirrors
// policy/speed-first-lane.ts normalRouteFirstByteDeadlineAppliesToLane.
func NormalRouteFirstByteDeadlineAppliesToLane(lane gatewayproto.RequestLane) bool {
	return lane == gatewayproto.LaneText
}

// NormalRouteSpeedFirstAppliesToLane mirrors
// normalRouteSpeedFirstAppliesToLane.
func NormalRouteSpeedFirstAppliesToLane(lane gatewayproto.RequestLane) bool {
	return NormalRouteFirstByteDeadlineAppliesToLane(lane)
}

// SchedulingPreferenceSpeedFirst mirrors the only scheduling preference the
// normal-route first-byte deadline accepts.
const SchedulingPreferenceSpeedFirst = "speed_first"

// NormalRouteFirstByteRuntimeConfig mirrors
// NormalRouteFirstByteRuntimeConfig.
type NormalRouteFirstByteRuntimeConfig struct {
	SchedulingPreference string
	FirstByteDeadlineMs  int64
}

// NormalRouteAttemptFirstByteDeadlineInput mirrors
// NormalRouteAttemptFirstByteDeadlineInput. Optional precommit deadline and
// final-response reserve stay pointers (JS `?: number`).
type NormalRouteAttemptFirstByteDeadlineInput struct {
	Config                          NormalRouteFirstByteRuntimeConfig
	GatewayRequestWallBudget        *GatewayRequestWallBudget
	AttemptStartedAtMs              int64
	LaneFirstByteTimeoutMs          int64
	UncommittedAttemptMaxLifetimeMs int64
	RequestPrecommitDeadlineAtMs    *int64
	FinalResponseReserveMs          *int64
}

// First-byte deadline limiting factors.
const (
	FirstByteLimitingFactorConfigured         = "configured"
	FirstByteLimitingFactorWallPrecommit      = "wall_precommit"
	FirstByteLimitingFactorUncommittedAttempt = "uncommitted_attempt"
	FirstByteLimitingFactorLaneTimeout        = "lane_timeout"
)

// NormalRouteAttemptFirstByteDeadline mirrors
// NormalRouteAttemptFirstByteDeadline.
type NormalRouteAttemptFirstByteDeadline struct {
	ConfiguredDeadlineMs int64
	EffectiveDeadlineMs  int64
	DeadlineAtMs         int64
	SchedulingPreference string
	Clipped              bool
	LimitingFactor       string
}

// NormalRouteAttemptFirstByteDeadline mirrors
// normalRouteAttemptFirstByteDeadline: freeze one attempt's pre-first-byte
// deadline at dispatch time. Inputs are clamped exactly like the Node
// helpers (timestamps floor at 0, positive durations floor at 1); the error
// mirrors the Node RangeError paths inside GatewayRequestWallBudget
// normalization (e.g. a negative final-response reserve).
func ResolveNormalRouteAttemptFirstByteDeadline(input NormalRouteAttemptFirstByteDeadlineInput) (NormalRouteAttemptFirstByteDeadline, error) {
	attemptStartedAtMs := normalizedDeadlineTimestamp(input.AttemptStartedAtMs)
	configuredDeadlineMs := normalizedDeadlinePositiveMs(input.Config.FirstByteDeadlineMs)
	laneFirstByteTimeoutMs := normalizedDeadlinePositiveMs(input.LaneFirstByteTimeoutMs)
	uncommittedAttemptMaxLifetimeMs := normalizedDeadlinePositiveMs(input.UncommittedAttemptMaxLifetimeMs)
	wallPrecommitRemainingMs, err := input.GatewayRequestWallBudget.PrecommitRemainingMs(PrecommitBudgetInput{
		NowMs:                        &attemptStartedAtMs,
		RequestPrecommitDeadlineAtMs: input.RequestPrecommitDeadlineAtMs,
		FinalResponseReserveMs:       input.FinalResponseReserveMs,
	})
	if err != nil {
		return NormalRouteAttemptFirstByteDeadline{}, err
	}
	type limitingCandidate struct {
		factor string
		value  int64
	}
	candidates := []limitingCandidate{
		{factor: FirstByteLimitingFactorConfigured, value: configuredDeadlineMs},
		{factor: FirstByteLimitingFactorWallPrecommit, value: wallPrecommitRemainingMs},
		{factor: FirstByteLimitingFactorUncommittedAttempt, value: uncommittedAttemptMaxLifetimeMs},
		{factor: FirstByteLimitingFactorLaneTimeout, value: laneFirstByteTimeoutMs},
	}
	limiting := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.value < limiting.value {
			limiting = candidate
		}
	}
	uncommittedAttemptDeadlineAtMs := attemptStartedAtMs + uncommittedAttemptMaxLifetimeMs
	effectiveDeadlineMs, err := input.GatewayRequestWallBudget.ClipFirstByteDeadlineMs(FirstByteDeadlineClipInput{
		NowMs:                          &attemptStartedAtMs,
		FirstByteDeadlineMs:            limiting.value,
		RequestPrecommitDeadlineAtMs:   input.RequestPrecommitDeadlineAtMs,
		FinalResponseReserveMs:         input.FinalResponseReserveMs,
		UncommittedAttemptDeadlineAtMs: &uncommittedAttemptDeadlineAtMs,
	})
	if err != nil {
		return NormalRouteAttemptFirstByteDeadline{}, err
	}

	return NormalRouteAttemptFirstByteDeadline{
		ConfiguredDeadlineMs: configuredDeadlineMs,
		EffectiveDeadlineMs:  effectiveDeadlineMs,
		DeadlineAtMs:         attemptStartedAtMs + effectiveDeadlineMs,
		SchedulingPreference: input.Config.SchedulingPreference,
		Clipped:              effectiveDeadlineMs < configuredDeadlineMs,
		LimitingFactor:       limiting.factor,
	}, nil
}

// normalizedDeadlineTimestamp mirrors the local normalizedTimestamp in
// normal-route-first-byte-deadline.ts: finite timestamps floor at 0
// (non-finite JS values collapse to 0; Go int64 simply clamps negatives).
func normalizedDeadlineTimestamp(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// normalizedDeadlinePositiveMs mirrors the local normalizedPositiveMs in
// normal-route-first-byte-deadline.ts: durations floor at 1.
func normalizedDeadlinePositiveMs(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}
