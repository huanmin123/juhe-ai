package modelcheckprobe

import "testing"

func tokenIntPtr(value int) *int { return &value }

func TestBuildTokenPaddingUsesInjectedTokenizerAndFailsClosed(t *testing.T) {
	// Use a deterministic counter that mirrors the contract without importing a
	// tokenizer implementation into the jobs binary.
	count := func(value string) int {
		if value == "" {
			return 0
		}
		tokens := 0
		for index := 0; index < len(value); index++ {
			if index+1 < len(value) && value[index] == ' ' && value[index+1] == 'x' {
				tokens++
			}
		}
		return tokens
	}
	prompt, total, err := BuildTokenPadding(3, "prefix", count)
	if err != nil {
		t.Fatalf("BuildTokenPadding: %v", err)
	}
	if prompt != "prefix x x x" || total != 3 {
		t.Fatalf("unexpected padding prompt=%q total=%d", prompt, total)
	}
	if _, _, err := BuildTokenPadding(MaxPaddingTokens+1, "", count); err == nil {
		t.Fatal("expected padding bound failure")
	}
	if _, _, err := BuildTokenPadding(1, "", nil); err == nil {
		t.Fatal("expected nil tokenizer failure")
	}
}

func TestAnalyzeTokenIntegrityMatchesNodeOracleStatuses(t *testing.T) {
	build := func(multiplier float64) []TokenSample {
		var samples []TokenSample
		for round := 0; round < 3; round++ {
			for _, padding := range []int{0, 512, 2048} {
				local := 100 + padding + round
				reported := int(float64(local) * multiplier)
				samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: local, ReportedInputTokens: tokenIntPtr(reported)})
			}
		}
		return samples
	}

	consistent := AnalyzeTokenIntegrity(build(1))
	if consistent.Status != "consistent" || consistent.SampleCount != 9 || consistent.RoundCount != 3 {
		t.Fatalf("consistent analysis=%+v", consistent)
	}

	suspected := AnalyzeTokenIntegrity(build(1.2))
	if suspected.Status != "suspected_padding" || len(suspected.ReasonCodes) != 1 || suspected.ReasonCodes[0] != "proportional_padding" {
		t.Fatalf("suspected analysis=%+v", suspected)
	}

	unsupported := AnalyzeTokenIntegrity(build(1)[:5])
	if unsupported.Status != "unsupported" || unsupported.SampleCount != 5 {
		t.Fatalf("unsupported analysis=%+v", unsupported)
	}

	missing := build(1)
	for index := 0; index < 4; index++ {
		missing[index].ReportedInputTokens = nil
	}
	missingAnalysis := AnalyzeTokenIntegrity(missing)
	if missingAnalysis.Status != "unsupported" || missingAnalysis.SampleCount != 5 {
		t.Fatalf("missing analysis=%+v", missingAnalysis)
	}
}

func TestAnalyzeTokenIntegrityDetectsBucketRounding(t *testing.T) {
	var samples []TokenSample
	for round := 0; round < 3; round++ {
		for _, padding := range []int{0, 512, 2048} {
			local := 128 + padding + round
			reported := ((local + 63) / 64) * 64
			samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: local, ReportedInputTokens: tokenIntPtr(reported)})
		}
	}
	analysis := AnalyzeTokenIntegrity(samples)
	if analysis.Status != "warning" || !containsReason(analysis.ReasonCodes, "bucket_rounding") {
		t.Fatalf("bucket analysis=%+v", analysis)
	}
}

func containsReason(reasons []string, expected string) bool {
	for _, reason := range reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
