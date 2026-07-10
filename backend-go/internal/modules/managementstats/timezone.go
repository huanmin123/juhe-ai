package managementstats

import (
	"time"

	"juhe-ai/backend-go/internal/timezonecompat"
)

func loadUsageStatsLocation(name string) (*time.Location, error) {
	return timezonecompat.LoadNodeLocation(name)
}

func canonicalIANATimezoneName(name string) string {
	return timezonecompat.CanonicalIANAName(name)
}
