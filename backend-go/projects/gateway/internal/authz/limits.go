package authz

import (
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
)

func normalizeAuthorizationLimitsJSON(value string) (*string, error) {
	limits, err := gatewayquota.ParseRequestQuotaLimitsJSON(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	encoded, ok := gatewayquota.RequestQuotaLimitsJSON(limits)
	if !ok {
		return nil, nil
	}
	return &encoded, nil
}

func canonicalAuthorizationLimits(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return ""
	}
	normalized, err := normalizeAuthorizationLimitsJSON(*value)
	if err != nil || normalized == nil {
		return strings.TrimSpace(*value)
	}
	return *normalized
}

// decodeAuthorizationLimits is used only for response projection. Stored
// rows are validated on write; an invalid legacy row is omitted rather than
// changing the existing read-path error contract.
func decodeAuthorizationLimits(value string) map[string]any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var limits map[string]any
	if err := json.Unmarshal([]byte(value), &limits); err != nil || len(limits) == 0 {
		return nil
	}
	return limits
}
