package modelcheckprobe

import "testing"

func TestAnalyzeTokenIntegrityRequiresThreeRounds(t *testing.T) {
	analysis := AnalyzeTokenIntegrity([]TokenSample{{RoundIndex: 0, LocalInputTokens: 10, ReportedInputTokens: intPtr(10)}})
	if analysis.Status != "unsupported" {
		t.Fatalf("analysis=%#v", analysis)
	}
}

func TestAnalyzeTokenIntegrityFlagsBucketRounding(t *testing.T) {
	samples := []TokenSample{
		{RoundIndex: 0, PaddingTokens: 0, LocalInputTokens: 100, ReportedInputTokens: intPtr(100)},
		{RoundIndex: 0, PaddingTokens: 512, LocalInputTokens: 612, ReportedInputTokens: intPtr(640)},
		{RoundIndex: 1, PaddingTokens: 2048, LocalInputTokens: 2148, ReportedInputTokens: intPtr(2176)},
		{RoundIndex: 1, PaddingTokens: 0, LocalInputTokens: 100, ReportedInputTokens: intPtr(100)},
		{RoundIndex: 2, PaddingTokens: 512, LocalInputTokens: 612, ReportedInputTokens: intPtr(640)},
		{RoundIndex: 2, PaddingTokens: 2048, LocalInputTokens: 2148, ReportedInputTokens: intPtr(2176)},
	}
	analysis := AnalyzeTokenIntegrity(samples)
	if analysis.Status != "warning" || len(analysis.ReasonCodes) != 1 || analysis.ReasonCodes[0] != "bucket_rounding" {
		t.Fatalf("analysis=%#v", analysis)
	}
}

func intPtr(value int) *int { return &value }
