package app

import (
	"os"
	"strings"
	"testing"
)

func TestServerWiresManagementStatsAccountUsageAndAIPerformanceReaders(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"ManagementStatsAccountUsageHandler:",
		"ManagementMyStatsAccountUsageHandler:",
		"ManagementStatsAccountUsageTrendHandler:",
		"ManagementMyStatsAccountUsageTrendHandler:",
		"ManagementStatsAIPerformanceHandler:",
		"ManagementMyStatsAIPerformanceHandler:",
		"ManagementStatsAIPerformanceAccountsHandler:",
		"ManagementMyStatsAIPerformanceAccountsHandler:",
		"httpapi.NewManagementStatsAccountUsageHandler(statsService)",
		"httpapi.NewManagementMyStatsAccountUsageHandler(statsService)",
		"httpapi.NewManagementStatsAccountUsageTrendHandler(statsService)",
		"httpapi.NewManagementMyStatsAccountUsageTrendHandler(statsService)",
		"httpapi.NewManagementStatsAIPerformanceHandler(statsService)",
		"httpapi.NewManagementMyStatsAIPerformanceHandler(statsService)",
		"httpapi.NewManagementStatsAIPerformanceAccountsHandler(statsService)",
		"httpapi.NewManagementMyStatsAIPerformanceAccountsHandler(statsService)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing management stats wiring %q", required)
		}
	}
}
