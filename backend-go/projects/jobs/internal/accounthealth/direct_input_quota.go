package accounthealth

import (
	"encoding/json"
	"fmt"
)

// DirectQuotaLimits mirrors the frozen authorization limits JSON. Unknown or
// malformed enabled limits are rejected by the reader rather than interpreted
// as unlimited access.
type DirectQuotaLimits struct {
	Hourly  *DirectHourlyQuotaLimit `json:"hourly"`
	Daily   *DirectQuotaLimit       `json:"daily"`
	Weekly  *DirectQuotaLimit       `json:"weekly"`
	Monthly *DirectQuotaLimit       `json:"monthly"`
	Total   *DirectQuotaLimit       `json:"total"`
}

type DirectQuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Limit   float64 `json:"limit"`
}

type DirectHourlyQuotaLimit struct {
	DirectQuotaLimit
	Hours int `json:"hours"`
}

type DirectQuotaCosts struct {
	Hourly  float64
	Daily   float64
	Weekly  float64
	Monthly float64
	Total   float64
}

func ParseDirectQuotaLimits(raw string) (DirectQuotaLimits, error) {
	if raw == "" {
		return DirectQuotaLimits{}, nil
	}
	var limits DirectQuotaLimits
	if err := json.Unmarshal([]byte(raw), &limits); err != nil {
		return DirectQuotaLimits{}, fmt.Errorf("解析 authorization limits 失败")
	}
	if err := validateDirectQuotaLimits(limits); err != nil {
		return DirectQuotaLimits{}, err
	}
	return limits, nil
}

func DirectQuotaExceeded(limits DirectQuotaLimits, costs DirectQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit) ||
		(limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit) ||
		(limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit) ||
		(limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit) ||
		(limits.Total != nil && limits.Total.Enabled && costs.Total >= limits.Total.Limit)
}

func validateDirectQuotaLimits(limits DirectQuotaLimits) error {
	for _, entry := range []*DirectQuotaLimit{limits.Daily, limits.Weekly, limits.Monthly, limits.Total} {
		if entry != nil && entry.Enabled && (entry.Limit < 0 || entry.Limit != entry.Limit) {
			return fmt.Errorf("authorization quota limit 无效")
		}
	}
	if limits.Hourly != nil && limits.Hourly.Enabled && (limits.Hourly.Hours < 1 || limits.Hourly.Hours > 720 || limits.Hourly.Limit < 0 || limits.Hourly.Limit != limits.Hourly.Limit) {
		return fmt.Errorf("authorization hourly quota limit 无效")
	}
	return nil
}
