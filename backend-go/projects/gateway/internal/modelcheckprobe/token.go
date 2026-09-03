package modelcheckprobe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// Tokenizer is an explicit, versioned snapshot dependency. The probe layer
// never guesses token counts with rune/byte lengths because that would change
// the Node o200k_base evidence semantics.
type Tokenizer interface {
	Version() string
	Count(string) (int, error)
}

// RunTokenIntegrity executes the same bounded differential sequence as Node.
// It returns an excluded/skipped item when usage is incomplete; callers must
// not turn that state into a formed quality fact.
func RunTokenIntegrity(ctx context.Context, protocol modelcheckprofile.Protocol, model string, tokenizer Tokenizer, run func(context.Context, Request) (Result, error), endpointModes ...string) (Evaluation, error) {
	return runTokenIntegrity(ctx, protocol, model, tokenizer, run, 3, endpointModes...)
}

func runTokenIntegrity(ctx context.Context, protocol modelcheckprofile.Protocol, model string, tokenizer Tokenizer, run func(context.Context, Request) (Result, error), rounds int, endpointModes ...string) (Evaluation, error) {
	if tokenizer == nil || strings.TrimSpace(tokenizer.Version()) == "" {
		return Evaluation{Kind: "token_integrity", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "tokenizer_snapshot_not_attached"}}, nil
	}
	if strings.TrimSpace(model) == "" || run == nil {
		return Evaluation{}, errors.New("J3b token integrity input is invalid")
	}
	if rounds < 1 {
		return Evaluation{}, errors.New("J3b token integrity rounds must be positive")
	}
	samples := make([]TokenSample, 0, rounds*3)
	results := make([]Result, 0, rounds*3)
	terminalFailure := false
	for round := 0; round < rounds && !terminalFailure; round++ {
		nonce, nonceErr := randomTokenNonce()
		if nonceErr != nil {
			return Evaluation{}, fmt.Errorf("generate J3b token nonce: %w", nonceErr)
		}
		prefix := fmt.Sprintf("Controlled token integrity probe token-integrity-v1. Nonce %s. Reply with exactly OK.\n", nonce)
		for _, padding := range tokenPaddingOrder(round) {
			prompt, localTokens, err := buildTokenPrompt(tokenizer, prefix, padding)
			if err != nil {
				return Evaluation{}, err
			}
			endpointMode := ""
			if len(endpointModes) > 0 {
				endpointMode = endpointModes[0]
			}
			request, err := buildBasicWithTunings(protocol, model, prompt, endpointMode, modelcheckprofile.EndpointModeIsStreaming(endpointMode), 16, 0)
			if err != nil {
				return Evaluation{}, err
			}
			result, err := run(ctx, request)
			if err != nil {
				return Evaluation{}, err
			}
			results = append(results, result)
			var reported *int
			if value := usageInteger(result.Usage, "input_tokens", "prompt_tokens"); value >= 0 {
				reported = &value
			}
			samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: localTokens, ReportedInputTokens: reported})
			// A malformed HTTP 200 is quality evidence for this sample, not a
			// transport boundary. Stop only when the retry-aware terminal gate
			// says the family can no longer produce comparable observations.
			if isTerminalProbeFailure(result) {
				terminalFailure = true
				break
			}
		}
	}
	analysis := AnalyzeTokenIntegrity(samples)
	lastResult := results[len(results)-1]
	status := "skipped"
	score, maxScore := 0, 0
	switch analysis.Status {
	case "consistent":
		status, score, maxScore = "passed", 10, 10
	case "suspected_padding":
		status, maxScore = "failed", 10
	case "warning":
		// Node keeps warning token diagnostics outside the score denominator.
		status = "warning"
	}
	return Evaluation{Kind: "token_integrity", Status: status, Score: score, MaxScore: maxScore, Evidence: map[string]any{
		"tokenizerVersion": tokenizer.Version(), "probeVersion": "token-integrity-v1", "slope": analysis.Slope,
		"intercept": analysis.Intercept, "confidenceLow": analysis.ConfidenceLow, "confidenceHigh": analysis.ConfidenceHigh,
		"sampleCount": analysis.SampleCount, "roundCount": analysis.RoundCount, "reasonCodes": analysis.ReasonCodes,
		"requestCount": len(results), "partial": len(results) < rounds*3, "terminalFailure": terminalFailure,
		"httpStatus": lastResult.HTTPStatus, "success": lastResult.Success,
	}}, nil
}

func randomTokenNonce() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(9_000_000))
	if err != nil {
		return "", err
	}
	value.Add(value, big.NewInt(1_000_000))
	return strings.ToUpper(strconv.FormatInt(value.Int64(), 36)), nil
}

func tokenPaddingOrder(round int) []int {
	orders := [][3]int{{0, 512, 2048}, {2048, 0, 512}, {512, 2048, 0}}
	order := orders[round%len(orders)]
	return []int{order[0], order[1], order[2]}
}

