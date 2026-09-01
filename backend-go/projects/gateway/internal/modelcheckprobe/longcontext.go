package modelcheckprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ModelLimitSnapshot is the versioned model-capacity source required before
// long-context probes can be considered evidence. No default window is
// inferred here because a wrong limit changes both load and result semantics.
type ModelLimitSnapshot interface {
	Version() string
	MaxInputTokens(providerCode, model string, protocol modelcheckprofile.Protocol) (int, error)
}

type LongContextDefinition struct {
	Key, Marker       string
	TargetInputTokens int
}

func RunLongContext(ctx context.Context, providerCode, model string, protocol modelcheckprofile.Protocol, tokenizer Tokenizer, limits ModelLimitSnapshot, run func(context.Context, Request) (Result, error), endpointModes ...string) (Evaluation, error) {
	if tokenizer == nil || limits == nil || strings.TrimSpace(tokenizer.Version()) == "" || strings.TrimSpace(limits.Version()) == "" {
		return Evaluation{Kind: "long_context", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "model_limit_snapshot_not_attached"}}, nil
	}
	if strings.TrimSpace(model) == "" || run == nil {
		return Evaluation{}, errors.New("J3b long context input is invalid")
	}
	limit, err := limits.MaxInputTokens(providerCode, model, protocol)
	if err != nil || limit < 8000 {
		return Evaluation{Kind: "long_context", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "model_limit_snapshot_invalid", "limitVersion": limits.Version()}}, nil
	}
	safeLimit := limit - minInt(2048, limit/50)
	high := minInt(safeLimit, maxInt(4000, limit/4))
	medium := minInt(high-1, maxInt(2000, minInt(60000, high*3/5)))
	low := minInt(medium-1, maxInt(1000, minInt(8000, medium/2)))
	definitions := []LongContextDefinition{{"context_low", "NEEDLE-LOW", low}, {"context_medium", "NEEDLE-MEDIUM", medium}, {"context_high", "NEEDLE-HIGH", high}}
	observations := make([]LongContextObservation, 0, len(definitions))
	for _, definition := range definitions {
		definition.Marker += fmt.Sprintf("-%d", definition.TargetInputTokens)
		prompt, buildErr := buildLongContextPrompt(tokenizer, definition)
		if buildErr != nil {
			return Evaluation{}, buildErr
		}
		endpointMode := ""
		if len(endpointModes) > 0 {
			endpointMode = endpointModes[0]
		}
		request, buildErr := buildBasicWithEndpointMode(protocol, model, prompt, modelcheckprofile.EndpointModeIsStreaming(endpointMode), endpointMode)
		if buildErr != nil {
			return Evaluation{}, buildErr
		}
		result, runErr := run(ctx, request)
		if runErr != nil {
			return Evaluation{}, runErr
		}
		observations = append(observations, LongContextObservation{Key: definition.Key, Marker: definition.Marker, TargetInputTokens: definition.TargetInputTokens, Result: result})
	}
	item := EvaluateLongContext(observations, model)
	if item.Evidence == nil {
		item.Evidence = map[string]any{}
	}
	item.Evidence["tokenizerVersion"], item.Evidence["limitVersion"] = tokenizer.Version(), limits.Version()
	return item, nil
}

func buildLongContextPrompt(tokenizer Tokenizer, definition LongContextDefinition) (string, error) {
	markerLine := fmt.Sprintf("关键记录 %s: 隐藏标记是 %s，只输出这个标记。", definition.Key, definition.Marker)
	rule := fmt.Sprintf("回答规则：只输出 key 为 %s 的隐藏标记本身。", definition.Key)
	prefix := fmt.Sprintf("阅读上下文，找到 key 为 %s 的隐藏标记。\n", definition.Key)
	prompt := prefix + markerLine + "\n" + rule
	count, err := tokenizer.Count(prompt)
	if err != nil {
		return "", err
	}
	for count < definition.TargetInputTokens {
		prompt = prefix + strings.Repeat("普通上下文干扰文本。 ", maxInt(1, definition.TargetInputTokens-count)/4) + markerLine + "\n" + rule
		next, countErr := tokenizer.Count(prompt)
		if countErr != nil {
			return "", countErr
		}
		if next <= count {
			break
		}
		count = next
	}
	return prompt, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

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
			if strings.TrimSpace(observation.Result.ObservedModel) != "" && modelMatches(observation.Result.ObservedModel, expectedModel) {
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
