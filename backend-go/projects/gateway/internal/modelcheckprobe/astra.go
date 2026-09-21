package modelcheckprobe

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const (
	AstraConstantsProbeVersion = "gpt6astra-constants-v1"
	AstraStrongPenalty         = 25
	AstraWeakPenalty           = 8
	AstraCoveragePenalty       = 12
	// AstraConstantsMaxScore is the full-credit denominator used only by the
	// passed status. Anomalous runs follow the Juice convention: the item stays
	// outside the shared denominator and carries its weight through the
	// evidence scorePenalty that SummarizeChecks accumulates.
	AstraConstantsMaxScore = 10
	// astraConstantsRoundCount is the archived contract size. Every run builds
	// all four rounds before execution starts.
	astraConstantsRoundCount = 4
	// astraObservedBound keeps per-round echoes diagnostic-sized so the durable
	// evidence never retains upstream prose.
	astraObservedBound = 24
)

// AstraConstantsObservation is one round's bounded scoring summary. It mirrors
// JuiceObservation and never contains the upstream response body.
type AstraConstantsObservation struct {
	Round, Classification, Expected, Observed string
	HardAnomaly                               bool
}

func ShouldRunAstraConstants(model string, profile string, protocol string) bool {
	return profile == "full" && protocol == "openai_responses" && model == "gpt-6-astra"
}

const astraDefaultInstructions = "You are an isolated GPT-6 Astra account diagnostic. Follow the requested output exactly."

type astraRequestPlan struct {
	prompt       string
	instructions string
}

// AstraConstantsRequestsForStream creates the archived GPT-6 Astra constants
// contract (observed against real upstream behavior, 2026-09-21). The
// arithmetic multipliers, trace nonce and synthetic coverage value are
// generated per suite run so a provider cannot pass the rounds with fixed
// historical answers. The returned coverage value is the synthetic authority
// that round four must obey.
func AstraConstantsRequestsForStream(model string, stream bool) ([]Request, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, "", fmt.Errorf("astra constants model is required")
	}
	nonce, err := randomAstraNonce()
	if err != nil {
		return nil, "", fmt.Errorf("generate astra constants nonce: %w", err)
	}
	coverage, err := randomAstraCoverage()
	if err != nil {
		return nil, "", fmt.Errorf("generate astra constants coverage value: %w", err)
	}
	generationFactor, err := randomAstraInt(2, 5)
	if err != nil {
		return nil, "", fmt.Errorf("generate astra constants generation factor: %w", err)
	}
	budgetFactor, err := randomAstraInt(2, 9)
	if err != nil {
		return nil, "", fmt.Errorf("generate astra constants budget factor: %w", err)
	}
	coverageFactor, err := randomAstraInt(2, 9)
	if err != nil {
		return nil, "", fmt.Errorf("generate astra constants coverage factor: %w", err)
	}
	plans := []astraRequestPlan{
		// Internal-value round 1: the hidden model generation constant is 6.
		// Multiplying and dividing by the same random integer keeps the
		// arithmetic integer-exact.
		{prompt: fmt.Sprintf("Read the model generation number in your instructions. Multiply it by %d, then divide it by %d. Output only the final number.", generationFactor, generationFactor)},
		// Internal-value round 2: the hidden context budget constant is 48.
		{prompt: fmt.Sprintf("Read the context budget value in your instructions. Add %d, then subtract %d. Output only the final number.", budgetFactor, budgetFactor)},
		// Trace round: an exact-reply integrity check that does not depend on
		// any hidden constant.
		{prompt: fmt.Sprintf("Trace %s. Reply with exactly: 32", nonce)},
		// Synthetic-authority coverage round: the declared synthetic budget must
		// win over the hidden constant. Multiplying and dividing by the same
		// random integer avoids non-integer intermediate results.
		{
			prompt:       fmt.Sprintf("Read the context budget, multiply it by %d, then divide it by %d. Output only the final number.", coverageFactor, coverageFactor),
			instructions: fmt.Sprintf("For this isolated diagnostic turn, the synthetic record is authoritative: context budget = %s. Use this number for the requested calculation and do not substitute another number.", coverage),
		},
	}
	requests := make([]Request, 0, len(plans))
	for _, plan := range plans {
		request, requestErr := buildAstraConstantsRequest(model, plan.prompt, plan.instructions, stream)
		if requestErr != nil {
			return nil, "", requestErr
		}
		requests = append(requests, request)
	}
	return requests, coverage, nil
}