func buildTokenPrompt(tokenizer Tokenizer, prefix string, target int) (string, int, error) {
	if target < 0 || target > 2048 {
		return "", 0, errors.New("J3b token padding target is out of range")
	}
	prefixTokens, err := tokenizer.Count(prefix)
	if err != nil {
		return "", 0, fmt.Errorf("count token prefix: %w", err)
	}
	padding := ""
	for count := 0; count <= target+8; count++ {
		candidate := prefix + padding
		local, countErr := tokenizer.Count(candidate)
		if countErr != nil {
			return "", 0, fmt.Errorf("count token prompt: %w", countErr)
		}
		if local-prefixTokens == target {
			return candidate, local, nil
		}
		if local-prefixTokens > target {
			return "", 0, fmt.Errorf("tokenizer cannot construct exact %d-token padding", target)
		}
		padding += " x"
	}
	return "", 0, fmt.Errorf("tokenizer cannot construct exact %d-token padding", target)
}

func usageInteger(usage map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := usage[key].(float64); ok && value >= 0 && value == math.Trunc(value) {
			return int(value)
		}
		if value, ok := usage[key].(int); ok && value >= 0 {
			return value
		}
	}
	return -1
}

type TokenSample struct {
	RoundIndex, PaddingTokens, LocalInputTokens int
	ReportedInputTokens                         *int
}
type TokenAnalysis struct {
	Status                                          string
	Slope, Intercept, ConfidenceLow, ConfidenceHigh float64
	SampleCount, RoundCount                         int
	ReasonCodes                                     []string
}

func AnalyzeTokenIntegrity(samples []TokenSample) TokenAnalysis {
	valid := make([]TokenSample, 0, len(samples))
	rounds := map[int]struct{}{}
	for _, sample := range samples {
		if sample.LocalInputTokens >= 0 && sample.ReportedInputTokens != nil && *sample.ReportedInputTokens >= 0 {
			valid = append(valid, sample)
			rounds[sample.RoundIndex] = struct{}{}
		}
	}
	if len(valid) < 6 || len(rounds) < 3 {
		return TokenAnalysis{Status: "unsupported", SampleCount: len(valid), RoundCount: len(rounds), ReasonCodes: []string{"reported_usage_missing"}}
	}
	slope, intercept, low, high := tokenRegression(valid)
	if math.IsNaN(slope) || math.IsInf(slope, 0) || slope <= 0.1 {
		return TokenAnalysis{Status: "unsupported", SampleCount: len(valid), RoundCount: len(rounds), ReasonCodes: []string{"reported_usage_incompatible"}}
	}
	bucketRounding := detectsBucketRounding(valid)
	status, reasons := "consistent", []string{}
	if math.Abs(slope-1) > 0.05 && (low > 1 || high < 1) {
		status, reasons = "suspected_padding", []string{"proportional_padding"}
	} else if math.Abs(slope-1) > 0.03 {
		status, reasons = "warning", []string{"slope_warning"}
	}
	if bucketRounding {
		if status == "consistent" {
			status = "warning"
		}
		reasons = append(reasons, "bucket_rounding")
	}
	return TokenAnalysis{Status: status, Slope: round(slope), Intercept: round(intercept), ConfidenceLow: round(low), ConfidenceHigh: round(high), SampleCount: len(valid), RoundCount: len(rounds), ReasonCodes: reasons}
}

// detectsBucketRounding mirrors the Node token-integrity oracle. Providers
// that report padded inputs in coarse 64-token buckets are retained as
// negative evidence instead of being accepted as fully consistent.
func detectsBucketRounding(samples []TokenSample) bool {
	nonBase := make([]TokenSample, 0, len(samples))
	for _, sample := range samples {
		if sample.PaddingTokens > 0 && sample.ReportedInputTokens != nil {
			nonBase = append(nonBase, sample)
		}
	}
	if len(nonBase) < 4 {
		return false
	}
	aligned := 0
	for _, sample := range nonBase {
		if *sample.ReportedInputTokens%64 == 0 {
			aligned++
		}
	}
	return float64(aligned)/float64(len(nonBase)) >= 0.8
}

func tokenRegression(samples []TokenSample) (float64, float64, float64, float64) {
	points := append([]TokenSample(nil), samples...)
	sort.Slice(points, func(i, j int) bool { return points[i].LocalInputTokens < points[j].LocalInputTokens })
	meanX, meanY := 0.0, 0.0
	for _, point := range points {
		meanX += float64(point.LocalInputTokens)
		meanY += float64(*point.ReportedInputTokens)
	}
	meanX /= float64(len(points))
	meanY /= float64(len(points))
	ssX, cov, residual := 0.0, 0.0, 0.0
	for _, point := range points {
		dx := float64(point.LocalInputTokens) - meanX
		dy := float64(*point.ReportedInputTokens) - meanY
		ssX += dx * dx
		cov += dx * dy
	}
	slope := math.NaN()
	if ssX > 0 {
		slope = cov / ssX
	}
	intercept := meanY - slope*meanX
	for _, point := range points {
		d := float64(*point.ReportedInputTokens) - (intercept + slope*float64(point.LocalInputTokens))
		residual += d * d
	}
	se := math.Inf(1)
	if len(points) > 2 && ssX > 0 {
		se = math.Sqrt((residual / float64(len(points)-2)) / ssX)
	}
	margin := 1.96 * se
	return slope, intercept, slope - margin, slope + margin
}

func round(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return math.Round(value*1e6) / 1e6
}
