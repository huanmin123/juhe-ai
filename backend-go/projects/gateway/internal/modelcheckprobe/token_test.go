package modelcheckprobe

import "testing"

func TestAnalyzeTokenIntegrityRequiresThreeRounds(t *testing.T) {
	analysis := AnalyzeTokenIntegrity([]TokenSample{{RoundIndex: 0, LocalInputTokens: 10, ReportedInputTokens: intPtr(10)}})
	if analysis.Status != "unsupported" {
		t.Fatalf("analysis=%#v", analysis)
	}
}

func intPtr(value int) *int { return &value }
