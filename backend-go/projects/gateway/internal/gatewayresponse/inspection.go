package gatewayresponse

import (
	"sort"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 响应检查策略匹配，对齐 inspection.ts。

// ResponseInspectionPolicySource 对齐 union。
const (
	PolicySourceAccount      = "account"
	PolicySourceManagement   = "management"
	PolicySourceSystemDefault = "system_default"
)

// textMatchSnippetChars 对齐 textMatchSnippetChars。
const textMatchSnippetChars = 160

// CodexCompactionContractPolicyID 对齐 codexCompactionContractPolicyId。
const CodexCompactionContractPolicyID = "default_codex_compaction_contract"

// RuntimeResponseInspectionPolicy 对齐 RuntimeResponseInspectionPolicy。
type RuntimeResponseInspectionPolicy struct {
	ID           string
	Source       string
	Name         string
	Enabled      bool
	Priority     int
	ScopeType    string
	ProtocolCode string
	ProviderCode string
	Match        gatewayruntimecache.ResponseInspectionPolicyMatch
	Action       string
	// ExecutionMode / DataHandling / RetryEnabled / AccountSwitch / AccountState
	// 由 responseInspectionPolicyActionRuntime 展开（这里直接承载）。
	ExecutionMode  string
	DataHandling   string
	RetryEnabled   bool
	AccountSwitch  string
	AccountState   string
}

// ResponseInspectionRuntimeContext 对齐 ResponseInspectionRuntimeContext。
type ResponseInspectionRuntimeContext struct {
	ClientProfile             string
	AccountClientCompatibility string
	CodexCompactionExpected   bool
}

// ResponseInspectionDecision 对齐 ResponseInspectionDecision。
type ResponseInspectionDecision struct {
	Reason               string // 'configured_response_policy' | 'before_downstream_write_response_failure'
	Action               string // 'client_retry' | 'discard_event' | 'discard_response' | 'replace_with_failure' | 'dry_run'
	Transport            string // 'json' | 'sse'
	TriggerPhase         string // 'before_downstream_write' | 'after_downstream_write'
	EndpointFamily       gatewayproto.ResponseEndpointFamily
	FrameType            string
	UpstreamEventType    string
	UpstreamErrorCode    string
	UpstreamErrorType    string
	UpstreamErrorMessage string
	FinishReason         string
	ClientProfile        string
	CodexCompactionExpected bool
	RewriteErrorCode     string
	RewriteMessage       string
	DownstreamWritten    bool
	PolicyID             string
	PolicyName           string
	PolicySource         string
	ReplayAuthority      string // '' | 'explicit_user_policy' | 'system_default_retry_next_account'
	PolicyScopeType      string
	PolicyProtocolCode   string
	PolicyProviderCode   string
	ExecutionMode        string
	DataHandling         string
	RetryEnabled         bool
	AccountSwitch        string
	AccountState         string
	MatchedField         string
	MatchedValue         string
	MatchedSnippet       string
}

// ResponseInspectionResult 对齐 ResponseInspectionResult。
type ResponseInspectionResult struct {
	Decision     *ResponseInspectionDecision
	Observations []ResponseInspectionDecision
}

// ResponseInspectionFailurePayload 对齐 ResponseInspectionFailurePayload。
type ResponseInspectionFailurePayload struct {
	ErrorCode string
	Message   string
}

// ResponseInspectionPolicyActionRuntime 对齐
// responseInspectionPolicyActionRuntime：由 action 派生运行时字段。
// action → {executionMode, dataHandling, retryEnabled, accountSwitch, accountState}。
type PolicyRuntime struct {
	ExecutionMode string
	DataHandling  string
	RetryEnabled  bool
	AccountSwitch string
	AccountState  string
}

// ResolvePolicyRuntime 对齐 responseInspectionPolicyActionRuntime。action 取值
// 与 ResponseInspectionPolicySummary['action'] 一致。
func ResolvePolicyRuntime(action string) PolicyRuntime {
	switch action {
	case "retry_next_account":
		return PolicyRuntime{ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true, AccountSwitch: "request_next_account"}
	case "avoid_account_ttl":
		return PolicyRuntime{ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true, AccountSwitch: "avoid_account_ttl"}
	case "avoid_upstream_bucket_ttl":
		return PolicyRuntime{ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true, AccountSwitch: "avoid_upstream_bucket_ttl"}
	case "runtime_avoidance":
		return PolicyRuntime{ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true, AccountState: "runtime_avoidance"}
	case "observe":
		return PolicyRuntime{ExecutionMode: "dry_run", DataHandling: "passthrough"}
	default:
		return PolicyRuntime{ExecutionMode: "enforce", DataHandling: "replace_with_failure"}
	}
}

// AccountResponseInspectionRule 对齐账户凭据里的 response_inspection_rules 条目
//（normalizeAccountResponseInspectionRules 的输出形状）。
type AccountResponseInspectionRule struct {
	Name         string
	Enabled      bool
	Priority     int
	Match        gatewayruntimecache.ResponseInspectionPolicyMatch
	Action       string
}

// ResolveRuntimeResponseInspectionPolicies 对齐
// resolveRuntimeResponseInspectionPolicies。accountProvider/protocol 与
// responseInspectionRules 由调用方投影；管理策略已含 defaultRule 标记。
func ResolveRuntimeResponseInspectionPolicies(accountProtocolCode string, accountProviderCode string, accountRules []AccountResponseInspectionRule, managementPolicies []gatewayruntimecache.ResponseInspectionPolicySummary) []RuntimeResponseInspectionPolicy {
	policies := make([]RuntimeResponseInspectionPolicy, 0, len(accountRules)+len(managementPolicies))
	for index, rule := range accountRules {
		runtime := ResolvePolicyRuntime(rule.Action)
		policies = append(policies, RuntimeResponseInspectionPolicy{
			ID:            "account_rule_" + strconv.Itoa(index+1),
			Source:        PolicySourceAccount,
			Name:          rule.Name,
			Enabled:       rule.Enabled,
			Priority:      rule.Priority,
			ScopeType:     "provider",
			ProtocolCode:  orOpenAIProtocol(accountProtocolCode),
			ProviderCode:  accountProviderCode,
			Match:         rule.Match,
			Action:        rule.Action,
			ExecutionMode: runtime.ExecutionMode,
			DataHandling:  runtime.DataHandling,
			RetryEnabled:  runtime.RetryEnabled,
			AccountSwitch: runtime.AccountSwitch,
			AccountState:  runtime.AccountState,
		})
	}
	for _, policy := range managementPolicies {
		if !policyMatchesAccountScope(policy, accountProtocolCode, accountProviderCode) {
			continue
		}
		source := PolicySourceManagement
		if policy.DefaultRule {
			source = PolicySourceSystemDefault
		}
		runtime := ResolvePolicyRuntime(policy.Action)
		providerCode := ""
		if policy.ProviderCode != nil {
			providerCode = *policy.ProviderCode
		}
		policies = append(policies, RuntimeResponseInspectionPolicy{
			ID:            policy.ID,
			Source:        source,
			Name:          policy.Name,
			Enabled:       policy.Enabled,
			Priority:      policy.Priority,
			ScopeType:     policy.ScopeType,
			ProtocolCode:  policy.ProtocolCode,
			ProviderCode:  providerCode,
			Match:         policy.Match,
			Action:        policy.Action,
			ExecutionMode: runtime.ExecutionMode,
			DataHandling:  runtime.DataHandling,
			RetryEnabled:  runtime.RetryEnabled,
			AccountSwitch: runtime.AccountSwitch,
			AccountState:  runtime.AccountState,
		})
	}
	sort.SliceStable(policies, func(i, j int) bool {
		left, right := policies[i], policies[j]
		leftOrder, rightOrder := sourceOrder(left.Source), sourceOrder(right.Source)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		leftScope, rightScope := scopeOrder(left), scopeOrder(right)
		if leftScope != rightScope {
			return leftScope < rightScope
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
	return policies
}

func orOpenAIProtocol(protocolCode string) string {
	if protocolCode == "" {
		return "openai"
	}
	return protocolCode
}

func policyMatchesAccountScope(policy gatewayruntimecache.ResponseInspectionPolicySummary, accountProtocolCode string, accountProviderCode string) bool {
	if !policy.Enabled {
		return false
	}
	providerCode := ""
	if policy.ProviderCode != nil {
		providerCode = *policy.ProviderCode
	}
	if policy.ScopeType == "protocol" {
		return policy.ProtocolCode == accountProtocolCode && providerCode == ""
	}
	return policy.ProtocolCode == accountProtocolCode && providerCode == accountProviderCode
}

func sourceOrder(source string) int {
	switch source {
	case PolicySourceAccount:
		return 0
	case PolicySourceManagement:
		return 1
	default:
		return 2
	}
}

func scopeOrder(policy RuntimeResponseInspectionPolicy) int {
	if policy.Source == PolicySourceAccount {
		return 0
	}
	if policy.ScopeType == "provider" {
		return 0
	}
	return 1
}

// InspectResponseSemanticFrames 对齐 inspectResponseSemanticFrames。
func InspectResponseSemanticFrames(frames []gatewayproto.SemanticFrame, policies []RuntimeResponseInspectionPolicy, downstreamWritten bool, transport string, context *ResponseInspectionRuntimeContext) ResponseInspectionResult {
	var observations []ResponseInspectionDecision
	for _, frame := range frames {
		match := MatchRuntimeResponseInspectionPolicy(frame, policies, context)
		if match == nil {
			continue
		}
		policy := match.Policy
		action := responseInspectionDecisionAction(policy, transport)
		decision := buildPolicyDecision(match, downstreamWritten, action, context)
		if policy.ExecutionMode == "dry_run" {
			observations = append(observations, decision)
			continue
		}
		if action == "discard_event" {
			return ResponseInspectionResult{
				Decision:     &decision,
				Observations: append(observations, decision),
			}
		}
		result := ResponseInspectionResult{Decision: &decision}
		if len(observations) > 0 {
			result.Observations = observations
		}
		return result
	}
	if len(observations) > 0 {
		return ResponseInspectionResult{Observations: observations}
	}
	return ResponseInspectionResult{}
}

// ResponseInspectionFailurePayloadForDecision 对齐
// responseInspectionFailurePayloadForDecision。
func ResponseInspectionFailurePayloadForDecision(decision *ResponseInspectionDecision, clientRetryEnabled bool) ResponseInspectionFailurePayload {
	clientRetryCode := ""
	if clientRetryEnabled && decision.RetryEnabled {
		clientRetryCode = gatewayStreamClientRetryErrorCode
	}
	errorCode := decision.RewriteErrorCode
	if errorCode == "" {
		errorCode = "response_inspection_matched"
	}
	if clientRetryCode != "" {
		errorCode = clientRetryCode
	}
	message := decision.RewriteMessage
	if message == "" {
		message = "响应命中检查策略"
	}
	if clientRetryCode == gatewayStreamClientRetryErrorCode {
		message = GatewayStreamClientRetryMessage
	}
	return ResponseInspectionFailurePayload{ErrorCode: errorCode, Message: message}
}

// InspectionMatchResult 对齐 ResponseInspectionMatchResult。
type InspectionMatchResult struct {
	Policy       RuntimeResponseInspectionPolicy
	MatchedFrame gatewayproto.SemanticFrame
	MatchedField string
	MatchedValue string
	Snippet      string
}

// MatchRuntimeResponseInspectionPolicy 对齐 matchRuntimeResponseInspectionPolicy。
func MatchRuntimeResponseInspectionPolicy(frame gatewayproto.SemanticFrame, policies []RuntimeResponseInspectionPolicy, context *ResponseInspectionRuntimeContext) *InspectionMatchResult {
	for _, policy := range policies {
		if !policy.Enabled {
			continue
		}
		if policy.Source == PolicySourceSystemDefault &&
			policy.ID == CodexCompactionContractPolicyID &&
			frame.FrameType != gatewayproto.FrameTypeRawJSONPath {
			// Node 以 provenance === 'gateway_protocol_contract' 判定；Go 的
			// SemanticFrame 用 FrameType raw_json_path 承载契约帧。
			continue
		}
		if !policyMatchesRuntimeContext(policy, context) {
			continue
		}
		match := firstPositiveMatch(frame, policy.Match)
		if match == nil {
			continue
		}
		if outputTextExcluded(frame, policy.Match) {
			continue
		}
		return &InspectionMatchResult{
			Policy:       policy,
			MatchedFrame: frame,
			MatchedField: match.field,
			MatchedValue: match.value,
			Snippet:      match.snippet,
		}
	}
	return nil
}

type partialMatch struct {
	field   string
	value   string
	snippet string
}

func firstPositiveMatch(frame gatewayproto.SemanticFrame, match gatewayruntimecache.ResponseInspectionPolicyMatch) *partialMatch {
	var matched []partialMatch

	if len(match.OutputTextIncludes) > 0 {
		if frame.Text == "" || !frame.VisibleOutput {
			return nil
		}
		outputTextMatch := firstSubstringMatch(frame.Text, match.OutputTextIncludes)
		if outputTextMatch == nil {
			return nil
		}
		matched = append(matched, partialMatch{
			field:   "outputTextIncludes",
			value:   *outputTextMatch,
			snippet: snippetAround(frame.Text, *outputTextMatch),
		})
	}

	if len(match.ErrorCodes) > 0 {
		errorCode := firstExactMatch(frame.ErrorCode, match.ErrorCodes)
		if errorCode == nil {
			return nil
		}
		matched = append(matched, partialMatch{field: "errorCodes", value: *errorCode, snippet: frame.ErrorCode})
	}

	if len(match.ErrorTypes) > 0 {
		errorType := firstExactMatch(frame.ErrorType, match.ErrorTypes)
		if errorType == nil {
			return nil
		}
		matched = append(matched, partialMatch{field: "errorTypes", value: *errorType, snippet: frame.ErrorType})
	}

	if len(match.ErrorMessagesIncludes) > 0 {
		if frame.ErrorMessage == "" {
			return nil
		}
		errorMessageMatch := firstSubstringMatch(frame.ErrorMessage, match.ErrorMessagesIncludes)
		if errorMessageMatch == nil {
			return nil
		}
		matched = append(matched, partialMatch{
			field:   "errorMessageIncludes",
			value:   *errorMessageMatch,
			snippet: snippetAround(frame.ErrorMessage, *errorMessageMatch),
		})
	}

	if len(match.FinishReasons) > 0 {
		finishReason := firstExactMatch(orString(frame.FinishReason, frame.Status), match.FinishReasons)
		if finishReason == nil {
			return nil
		}
		matched = append(matched, partialMatch{field: "finishReasons", value: *finishReason, snippet: orString(frame.FinishReason, frame.Status)})
	}

	if len(match.JSONPathsExists) > 0 {
		jsonPath := firstJSONPathMatch(frame, match.JSONPathsExists)
		if jsonPath == nil {
			return nil
		}
		matched = append(matched, partialMatch{field: "jsonPathsExists", value: *jsonPath, snippet: *jsonPath})
	}

	if len(match.RawTextIncludes) > 0 {
		if frame.RawText == "" {
			return nil
		}
		rawTextMatch := firstSubstringMatch(frame.RawText, match.RawTextIncludes)
		if rawTextMatch == nil {
			return nil
		}
		matched = append(matched, partialMatch{
			field:   "rawTextIncludes",
			value:   *rawTextMatch,
			snippet: snippetAround(frame.RawText, *rawTextMatch),
		})
	}

	for _, item := range matched {
		if item.snippet != "" {
			return &item
		}
	}
	if len(matched) > 0 {
		return &matched[0]
	}
	return nil
}

func outputTextExcluded(frame gatewayproto.SemanticFrame, match gatewayruntimecache.ResponseInspectionPolicyMatch) bool {
	if frame.Text == "" || !frame.VisibleOutput || len(match.OutputTextIncludes) == 0 || len(match.OutputTextExcludes) == 0 {
		return false
	}
	return firstSubstringMatch(frame.Text, match.OutputTextExcludes) != nil
}

func buildPolicyDecision(match *InspectionMatchResult, downstreamWritten bool, action string, context *ResponseInspectionRuntimeContext) ResponseInspectionDecision {
	policy := match.Policy
	frame := match.MatchedFrame
	decision := ResponseInspectionDecision{
		Reason:               configuredPolicyReason(policy),
		Action:               action,
		Transport:            string(frame.Transport),
		TriggerPhase:         "before_downstream_write",
		EndpointFamily:       frame.EndpointFamily,
		FrameType:            frame.FrameType,
		UpstreamEventType:    frame.EventType,
		UpstreamErrorCode:    frame.ErrorCode,
		UpstreamErrorType:    frame.ErrorType,
		UpstreamErrorMessage: frame.ErrorMessage,
		FinishReason:         orString(frame.FinishReason, frame.Status),
		RewriteErrorCode:     rewriteErrorCode(policy, frame),
		RewriteMessage:       frame.ErrorMessage,
		DownstreamWritten:    downstreamWritten,
		PolicyID:             policy.ID,
		PolicyName:           policy.Name,
		PolicySource:         policy.Source,
		PolicyScopeType:      policy.ScopeType,
		PolicyProtocolCode:   policy.ProtocolCode,
		PolicyProviderCode:   policy.ProviderCode,
		ExecutionMode:        policy.ExecutionMode,
		DataHandling:         policy.DataHandling,
		RetryEnabled:         policy.RetryEnabled,
		AccountSwitch:        policy.AccountSwitch,
		AccountState:         policy.AccountState,
		MatchedField:         match.MatchedField,
		MatchedValue:         match.MatchedValue,
		MatchedSnippet:       match.Snippet,
	}
	if downstreamWritten {
		decision.TriggerPhase = "after_downstream_write"
	}
	if context != nil {
		decision.ClientProfile = context.ClientProfile
		decision.CodexCompactionExpected = context.CodexCompactionExpected
	}
	if decision.RewriteMessage == "" {
		decision.RewriteMessage = "响应命中检查策略：" + firstNonEmpty(policy.Name, policy.ID, "未命名策略")
	}
	if policy.AccountSwitch == "request_next_account" ||
		policy.AccountSwitch == "avoid_account_ttl" ||
		policy.AccountSwitch == "avoid_upstream_bucket_ttl" {
		if policy.Source == PolicySourceSystemDefault {
			decision.ReplayAuthority = "system_default_retry_next_account"
		} else {
			decision.ReplayAuthority = "explicit_user_policy"
		}
	}
	return decision
}

func policyMatchesRuntimeContext(policy RuntimeResponseInspectionPolicy, context *ResponseInspectionRuntimeContext) bool {
	hasClientProfileMatcher := len(policy.Match.ClientProfiles) > 0
	if hasClientProfileMatcher {
		if context == nil || context.ClientProfile == "" || firstExactMatch(context.ClientProfile, policy.Match.ClientProfiles) == nil {
			return false
		}
	}
	if !hasClientProfileMatcher &&
		len(policy.Match.ErrorCodes) > 0 &&
		(policy.ScopeType != "provider" || policy.ProviderCode == "") {
		return false
	}
	return true
}

func configuredPolicyReason(policy RuntimeResponseInspectionPolicy) string {
	if policy.Source == PolicySourceSystemDefault {
		return "before_downstream_write_response_failure"
	}
	return "configured_response_policy"
}

func responseInspectionDecisionAction(policy RuntimeResponseInspectionPolicy, transport string) string {
	if policy.ExecutionMode == "dry_run" {
		return "dry_run"
	}
	if policy.DataHandling == "discard_event" && transport == "sse" {
		return "discard_event"
	}
	if policy.DataHandling == "discard_event" {
		return "dry_run"
	}
	if policy.DataHandling == "discard_response" {
		return "discard_response"
	}
	return "replace_with_failure"
}

func rewriteErrorCode(policy RuntimeResponseInspectionPolicy, frame gatewayproto.SemanticFrame) string {
	if frame.ErrorCode != "" {
		return frame.ErrorCode
	}
	return "response_inspection_matched"
}

func firstExactMatch(value string, needles []string) *string {
	if value == "" || len(needles) == 0 {
		return nil
	}
	normalized := strings.ToLower(value)
	for _, needle := range needles {
		if strings.ToLower(needle) == normalized {
			return &needle
		}
	}
	return nil
}

func firstSubstringMatch(value string, needles []string) *string {
	if len(needles) == 0 {
		return nil
	}
	normalized := strings.ToLower(value)
	for _, needle := range needles {
		if strings.Contains(normalized, strings.ToLower(needle)) {
			return &needle
		}
	}
	return nil
}

func firstJSONPathMatch(frame gatewayproto.SemanticFrame, needles []string) *string {
	for _, needle := range needles {
		if (frame.RawJSON != nil && jsonPathExists(frame.RawJSON, needle)) ||
			stringSliceContains(frame.RawJSONPaths, needle) {
			return &needle
		}
	}
	return nil
}

func jsonPathExists(value any, path string) bool {
	parts := strings.Split(path, ".")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		return false
	}
	current := value
	for _, part := range filtered {
		switch typed := current.(type) {
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return false
			}
			current = typed[index]
		case map[string]any:
			child, exists := typed[part]
			if !exists {
				return false
			}
			current = child
		default:
			return false
		}
	}
	return hasJSONPathMeaningfulValue(current)
}

func hasJSONPathMeaningfulValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func snippetAround(value string, needle string) string {
	runes := []rune(value)
	matchRuneIndex := -1
	needleRunes := []rune(strings.ToLower(needle))
	lowerRunes := []rune(strings.ToLower(value))
	for i := 0; i+len(needleRunes) <= len(lowerRunes); i++ {
		if string(lowerRunes[i:i+len(needleRunes)]) == string(needleRunes) {
			matchRuneIndex = i
			break
		}
	}
	if matchRuneIndex < 0 {
		if len(runes) > textMatchSnippetChars {
			return string(runes[:textMatchSnippetChars])
		}
		return value
	}
	start := matchRuneIndex - 40
	if start < 0 {
		start = 0
	}
	end := matchRuneIndex + len(needleRunes) + 80
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func orString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string { return orString(values...) }
