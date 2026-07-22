package managementresponseinspectionpolicies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	MaxManagementPolicies = 100

	ResponseInspectionPolicyCreatedReason = "response_inspection_policy_created"
	ResponseInspectionPolicyUpdatedReason = "response_inspection_policy_updated"
	ResponseInspectionPolicyDeletedReason = "response_inspection_policy_deleted"

	postCommitInvalidationTimeout = 5 * time.Second
)

type Match = port.ResponseInspectionPolicyMatch

type Input struct {
	Name         string
	Enabled      *bool
	Priority     *int
	ScopeType    string
	ProtocolCode string
	ProviderCode *string
	Match        Match
	Action       string
	Notes        *string
}

type ListResult struct {
	DefaultRules []port.ResponseInspectionPolicy `json:"defaultRules"`
	Policies     []port.ResponseInspectionPolicy `json:"policies"`
}

type RuntimeInvalidator interface {
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type Options struct {
	Store       port.ResponseInspectionPolicyStore
	Invalidator RuntimeInvalidator
	Now         func() time.Time
	NewID       func(prefix string) string
	Logger      *slog.Logger
}

type Service struct {
	store       port.ResponseInspectionPolicyStore
	invalidator RuntimeInvalidator
	now         func() time.Time
	newID       func(string) string
	logger      *slog.Logger
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

type NotFoundError struct{}

func (*NotFoundError) Error() string { return "响应检查策略不存在" }

func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{store: opts.Store, invalidator: opts.Invalidator, now: now, newID: newID, logger: opts.Logger}
}

func (s *Service) List(ctx context.Context) (ListResult, error) {
	if s.store == nil {
		return ListResult{}, fmt.Errorf("response inspection policy store is required")
	}
	policies, err := s.store.ListResponseInspectionPolicies(ctx, MaxManagementPolicies)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{DefaultRules: clonePolicies(systemDefaultRules), Policies: clonePolicies(policies)}, nil
}

func (s *Service) Create(ctx context.Context, input Input) (port.ResponseInspectionPolicy, error) {
	if s.store == nil {
		return port.ResponseInspectionPolicy{}, fmt.Errorf("response inspection policy store is required")
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return port.ResponseInspectionPolicy{}, err
	}
	now := nodeISOString(s.now())
	normalized.ID = s.newID("rip")
	normalized.CreatedAt = now
	normalized.UpdatedAt = now

	var created port.ResponseInspectionPolicy
	err = s.store.ResponseInspectionPolicyInTx(ctx, func(txCtx context.Context, tx port.ResponseInspectionPolicyTxStore) error {
		count, err := tx.CountResponseInspectionPolicies(txCtx, MaxManagementPolicies+1)
		if err != nil {
			return err
		}
		if count >= MaxManagementPolicies {
			return validationError(fmt.Sprintf("响应检查策略最多允许 %d 条", MaxManagementPolicies))
		}
		if err := validateProviderScope(txCtx, tx, normalized); err != nil {
			return err
		}
		created, err = tx.CreateResponseInspectionPolicy(txCtx, normalized)
		return mapWriteError(err)
	})
	if err != nil {
		return port.ResponseInspectionPolicy{}, mapWriteError(err)
	}
	s.invalidate(ctx, ResponseInspectionPolicyCreatedReason)
	return created, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (port.ResponseInspectionPolicy, error) {
	if s.store == nil {
		return port.ResponseInspectionPolicy{}, fmt.Errorf("response inspection policy store is required")
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return port.ResponseInspectionPolicy{}, err
	}
	if id == "" {
		return port.ResponseInspectionPolicy{}, &NotFoundError{}
	}

	var updated port.ResponseInspectionPolicy
	err = s.store.ResponseInspectionPolicyInTx(ctx, func(txCtx context.Context, tx port.ResponseInspectionPolicyTxStore) error {
		current, found, err := tx.FindResponseInspectionPolicyForUpdate(txCtx, id)
		if err != nil {
			return err
		}
		if !found {
			return &NotFoundError{}
		}
		if err := validateProviderScope(txCtx, tx, normalized); err != nil {
			return err
		}
		normalized.ID = id
		normalized.CreatedAt = current.CreatedAt
		normalized.UpdatedAt = nodeISOString(s.now())
		updated, found, err = tx.UpdateResponseInspectionPolicy(txCtx, normalized)
		if err != nil {
			return mapWriteError(err)
		}
		if !found {
			return &NotFoundError{}
		}
		return nil
	})
	if err != nil {
		return port.ResponseInspectionPolicy{}, mapWriteError(err)
	}
	s.invalidate(ctx, ResponseInspectionPolicyUpdatedReason)
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, id string) (port.ResponseInspectionPolicy, error) {
	if s.store == nil {
		return port.ResponseInspectionPolicy{}, fmt.Errorf("response inspection policy store is required")
	}
	if id == "" {
		return port.ResponseInspectionPolicy{}, &NotFoundError{}
	}
	var deleted port.ResponseInspectionPolicy
	err := s.store.ResponseInspectionPolicyInTx(ctx, func(txCtx context.Context, tx port.ResponseInspectionPolicyTxStore) error {
		current, found, err := tx.FindResponseInspectionPolicyForUpdate(txCtx, id)
		if err != nil {
			return err
		}
		if !found {
			return &NotFoundError{}
		}
		removed, err := tx.DeleteResponseInspectionPolicy(txCtx, id)
		if err != nil {
			return mapWriteError(err)
		}
		if !removed {
			return &NotFoundError{}
		}
		deleted = current
		return nil
	})
	if err != nil {
		return port.ResponseInspectionPolicy{}, mapWriteError(err)
	}
	s.invalidate(ctx, ResponseInspectionPolicyDeletedReason)
	return deleted, nil
}

func normalizeInput(input Input) (port.ResponseInspectionPolicyWriteInput, error) {
	name, err := requiredText(input.Name, "规则名称", 100)
	if err != nil {
		return port.ResponseInspectionPolicyWriteInput{}, err
	}
	if input.ScopeType != "protocol" && input.ScopeType != "provider" {
		return port.ResponseInspectionPolicyWriteInput{}, validationError("响应检查策略作用层级无效")
	}
	if input.ProtocolCode != "openai" && input.ProtocolCode != "anthropic" && input.ProtocolCode != "gemini" {
		return port.ResponseInspectionPolicyWriteInput{}, validationError("响应检查策略协议无效")
	}
	providerCode, err := normalizeProviderCode(input.ScopeType, input.ProviderCode)
	if err != nil {
		return port.ResponseInspectionPolicyWriteInput{}, err
	}
	priority := 100
	if input.Priority != nil {
		priority = *input.Priority
	}
	if priority < 1 || priority > 9999 {
		return port.ResponseInspectionPolicyWriteInput{}, validationError("优先级必须是 1-9999 的整数")
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	action := input.Action
	if !validAction(action) {
		return port.ResponseInspectionPolicyWriteInput{}, validationError("响应检查策略动作无效")
	}
	match, err := normalizeMatch(input.Match)
	if err != nil {
		return port.ResponseInspectionPolicyWriteInput{}, err
	}
	notes, err := optionalText(input.Notes, "备注", 1000)
	if err != nil {
		return port.ResponseInspectionPolicyWriteInput{}, err
	}
	return port.ResponseInspectionPolicyWriteInput{
		Name: name, Enabled: enabled, Priority: priority, ScopeType: input.ScopeType,
		ProtocolCode: input.ProtocolCode, ProviderCode: providerCode, Match: match,
		Action: action, Notes: notes,
	}, nil
}

func normalizeProviderCode(scope string, value *string) (*string, error) {
	if scope == "protocol" {
		if value != nil {
			return nil, validationError("协议层响应检查策略不能绑定供应商")
		}
		return nil, nil
	}
	if value == nil || responsePolicyTrimECMAScriptWhitespace(*value) == "" {
		return nil, validationError("供应商层响应检查策略必须选择供应商")
	}
	provider, err := requiredText(*value, "供应商编码", 80)
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

func normalizeMatch(input Match) (Match, error) {
	clientProfiles, err := normalizeClientProfiles(input.ClientProfiles)
	if err != nil {
		return Match{}, err
	}
	outputTextIncludes, err := normalizeStringList(input.OutputTextIncludes, 50)
	if err != nil {
		return Match{}, err
	}
	outputTextExcludes, err := normalizeStringList(input.OutputTextExcludes, 50)
	if err != nil {
		return Match{}, err
	}
	errorCodes, err := normalizeStringList(input.ErrorCodes, 50)
	if err != nil {
		return Match{}, err
	}
	errorTypes, err := normalizeStringList(input.ErrorTypes, 50)
	if err != nil {
		return Match{}, err
	}
	errorMessageIncludes, err := normalizeStringList(input.ErrorMessageIncludes, 50)
	if err != nil {
		return Match{}, err
	}
	finishReasons, err := normalizeStringList(input.FinishReasons, 50)
	if err != nil {
		return Match{}, err
	}
	jsonPathsExists, err := normalizeStringList(input.JSONPathsExists, 50)
	if err != nil {
		return Match{}, err
	}
	rawTextIncludes, err := normalizeStringList(input.RawTextIncludes, 50)
	if err != nil {
		return Match{}, err
	}
	if len(outputTextIncludes)+len(errorCodes)+len(errorTypes)+len(errorMessageIncludes)+len(finishReasons)+len(jsonPathsExists)+len(rawTextIncludes) == 0 {
		return Match{}, validationError("至少需要填写一个匹配条件")
	}
	return Match{
		ClientProfiles: clientProfiles, OutputTextIncludes: outputTextIncludes,
		OutputTextExcludes: outputTextExcludes, ErrorCodes: errorCodes, ErrorTypes: errorTypes,
		ErrorMessageIncludes: errorMessageIncludes, FinishReasons: finishReasons,
		JSONPathsExists: jsonPathsExists, RawTextIncludes: rawTextIncludes,
	}, nil
}

func normalizeStringList(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, validationError(fmt.Sprintf("匹配条件不能超过 %d 项", limit))
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		text := responsePolicyTrimECMAScriptWhitespace(value)
		if text == "" {
			return nil, validationError("匹配条件不能为空")
		}
		if utf16Length(text) > 200 {
			return nil, validationError("匹配条件不能超过 200 个字符")
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result, nil
}

func normalizeClientProfiles(values []string) ([]string, error) {
	if len(values) > 6 {
		return nil, validationError("匹配条件不能超过 6 项")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !validClientProfile(value) {
			return nil, validationError("客户端画像无效")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateProviderScope(ctx context.Context, store port.ResponseInspectionPolicyTxStore, input port.ResponseInspectionPolicyWriteInput) error {
	if input.ScopeType != "provider" || input.ProviderCode == nil {
		return nil
	}
	ok, err := store.ResponseInspectionProviderSupportsProtocol(ctx, *input.ProviderCode, input.ProtocolCode)
	if err != nil {
		return err
	}
	if !ok {
		return validationError("响应检查策略供应商必须使用同协议启用档案")
	}
	return nil
}

func mapWriteError(err error) error {
	if errors.Is(err, port.ErrResponseInspectionPolicyConflict) {
		return &ConflictError{Message: "响应检查策略写入冲突，请刷新后重试"}
	}
	return err
}

func (s *Service) invalidate(ctx context.Context, reason string) {
	if s.invalidator == nil {
		return
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postCommitInvalidationTimeout)
	defer cancel()
	if err := s.invalidator.InvalidateGatewayRuntime(postCtx, reason); err != nil && s.logger != nil {
		s.logger.Warn("响应检查策略运行态缓存失效失败",
			slog.String("event", "response_inspection_policy_runtime_invalidation_failed"),
			slog.String("reason", reason),
			slog.Any("error", err),
		)
	}
}

func ValidationMessage(err error) string {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Message
	}
	return ""
}

func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if message := ValidationMessage(err); message != "" {
		return message
	}
	var conflictErr *ConflictError
	if errors.As(err, &conflictErr) {
		return conflictErr.Message
	}
	var notFoundErr *NotFoundError
	if errors.As(err, &notFoundErr) {
		return notFoundErr.Error()
	}
	return ""
}

func IsConflict(err error) bool {
	var target *ConflictError
	return errors.As(err, &target)
}

func IsNotFound(err error) bool {
	var target *NotFoundError
	return errors.As(err, &target)
}

func validationError(message string) error { return &ValidationError{Message: message} }

func requiredText(value string, label string, max int) (string, error) {
	text := responsePolicyTrimECMAScriptWhitespace(value)
	if text == "" {
		return "", validationError(label + "不能为空")
	}
	if utf16Length(text) > max {
		return "", validationError(fmt.Sprintf("%s不能超过 %d 个字符", label, max))
	}
	return text, nil
}

func optionalText(value *string, label string, max int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, err := requiredText(*value, label, max)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length += utf16.RuneLen(character)
	}
	return length
}

func responsePolicyTrimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, responsePolicyECMAScriptWhitespace)
}

func responsePolicyECMAScriptWhitespace(character rune) bool {
	switch character {
	case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
		'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
		'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
		'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

func validAction(value string) bool {
	switch value {
	case "observe", "drop_event", "retry_no_avoidance", "retry_next_account", "avoid_account_ttl", "avoid_upstream_bucket_ttl":
		return true
	default:
		return false
	}
}

func validClientProfile(value string) bool {
	switch value {
	case "codex", "generic_openai", "claude_code", "generic_anthropic", "generic_gemini", "gemini_cli":
		return true
	default:
		return false
	}
}

func clonePolicies(values []port.ResponseInspectionPolicy) []port.ResponseInspectionPolicy {
	result := make([]port.ResponseInspectionPolicy, 0, len(values))
	for _, value := range values {
		value.Match = cloneMatch(value.Match)
		result = append(result, value)
	}
	return result
}

func cloneMatch(value Match) Match {
	return Match{
		ClientProfiles:       append([]string(nil), value.ClientProfiles...),
		OutputTextIncludes:   append([]string(nil), value.OutputTextIncludes...),
		OutputTextExcludes:   append([]string(nil), value.OutputTextExcludes...),
		ErrorCodes:           append([]string(nil), value.ErrorCodes...),
		ErrorTypes:           append([]string(nil), value.ErrorTypes...),
		ErrorMessageIncludes: append([]string(nil), value.ErrorMessageIncludes...),
		FinishReasons:        append([]string(nil), value.FinishReasons...),
		JSONPathsExists:      append([]string(nil), value.JSONPathsExists...),
		RawTextIncludes:      append([]string(nil), value.RawTextIncludes...),
	}
}

func nodeISOString(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

var systemDefaultRules = []port.ResponseInspectionPolicy{
	{ID: "default_openai_error_object", DefaultRule: true, Editable: false, Name: "OpenAI error 对象", Enabled: true, Priority: 1, ScopeType: "protocol", ProtocolCode: "openai", Match: Match{JSONPathsExists: []string{"error"}}, Action: "retry_no_avoidance", Notes: textPointer("OpenAI v1 JSON / SSE data.error 默认检查规则；是否允许客户端专用重试由运行时客户端能力门控。")},
	{ID: "default_openai_response_error", DefaultRule: true, Editable: false, Name: "OpenAI response.error", Enabled: true, Priority: 2, ScopeType: "protocol", ProtocolCode: "openai", Match: Match{JSONPathsExists: []string{"response.error"}}, Action: "retry_no_avoidance", Notes: textPointer("OpenAI v1 Responses response.error 默认检查规则。")},
	{ID: "default_openai_failed_status", DefaultRule: true, Editable: false, Name: "OpenAI failed 状态", Enabled: true, Priority: 3, ScopeType: "protocol", ProtocolCode: "openai", Match: Match{FinishReasons: []string{"failed"}}, Action: "retry_no_avoidance", Notes: textPointer("OpenAI v1 Responses failed 状态默认检查规则。")},
	{ID: "default_codex_response_incomplete", DefaultRule: true, Editable: false, Name: "Codex response.incomplete", Enabled: true, Priority: 4, ScopeType: "protocol", ProtocolCode: "openai", Match: Match{ClientProfiles: []string{"codex"}, FinishReasons: []string{"incomplete"}}, Action: "retry_no_avoidance", Notes: textPointer("Codex 客户端会把 Responses response.incomplete 当成可重试流式错误；网关在写下游前拦截为统一可重试失败，避免服务端误判成功。")},
	{ID: "default_codex_compaction_contract", DefaultRule: true, Editable: false, Name: "Codex compact 输出契约", Enabled: true, Priority: 5, ScopeType: "protocol", ProtocolCode: "openai", Match: Match{ClientProfiles: []string{"codex"}, ErrorCodes: []string{"codex_compaction_contract_mismatch"}}, Action: "retry_next_account", Notes: textPointer("Codex Remote Compaction V2 要求返回恰好 1 个 compaction output item；不满足时在下游写出前触发重试或可重试失败。")},
	{ID: "default_gpt_cyber_policy", DefaultRule: true, Editable: false, Name: "GPT cyber_policy", Enabled: true, Priority: 6, ScopeType: "provider", ProtocolCode: "openai", ProviderCode: textPointer("gpt"), Match: Match{ErrorCodes: []string{"cyber_policy"}}, Action: "retry_no_avoidance", Notes: textPointer("GPT 供应商 cyber_policy 规则，适用于该供应商的所有下游客户端；不能扩散为所有 OpenAI-compatible 供应商语义。")},
	{ID: "default_anthropic_error_object", DefaultRule: true, Editable: false, Name: "Anthropic error 对象", Enabled: true, Priority: 1, ScopeType: "protocol", ProtocolCode: "anthropic", Match: Match{JSONPathsExists: []string{"error"}}, Action: "retry_no_avoidance", Notes: textPointer("Anthropic Messages JSON / SSE event:error 默认检查规则；错误类型只作为响应语义输入，不直接写账号状态。")},
	{ID: "default_gemini_cli_retryable_error", DefaultRule: true, Editable: false, Name: "Gemini CLI 可重试错误", Enabled: true, Priority: 1, ScopeType: "protocol", ProtocolCode: "gemini", Match: Match{ClientProfiles: []string{"gemini_cli"}, ErrorTypes: []string{"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "CANCELLED"}}, Action: "retry_next_account", Notes: textPointer("gemini-cli 已知会把 429、499、5xx 和超时类 Google canonical error 当作可重试错误；该规则只在 gemini_cli 客户端画像下请求下一个账号，不扩散到普通 Gemini 客户端。")},
	{ID: "default_gemini_error_object", DefaultRule: true, Editable: false, Name: "Gemini error 对象", Enabled: true, Priority: 20, ScopeType: "protocol", ProtocolCode: "gemini", Match: Match{JSONPathsExists: []string{"error"}}, Action: "retry_no_avoidance", Notes: textPointer("Gemini JSON / SSE error 默认检查规则；错误状态只作为响应语义输入，不直接写账号状态。")},
}

func textPointer(value string) *string { return &value }
