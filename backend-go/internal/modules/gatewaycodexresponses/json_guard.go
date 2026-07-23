package gatewaycodexresponses

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"juhe-ai/backend-go/internal/protocols/codexresponses"
)

const (
	DefaultJSONMaxBytes int64 = 16 << 20
	JSONMaxBytes        int64 = 64 << 20
)

var (
	ErrJSONBodyTooLarge        = errors.New("Codex Responses JSON body 超过限制")
	ErrJSONInvalid             = codexresponses.ErrInvalidJSON
	ErrJSONUnsupportedMode     = errors.New("Codex Responses JSON guard mode 无效")
	ErrJSONProvenance          = errors.New("Codex Responses JSON guard provenance 无效")
	ErrJSONUnsupportedEnvelope = errors.New("Codex Responses JSON envelope 无效")
)

type JSONEnvelopeKind string

const (
	JSONEnvelopeResponse JSONEnvelopeKind = "response"
	JSONEnvelopeCompact  JSONEnvelopeKind = "compact"
)

type JSONOptions struct {
	Mode         codexresponses.Mode
	Provenance   codexresponses.Provenance
	EnvelopeKind JSONEnvelopeKind
	Commit       codexresponses.CommitState
	MaxBytes     int64
	CreateItemID func(prefix, itemType string, sequence, outputIndex int) string
}

type JSONResult struct {
	Revision          string
	Mode              codexresponses.Mode
	Provenance        codexresponses.Provenance
	EnvelopeKind      JSONEnvelopeKind
	Outcome           codexresponses.Outcome
	Validation        codexresponses.ValidationResult
	Issues            []codexresponses.Issue
	OmittedIssueCount int
	Retryable         bool
	RepairRuleIDs     []string
	Commit            codexresponses.CommitState
	Body              []byte
	Changed           bool
}

// InspectJSON validates one already-bounded non-stream response. Safe repair is
// deliberately limited to typed output item IDs at explicit response checkpoints.
func InspectJSON(raw []byte, options JSONOptions) (JSONResult, error) {
	options = normalizeJSONOptions(options)
	if options.Mode != codexresponses.ModeShadow && options.Mode != codexresponses.ModeSafeRepair && options.Mode != codexresponses.ModeStrictIntercept {
		return JSONResult{}, fmt.Errorf("%w: %s", ErrJSONUnsupportedMode, options.Mode)
	}
	if options.Provenance != codexresponses.ProvenanceRawUpstream && options.Provenance != codexresponses.ProvenanceGatewayBridge && options.Provenance != codexresponses.ProvenanceRequestHistory {
		return JSONResult{}, fmt.Errorf("%w: %s", ErrJSONProvenance, options.Provenance)
	}
	if options.EnvelopeKind != JSONEnvelopeResponse && options.EnvelopeKind != JSONEnvelopeCompact {
		return JSONResult{}, fmt.Errorf("%w: %s", ErrJSONUnsupportedEnvelope, options.EnvelopeKind)
	}
	if int64(len(raw)) > options.MaxBytes {
		return JSONResult{}, fmt.Errorf("%w: limit=%d", ErrJSONBodyTooLarge, options.MaxBytes)
	}
	document, err := decodeJSONObject(raw)
	if err != nil {
		return JSONResult{}, err
	}
	validation := validateJSONEnvelope(document, options)
	if validation.Outcome == codexresponses.OutcomeClean {
		validation = codexresponses.Validate(document, options.Provenance)
	}
	result := newJSONResult(raw, validation, options)
	if result.Outcome == codexresponses.OutcomeLateViolation || options.Mode != codexresponses.ModeSafeRepair || validation.Outcome != codexresponses.OutcomeRepairable {
		return result, nil
	}
	if options.Provenance != codexresponses.ProvenanceRawUpstream && options.Provenance != codexresponses.ProvenanceGatewayBridge {
		return failedJSONRepair(result, options, "safe_repair_provenance_forbidden", false), nil
	}
	repaired, ruleIDs, err := repairJSONDocument(document, validation, options)
	if err != nil {
		return failedJSONRepair(result, options, "safe_repair_failed", options.Commit.CanRetryUpstream()), nil
	}
	body, err := json.Marshal(repaired)
	if err != nil {
		return JSONResult{}, fmt.Errorf("%w: encode repaired body: %v", ErrJSONInvalid, err)
	}
	if int64(len(body)) > options.MaxBytes {
		return JSONResult{}, fmt.Errorf("%w: repaired limit=%d", ErrJSONBodyTooLarge, options.MaxBytes)
	}
	postValidation := codexresponses.Validate(repaired, options.Provenance)
	for _, issue := range postValidation.Issues {
		if issue.RepairLevel != codexresponses.RepairNone {
			return failedJSONRepair(result, options, "safe_repair_post_validation_failed", options.Commit.CanRetryUpstream()), nil
		}
	}
	result.Body = body
	result.Changed = true
	result.Retryable = false
	result.RepairRuleIDs = ruleIDs
	if options.Provenance == codexresponses.ProvenanceGatewayBridge {
		result.Outcome = codexresponses.OutcomeRepairedBridge
	} else {
		result.Outcome = codexresponses.OutcomeRepairedSafe
	}
	return result, nil
}

