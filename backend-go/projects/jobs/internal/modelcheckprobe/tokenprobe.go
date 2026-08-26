package modelcheckprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// TokenProbeInput is the Go equivalent of Node's token differential probe.
// Token counting is injected so the jobs binary can pin the same tokenizer as
// the caller without importing a second, potentially divergent implementation.
type TokenProbeInput struct {
	Model       string
	Protocol    modelcheckprofile.Protocol
	ProfileMode string
	Prefix      string
	Stream      bool
	CountTokens func(string) int
	RunProbe    func(context.Context, Request) (ProbeResult, error)
}

type TokenProbeRun struct {
	Item    EvaluationItem
	Samples []TokenSample
}

// RunTokenIntegrity executes the fixed Node-compatible 0/512/2048 padding
// matrix. A terminal non-200 retry result stops only this probe; callers may
// continue unrelated probes and preserve this item's skipped evidence.
func RunTokenIntegrity(ctx context.Context, input TokenProbeInput) (TokenProbeRun, error) {
	if input.RunProbe == nil || input.CountTokens == nil || input.Model == "" {
		return TokenProbeRun{}, fmt.Errorf("token integrity probe input is invalid")
	}
	rounds := 3
	if input.ProfileMode == "quick" {
		rounds = 1
	}
	orders := [][]int{{0, 512, 2048}, {2048, 0, 512}, {512, 2048, 0}}
	samples := make([]TokenSample, 0, rounds*3)
	var representative, last ProbeResult
	terminal := false
	for round := 0; round < rounds && !terminal; round++ {
		prefix := input.Prefix
		if prefix == "" {
			prefix = fmt.Sprintf("Controlled token integrity probe %s. Nonce %s. Reply with exactly OK.\n", TokenProbeVersion, tokenNonce())
		}
		for _, padding := range orders[round%len(orders)] {
			prompt, local, err := BuildTokenPadding(padding, prefix, input.CountTokens)
			if err != nil {
				return TokenProbeRun{}, err
			}
			request, err := BuildBasic(input.Protocol, input.Model, prompt, BasicOptions{MaxOutputTokens: 8, Stream: input.Stream})
			if err != nil {
				return TokenProbeRun{}, err
			}
			result, err := input.RunProbe(ctx, request)
			if err != nil {
				return TokenProbeRun{}, err
			}
			if representative.TraceID == "" {
				representative = result
			}
			last = result
			reported := usageInt(result.Response.Usage, "input_tokens", "prompt_tokens")
			cached := usageInt(result.Response.Usage, "cached_tokens")
			samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: local, ReportedInputTokens: reported, CachedInputTokens: cached})
			if isTerminalProbeResult(result) {
				terminal = true
				break
			}
		}
	}
	analysis := AnalyzeTokenIntegrity(samples)
	status, score, maxScore := "skipped", 0, 0
	if analysis.Status == "consistent" {
		status, score, maxScore = "passed", 10, 10
	} else if analysis.Status == "suspected_padding" {
		status, maxScore = "failed", 10
	} else if analysis.Status == "warning" {
		status = "warning"
	}
	evidence := map[string]any{"message": tokenAnalysisMessage(analysis.Status), "diagnosticOnly": maxScore == 0, "tokenizerVersion": TokenizerVersion, "probeVersion": TokenProbeVersion, "slope": analysis.Slope, "intercept": analysis.Intercept, "slopeConfidenceLow": analysis.SlopeConfidenceLow, "slopeConfidenceHigh": analysis.SlopeConfidenceHigh, "sampleCount": analysis.SampleCount, "roundCount": analysis.RoundCount, "reasonCodes": analysis.ReasonCodes, "httpStatus": last.HTTPStatusCode}
	return TokenProbeRun{Item: EvaluationItem{ItemKey: "target.token_integrity", ItemType: "token_integrity", Status: status, Score: score, MaxScore: maxScore, TraceID: representative.TraceID, DurationMS: last.DurationMS, Evidence: evidence}, Samples: samples}, nil
}

func tokenNonce() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "jobs-token"
	}
	return hex.EncodeToString(raw[:])
}

func usageInt(usage map[string]any, keys ...string) *int {
	if usage == nil {
		return nil
	}
	for _, key := range keys {
		if result := nonNegativeUsageInt(usage[key]); result != nil {
			return result
		}
	}
	// OpenAI Responses/Chat and Gemini may place the same counters in
	// protocol-specific detail objects. Only numeric counters are extracted.
	for _, detailKey := range []string{"input_tokens_details", "prompt_tokens_details", "output_tokens_details", "completion_tokens_details", "usageMetadata"} {
		if details, ok := usage[detailKey].(map[string]any); ok {
			if result := usageInt(details, keys...); result != nil {
				return result
			}
		}
	}
	return nil
}

func nonNegativeUsageInt(value any) *int {
	switch value := value.(type) {
	case float64:
		if value >= 0 {
			result := int(value)
			return &result
		}
	case int:
		if value >= 0 {
			result := value
			return &result
		}
	case int64:
		if value >= 0 {
			result := int(value)
			return &result
		}
	}
	return nil
}

func tokenAnalysisMessage(status string) string {
	switch status {
	case "consistent":
		return "受控差分 Token 探针未发现比例或分桶异常"
	case "suspected_padding":
		return "受控差分 Token 探针发现疑似比例灌水"
	case "warning":
		return "受控差分 Token 探针存在需要继续校准的异常"
	default:
		return "上游 usage 不完整或不兼容，暂不支持形成 Token 诚信结论"
	}
}
