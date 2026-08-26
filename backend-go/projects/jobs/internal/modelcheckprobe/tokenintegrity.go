package modelcheckprobe

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	TokenProbeVersion = "token-integrity-v1"
	TokenizerVersion  = "js-tiktoken@1.0.21:o200k_base"
	MaxPaddingTokens  = 2048
)

type TokenSample struct {
	RoundIndex          int
	PaddingTokens       int
	LocalInputTokens    int
	ReportedInputTokens *int
	CachedInputTokens   *int
}

type TokenAnalysis struct {
	Status              string   `json:"status"`
	Slope               float64  `json:"slope"`
	Intercept           float64  `json:"intercept"`
	SlopeConfidenceLow  float64  `json:"slopeConfidenceLow"`
	SlopeConfidenceHigh float64  `json:"slopeConfidenceHigh"`
	SampleCount         int      `json:"sampleCount"`
	RoundCount          int      `json:"roundCount"`
	ReasonCodes         []string `json:"reasonCodes"`
}

func BuildTokenPadding(target int, prefix string, countTokens func(string) int) (string, int, error) {
	if target < 0 || target > MaxPaddingTokens || countTokens == nil {
		return "", 0, fmt.Errorf("invalid token padding target")
	}
	prefixTokens := countTokens(prefix)
	padding := strings.Repeat(" x", target)
	prompt := prefix + padding
	if countTokens(prompt)-prefixTokens != target {
		return "", 0, fmt.Errorf("token padding is not exact")
	}
	return prompt, countTokens(prompt), nil
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
		return emptyTokenAnalysis(len(valid), len(rounds), "reported_usage_missing")
	}
	reg := tokenRegression(valid)
	if !isFinite(reg.slope) || reg.slope <= 0.1 {
		return emptyTokenAnalysis(len(valid), len(rounds), "reported_usage_incompatible")
	}
	status := "consistent"
	reasons := []string{}
	if math.Abs(reg.slope-1) > 0.05 && (reg.low > 1 || reg.high < 1) {
		status = "suspected_padding"
		reasons = append(reasons, "proportional_padding")
	} else if math.Abs(reg.slope-1) > 0.03 {
		status = "warning"
		reasons = append(reasons, "slope_warning")
	}
	if status == "consistent" && detectsTokenBucket(valid) {
		status = "warning"
		reasons = append(reasons, "bucket_rounding")
	}
	return TokenAnalysis{Status: status, Slope: roundToken(reg.slope), Intercept: roundToken(reg.intercept), SlopeConfidenceLow: roundToken(reg.low), SlopeConfidenceHigh: roundToken(reg.high), SampleCount: len(valid), RoundCount: len(rounds), ReasonCodes: reasons}
}

type tokenRegressionResult struct{ slope, intercept, low, high float64 }

func tokenRegression(samples []TokenSample) tokenRegressionResult {
	points := append([]TokenSample(nil), samples...)
	sort.Slice(points, func(i, j int) bool { return points[i].LocalInputTokens < points[j].LocalInputTokens })
	meanX, meanY := 0.0, 0.0
	for _, p := range points {
		meanX += float64(p.LocalInputTokens)
		meanY += float64(*p.ReportedInputTokens)
	}
	meanX /= float64(len(points))
	meanY /= float64(len(points))
	ssX, cov, residual := 0.0, 0.0, 0.0
	for _, p := range points {
		dx := float64(p.LocalInputTokens) - meanX
		dy := float64(*p.ReportedInputTokens) - meanY
		ssX += dx * dx
		cov += dx * dy
	}
	slope := math.NaN()
	if ssX > 0 {
		slope = cov / ssX
	}
	intercept := meanY - slope*meanX
	for _, p := range points {
		d := float64(*p.ReportedInputTokens) - (intercept + slope*float64(p.LocalInputTokens))
		residual += d * d
	}
	se := math.Inf(1)
	if len(points) > 2 && ssX > 0 {
		se = math.Sqrt((residual / float64(len(points)-2)) / ssX)
	}
	margin := 1.96 * se
	return tokenRegressionResult{slope: slope, intercept: intercept, low: slope - margin, high: slope + margin}
}
func detectsTokenBucket(samples []TokenSample) bool {
	nonBase := 0
	aligned := 0
	for _, s := range samples {
		if s.PaddingTokens > 0 && s.ReportedInputTokens != nil {
			nonBase++
			if *s.ReportedInputTokens%64 == 0 {
				aligned++
			}
		}
	}
	return nonBase >= 4 && float64(aligned)/float64(nonBase) >= 0.8
}

func emptyTokenAnalysis(sampleCount, roundCount int, reason string) TokenAnalysis {
	return TokenAnalysis{Status: "unsupported", SampleCount: sampleCount, RoundCount: roundCount, ReasonCodes: []string{reason}}
}
func roundToken(value float64) float64 {
	if !isFinite(value) {
		return value
	}
	return math.Round(value*1e6) / 1e6
}
func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