func normalizeJSONOptions(options JSONOptions) JSONOptions {
	if options.Mode == "" {
		options.Mode = codexresponses.ModeShadow
	}
	if options.EnvelopeKind == "" {
		options.EnvelopeKind = JSONEnvelopeResponse
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultJSONMaxBytes
	}
	if options.MaxBytes > JSONMaxBytes {
		options.MaxBytes = JSONMaxBytes
	}
	return options
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJSONInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing JSON value", ErrJSONInvalid)
		}
		return nil, fmt.Errorf("%w: %v", ErrJSONInvalid, err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, ErrJSONInvalid
	}
	return document, nil
}

func validateJSONEnvelope(document map[string]any, options JSONOptions) codexresponses.ValidationResult {
	_, outputArray := document["output"].([]any)
	valid := false
	switch options.EnvelopeKind {
	case JSONEnvelopeResponse:
		valid = document["object"] == "response" && outputArray
	case JSONEnvelopeCompact:
		object, hasObject := document["object"]
		valid = outputArray && (!hasObject || object == "response.compaction")
	}
	if valid {
		return codexresponses.ValidationResult{Revision: codexresponses.Revision, Provenance: options.Provenance, Outcome: codexresponses.OutcomeClean}
	}
	return codexresponses.ValidationResult{
		Revision:   codexresponses.Revision,
		Provenance: options.Provenance,
		Outcome:    codexresponses.OutcomeBlocked,
		Issues: []codexresponses.Issue{{
			Code: "response_envelope_invalid", Message: "Codex Responses contract issue: response_envelope_invalid",
			Provenance: options.Provenance, OutputIndex: -1, RepairLevel: codexresponses.RepairR2,
		}},
	}
}

func newJSONResult(raw []byte, validation codexresponses.ValidationResult, options JSONOptions) JSONResult {
	outcome := codexresponses.OutcomeAtCommit(validation.Outcome, options.Commit)
	return JSONResult{
		Revision:          codexresponses.Revision,
		Mode:              options.Mode,
		Provenance:        options.Provenance,
		EnvelopeKind:      options.EnvelopeKind,
		Outcome:           outcome,
		Validation:        validation,
		Issues:            cloneJSONIssues(validation.Issues),
		OmittedIssueCount: validation.OmittedIssueCount,
		Retryable:         validation.Outcome == codexresponses.OutcomeBlocked && options.Commit.CanRetryUpstream(),
		Commit:            options.Commit,
		Body:              append([]byte(nil), raw...),
	}
}

func failedJSONRepair(result JSONResult, options JSONOptions, code string, retryable bool) JSONResult {
	issue := codexresponses.Issue{
		Code: code, Message: "Codex Responses contract issue: " + code,
		Provenance: options.Provenance, OutputIndex: -1, RepairLevel: codexresponses.RepairR2,
	}
	if len(result.Issues) < codexresponses.DiagnosticLimit {
		result.Issues = append(result.Issues, issue)
	} else {
		result.OmittedIssueCount++
	}
	result.Outcome = codexresponses.OutcomeBlocked
	result.Retryable = retryable
	result.RepairRuleIDs = nil
	result.Changed = false
	return result
}