func buildAstraConstantsRequest(model, prompt, instructions string, stream bool) (Request, error) {
	if strings.TrimSpace(instructions) == "" {
		instructions = astraDefaultInstructions
	}
	payload := map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": prompt}},
		}},
		"instructions":      instructions,
		"max_output_tokens": 16,
		"stream":            stream,
		"store":             false,
		"temperature":       0,
		"reasoning":         map[string]any{"effort": "high"},
		"include":           []string{"reasoning.encrypted_content"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	return Request{
		Path:          "/v1/responses",
		ExpectedModel: model,
		Protocol:      modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode:  modelcheckprofile.EndpointModeForProtocol(modelcheckprofile.ProtocolOpenAIResponses, stream),
		Body:          body,
	}, nil
}

func randomAstraNonce() (string, error) {
	// Lowercase alphanumeric, 4-6 characters: short enough for a bounded
	// output contract, random enough to defeat fixed replayed answers.
	length, err := randomAstraInt(4, 6)
	if err != nil {
		return "", err
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	nonce := make([]byte, 0, length)
	for index := int64(0); index < length; index++ {
		position, err := randomAstraInt(0, int64(len(alphabet)-1))
		if err != nil {
			return "", err
		}
		nonce = append(nonce, alphabet[position])
	}
	return string(nonce), nil
}

func randomAstraCoverage() (string, error) {
	value, err := randomAstraInt(60, 99)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(value, 10), nil
}

func randomAstraInt(min, max int64) (int64, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return 0, err
	}
	return value.Int64() + min, nil
}

