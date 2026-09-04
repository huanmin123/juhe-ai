package gatewayusage

import "fmt"

// OpenAIGatewayTrafficSource mirrors the Node OpenAIGatewayTrafficSource
// union ('gateway' | 'manual_account_test' | ...).
type OpenAIGatewayTrafficSource = string

// Traffic source values (traffic-source.ts, order preserved).
const (
	TrafficSourceGateway              OpenAIGatewayTrafficSource = "gateway"
	TrafficSourceManualAccountTest    OpenAIGatewayTrafficSource = "manual_account_test"
	TrafficSourceAccountHealthCheck   OpenAIGatewayTrafficSource = "account_health_check"
	TrafficSourceRuntimeRecoveryProbe OpenAIGatewayTrafficSource = "runtime_recovery_probe"
	TrafficSourceCooldownRetest       OpenAIGatewayTrafficSource = "cooldown_retest"
	TrafficSourceHybridScoring        OpenAIGatewayTrafficSource = "hybrid_scoring"
	TrafficSourceHybridQualityScoring OpenAIGatewayTrafficSource = "hybrid_quality_scoring"
)

// NormalizeOpenAIGatewayTrafficSource mirrors
// normalizeOpenAIGatewayTrafficSource: undefined input falls back to
// 'gateway'; unknown values throw 非法网关流量来源.
func NormalizeOpenAIGatewayTrafficSource(value any) (OpenAIGatewayTrafficSource, error) {
	if value == nil {
		return TrafficSourceGateway, nil
	}
	switch text := value.(type) {
	case string:
		if isValidTrafficSource(text) {
			return text, nil
		}
	}
	return "", fmt.Errorf("非法网关流量来源：%s", displayString(value))
}

func isValidTrafficSource(value string) bool {
	switch value {
	case TrafficSourceGateway,
		TrafficSourceManualAccountTest,
		TrafficSourceAccountHealthCheck,
		TrafficSourceRuntimeRecoveryProbe,
		TrafficSourceCooldownRetest,
		TrafficSourceHybridScoring,
		TrafficSourceHybridQualityScoring:
		return true
	}
	return false
}

// IsCooldownRetestTrafficSource mirrors isCooldownRetestTrafficSource.
func IsCooldownRetestTrafficSource(value any) bool {
	normalized, err := NormalizeOpenAIGatewayTrafficSource(value)
	return err == nil && normalized == TrafficSourceCooldownRetest
}

// IsAccountProbeTrafficSource mirrors isAccountProbeTrafficSource:
// account_health_check / runtime_recovery_probe / cooldown_retest.
func IsAccountProbeTrafficSource(value any) bool {
	normalized, err := NormalizeOpenAIGatewayTrafficSource(value)
	if err != nil {
		return false
	}
	return normalized == TrafficSourceAccountHealthCheck ||
		normalized == TrafficSourceRuntimeRecoveryProbe ||
		normalized == TrafficSourceCooldownRetest
}

// IsAccountDiagnosticTrafficSource mirrors isAccountDiagnosticTrafficSource:
// manual_account_test plus every account probe source.
func IsAccountDiagnosticTrafficSource(value any) bool {
	normalized, err := NormalizeOpenAIGatewayTrafficSource(value)
	if err != nil {
		return false
	}
	return normalized == TrafficSourceManualAccountTest ||
		normalized == TrafficSourceAccountHealthCheck ||
		normalized == TrafficSourceRuntimeRecoveryProbe ||
		normalized == TrafficSourceCooldownRetest
}

// displayString mirrors Node String(value) for the error copy.
func displayString(value any) string {
	switch text := value.(type) {
	case nil:
		return "undefined"
	case string:
		return text
	case fmt.Stringer:
		return text.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}