func repairJSONDocument(document map[string]any, validation codexresponses.ValidationResult, options JSONOptions) (map[string]any, []string, error) {
	field, rawItems, err := repairCollection(document)
	if err != nil {
		return nil, nil, err
	}
	output := make(map[string]any, len(document))
	for key, value := range document {
		output[key] = value
	}
	items := append([]any(nil), rawItems...)
	output[field] = items
	existingIDs := collectJSONItemIDs(rawItems)
	registry := codexresponses.NewRegistry()
	planned := make(map[int]struct{})
	sequence := 1
	for _, issue := range validation.Issues {
		if issue.RepairLevel != codexresponses.RepairR0 {
			continue
		}
		index, item, err := jsonRepairTarget(issue, field, rawItems)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := planned[index]; exists {
			continue
		}
		contract, ok := registry.Item(issue.ItemType)
		if !ok || contract.Prefix == "" || !containsJSONIDPath(contract.RepairableIDPaths) {
			return nil, nil, errors.New("item type is not safely repairable")
		}
		generated := ""
		for attempt := 0; attempt < 100 && generated == ""; attempt++ {
			candidate := createJSONItemID(options, contract.Prefix, issue.ItemType, sequence, index)
			sequence++
			if len(candidate) <= 256 && validGeneratedJSONID(candidate, contract.Prefix) {
				if _, collision := existingIDs[candidate]; !collision {
					generated = candidate
				}
			}
		}
		if generated == "" {
			return nil, nil, errors.New("item ID generation failed")
		}
		existingIDs[generated] = struct{}{}
		copyItem := make(map[string]any, len(item))
		for key, value := range item {
			copyItem[key] = value
		}
		copyItem["id"] = generated
		items[index] = copyItem
		planned[index] = struct{}{}
	}
	if len(planned) == 0 {
		return nil, nil, errors.New("no safe repair available")
	}
	return output, []string{"codex.r0.response.replace_item_id"}, nil
}

func repairCollection(document map[string]any) (string, []any, error) {
	if items, ok := document["output"].([]any); ok {
		return "output", items, nil
	}
	return "", nil, errors.New("response output collection is missing")
}

func jsonRepairTarget(issue codexresponses.Issue, field string, items []any) (int, map[string]any, error) {
	if len(issue.Path) != 3 || issue.Path[0] != field || issue.Path[2] != "id" {
		return 0, nil, errors.New("invalid repair path")
	}
	index, ok := issue.Path[1].(int)
	if !ok || index < 0 || index >= len(items) {
		return 0, nil, errors.New("invalid repair index")
	}
	item, ok := items[index].(map[string]any)
	if !ok || item["type"] != issue.ItemType {
		return 0, nil, errors.New("stale repair target")
	}
	if issue.Code != "item_id_invalid" && issue.Code != "item_id_prefix_mismatch" {
		return 0, nil, errors.New("issue is not R0 allowlisted")
	}
	return index, item, nil
}

func collectJSONItemIDs(items []any) map[string]struct{} {
	result := make(map[string]struct{})
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			if id, ok := item["id"].(string); ok && id != "" {
				result[id] = struct{}{}
			}
		}
	}
	return result
}

func createJSONItemID(options JSONOptions, prefix, itemType string, sequence, outputIndex int) string {
	if options.CreateItemID != nil {
		return options.CreateItemID(prefix, itemType, sequence, outputIndex)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return ""
	}
	return fmt.Sprintf("%s_%x", prefix, token[:])
}

func validGeneratedJSONID(candidate, prefix string) bool {
	return len(candidate) > len(prefix)+1 && bytes.HasPrefix([]byte(candidate), []byte(prefix+"_"))
}

func containsJSONIDPath(paths []string) bool {
	for _, path := range paths {
		if path == "id" {
			return true
		}
	}
	return false
}

func cloneJSONIssues(issues []codexresponses.Issue) []codexresponses.Issue {
	result := make([]codexresponses.Issue, len(issues))
	for index, issue := range issues {
		result[index] = issue
		result[index].Path = append([]any(nil), issue.Path...)
	}
	return result
}
