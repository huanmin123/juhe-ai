package modelcheckprobe

import "strings"

type LongContextObservation struct {
	Key, Marker       string
	TargetInputTokens int
	Result            Result
}

func EvaluateLongContext(observations []LongContextObservation, expectedModel string) Evaluation {
	success, matched, needle := 0, 0, 0
	for _, observation := range observations {
		if observation.Result.Success {
			success++
			if observation.Result.ObservedModel == "" || modelMatches(observation.Result.ObservedModel, expectedModel) {
				matched++
			}
			if strings.Contains(strings.ToUpper(observation.Result.Output), strings.ToUpper(observation.Marker)) {
				needle++
			}
		}
	}
	if success == 0 {
		return Evaluation{Kind: "long_context", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true, "probeCount": len(observations)}}
	}
	modelRate, needleRate := float64(matched)/float64(success), float64(needle)/float64(success)
	score := int((modelRate*0.3 + needleRate*0.7) * 15)
	status := "failed"
	if modelRate >= .85 && needleRate >= .85 {
		status = "passed"
	} else if modelRate >= .6 && needleRate >= .6 {
		status = "warning"
	}
	return Evaluation{Kind: "long_context", Status: status, Score: score, MaxScore: 15, Evidence: map[string]any{"probeCount": len(observations), "successRate": float64(success) / float64(maxInt(len(observations), 1)), "modelMatchRate": modelRate, "needleRate": needleRate, "partial": success < len(observations)}}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