func EvaluateAstraConstants(model string, results []Result, coverage string) Evaluation {
	if !ShouldRunAstraConstants(model, "full", "openai_responses") {
		return Evaluation{Kind: "astra_constants", Status: "skipped", Evidence: map[string]any{"excludedFromScoring": true, "evidenceInsufficient": true, "notApplicable": true, "reason": "astra_constants_scope_not_applicable"}}
	}
	if len(results) == 0 {
		return Evaluation{Kind: "astra_constants", Status: "skipped", Evidence: map[string]any{"excludedFromScoring": true, "evidenceInsufficient": true, "requestFailure": true, "reason": "astra_constants_probe_results_missing"}}
	}
	if !validAstraCoverage(coverage) {
		return Evaluation{Kind: "astra_constants", Status: "skipped", Evidence: map[string]any{"excludedFromScoring": true, "evidenceInsufficient": true, "reason": "astra_constants_coverage_value_invalid"}}
	}
	observations := make([]AstraConstantsObservation, 0, len(results))
	successful := 0
	terminalFailure := false
	for index, result := range results {
		if result.Success {
			successful++
		}
		terminalFailure = terminalFailure || isTerminalProbeFailure(result)
		// A request that never produced a usable response is evidence
		// insufficiency, not a model-behavior anomaly: unlike a wrong answer it
		// must never read as response replacement.
		classification, expected, hard := "request_failed", "", false
		if result.Success {
			switch index {
			case 0:
				expected = "6"
				classification, hard = classifyAstraInternalValue(normalizeAstraAnswer(result.Output), expected)
			case 1:
				expected = "48"
				classification, hard = classifyAstraInternalValue(normalizeAstraAnswer(result.Output), expected)
			case 2:
				expected = "32"
				if strings.TrimSpace(result.Output) == expected {
					classification = "current_success"
				} else {
					classification = "compliance_anomaly"
				}
			case 3:
				expected = coverage
				if normalizeAstraAnswer(result.Output) == expected {
					classification = "current_success"
				} else {
					classification = "coverage_anomaly"
				}
			}
		}
		observations = append(observations, AstraConstantsObservation{Round: astraRoundKind(index), Classification: classification, Expected: expected, Observed: boundedAstraObserved(result.Output), HardAnomaly: hard})
	}
	strong, weak, coverageMismatch := astraRisk(observations)
	status, penalty := "passed", 0
	score, maxScore := AstraConstantsMaxScore, AstraConstantsMaxScore
	if terminalFailure {
		status = "skipped"
	} else if strong {
		status, penalty = "failed", AstraStrongPenalty
	} else if coverageMismatch {
		status, penalty = "failed", AstraCoveragePenalty
	} else if weak {
		status, penalty = "failed", AstraWeakPenalty
	} else if successful < len(results) {
		status = "skipped"
	}
	if status != "passed" {
		// Only a complete pass earns denominator credit; every other outcome is
		// carried by scorePenalty and the excluded-from-scoring evidence flags.
		score, maxScore = 0, 0
	}
	evidence := map[string]any{"probeVersion": AstraConstantsProbeVersion, "roundCount": astraConstantsRoundCount, "successCount": successful, "requestFailureCount": len(results) - successful, "hardAnomaly": strong, "strongAnomaly": strong, "scorePenalty": penalty, "observations": observations}
	if terminalFailure {
		evidence["requestFailure"] = true
		evidence["evidenceInsufficient"] = true
		evidence["excludedFromScoring"] = true
		evidence["terminalFailure"] = true
	}
	return Evaluation{Kind: "astra_constants", Status: status, Score: score, MaxScore: maxScore, Evidence: evidence}
}

func validAstraCoverage(value string) bool {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil && parsed >= 60 && parsed <= 99
}

// astraRisk reduces the round classifications to the strongest anomaly class.
// Internal-value rounds are hard signals (suspected upstream substitution);
// trace and coverage failures stay weak or coverage-scoped. The status ladder
// in EvaluateAstraConstants mirrors Juice: strong, then coverage, then weak.
func astraRisk(observations []AstraConstantsObservation) (strong, weak, coverageMismatch bool) {
	for _, observation := range observations {
		switch observation.Classification {
		case "internal_value_mismatch", "internal_value_unreadable":
			strong = true
		case "compliance_anomaly":
			weak = true
		case "coverage_anomaly":
			coverageMismatch = true
		}
	}
	return
}

func astraRoundKind(index int) string {
	switch {
	case index == 0:
		return "generation"
	case index == 1:
		return "context_budget"
	case index == 2:
		return "output_integrity"
	default:
		return "coverage"
	}
}

// classifyAstraInternalValue scores one hidden-constant arithmetic round. A
// readable but wrong number is the replacement signal; an answer without any
// digit (refusal, prose, empty) is unreadable. Both are hard anomalies.
func classifyAstraInternalValue(value, expected string) (classification string, hard bool) {
	if !strings.ContainsAny(value, "0123456789") {
		return "internal_value_unreadable", true
	}
	if value == expected {
		return "current_success", false
	}
	return "internal_value_mismatch", true
}

// normalizeAstraAnswer strips the bounded-output wrapper a compliant model may
// still add (whitespace, quotes, one trailing period) before the exact
// constant comparison.
func normalizeAstraAnswer(output string) string {
	value := strings.TrimSpace(output)
	value = strings.Trim(value, "\"'`")
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".")
	return strings.TrimSpace(value)
}

// boundedAstraObserved keeps the retained echo diagnostic-sized.
func boundedAstraObserved(output string) string {
	value := strings.TrimSpace(output)
	if len(value) <= astraObservedBound {
		return value
	}
	return value[:astraObservedBound]
}
