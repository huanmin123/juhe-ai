package gatewaysession

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

// SchedulingPolicyValues is the closed two-key ordinary-group policy. The
// retired high_concurrency fields are gone; -1/0 mean disabled.
type SchedulingPolicyValues = gatewayscheduling.Policy

// SchedulingDefaults is retained for the affinity service constructor; the
// two-key policy no longer has globalMax-backed defaults.
type SchedulingDefaults struct {
	GlobalMax int64
}

// ResolveGroupSchedulingPolicy parses the two-key policy. nil means the
// default (both disabled).
func ResolveGroupSchedulingPolicy(value *gatewayscheduling.Policy) (*SchedulingPolicyValues, error) {
	if value == nil {
		policy := gatewayscheduling.DefaultPolicy()
		return &policy, nil
	}
	policy := *value
	return &policy, nil
}

// EffectiveSoftConcurrencyLimit returns the effective soft threshold for an
// account hard limit; 0 means disabled.
func EffectiveSoftConcurrencyLimit(accountConcurrencyLimit int64, policy *gatewayscheduling.Policy) (int64, error) {
	resolved, err := ResolveGroupSchedulingPolicy(policy)
	if err != nil {
		return 0, err
	}
	return int64(resolved.SoftLimit(int(accountConcurrencyLimit))), nil
}
