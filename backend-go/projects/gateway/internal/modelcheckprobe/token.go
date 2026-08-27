package modelcheckprobe

import (
	"math"
	"sort"
)

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
	status, reasons := "consistent", []string{}
	if math.Abs(slope-1) > 0.05 && (low > 1 || high < 1) {
		status, reasons = "suspected_padding", []string{"proportional_padding"}
	} else if math.Abs(slope-1) > 0.03 {
		status, reasons = "warning", []string{"slope_warning"}
	}
	return TokenAnalysis{Status: status, Slope: round(slope), Intercept: round(intercept), ConfidenceLow: round(low), ConfidenceHigh: round(high), SampleCount: len(valid), RoundCount: len(rounds), ReasonCodes: reasons}
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
