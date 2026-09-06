package main

// 显式账户错误策略决策服务 —— response/failure-dispatch.ts 所消费的
// policy/account-error-policy.service.ts decideAccountErrorPolicy 的 Go 移植。
//
// 归属判断：Node 中该服务位于 modules/gateway/policy/（网关策略域），其
// modules/accounts/ 侧同族文件（规则/覆盖校验、额度恢复策略归一）已由
// internal/accounts/error_policy.go、quota_recovery.go 落地为写侧校验；本文件
// 只补齐网关运行时需要的「只读决策面」：系统继承规则注册表与匹配器、账户规则
// 的读取归一与匹配、恢复时间计算（含被动调度确定性抖动）、上游显式恢复 hint。
// 状态写侧不在这里 —— 见 chain_error_policy_effects.go 的窄口与桥。
//
// 决策输入：statusCode / 协议错误载荷（code/type/message）/ 失败体文本；
// 规则来源：系统继承规则（system.upstream_insufficient_quota，代码内注册表，
// 可被账户覆盖 replace/delete 抑制）+ 账户 credentials.error_handling_rules
// （enabled 过滤 + priority 升序，先命中先赢）。
// 动作空间：retry_next / cooldown(rate_limited|temporary_unavailable) / disable。

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayobs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// provenance 常量（domain/account-runtime-provenance.ts）
// ---------------------------------------------------------------------------

// explicitAccountErrorPolicyCooldownCode 镜像
// EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE。
const explicitAccountErrorPolicyCooldownCode = "explicit_account_error_policy_cooldown"

// systemQuotaGenericCooldownCode / systemQuotaExplicitResetCooldownCode 镜像
// SYSTEM_QUOTA_{GENERIC,EXPLICIT_RESET}_COOLDOWN_CODE：账户级系统额度 provenance
// 错误码，用于对单 Key / OAuth 冷却写入做围栏（systemQuotaCooldownPrioritySql）。
const (
	systemQuotaGenericCooldownCode        = "system_quota_generic_cooldown"
	systemQuotaExplicitResetCooldownCode  = "system_quota_explicit_reset"
	legacyExplicitPolicyMessagePrefix     = "账户错误策略「"
	systemQuotaExplicitResetMessagePrefix = "系统继承错误策略「"
)

// systemInsufficientQuotaRuleID 镜像 SYSTEM_INSUFFICIENT_QUOTA_ERROR_POLICY_RULE_ID。
const systemInsufficientQuotaRuleID = "system.upstream_insufficient_quota"

// systemQuotaRuleName 是系统额度不足规则的固定名称（Node 注册表 name）。
const systemQuotaRuleName = "上游额度不足"

// ---------------------------------------------------------------------------
// 决策形状（AccountErrorPolicyDecision）
// ---------------------------------------------------------------------------

// accountErrorPolicyDecision 镜像 AccountErrorPolicyDecision。
type accountErrorPolicyDecision struct {
	Action                 string // 'retry_next' | 'cooldown' | 'disable'
	RuleName               string
	RuleID                 string
	RuleSource             string // 'system' | 'account'
	CooldownUntil          string
	CooldownStatus         string // 'rate_limited' | 'temporary_unavailable'
	KeyScoped              bool
	QuotaRecoveryMode      string // '' | 'generic' | 'explicit_reset'
	QuotaRecoveryHintSource string // '' | 'reset_at' | 'retry_after' | 'provider_header'
}

// 决策动作 / 冷却状态值。
const (
	decisionActionRetryNext = "retry_next"
	decisionActionCooldown  = "cooldown"
	decisionActionDisable   = "disable"

	cooldownStatusRateLimited          = "rate_limited"
	cooldownStatusTemporaryUnavailable = "temporary_unavailable"
)

// chainErrorPolicyDeps 携带决策服务的运行时协作者。零值可用：Now 缺省
// time.Now，PoolIsolationEnabled 缺省恒 false（等价单 Key 账户的 Node 投影）。
type chainErrorPolicyDeps struct {
	Now func() time.Time
	// PoolIsolationEnabled 镜像 isAccountApiKeyPoolIsolationEnabled(...) &&
	// Boolean(selectedApiKeyFingerprint)：生产装配经 accountkeystates.Store
	// 提供真实实现，测试注入桩。
	PoolIsolationEnabled func(account gatewaydispatch.AccountCandidate) bool
}

// chainErrorPolicyService 是决策服务的无状态载体。
type chainErrorPolicyService struct {
	now  func() time.Time
	pool func(account gatewaydispatch.AccountCandidate) bool
}

func newChainErrorPolicyService(deps chainErrorPolicyDeps) *chainErrorPolicyService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now() }
	}
	pool := deps.PoolIsolationEnabled
	if pool == nil {
		pool = func(gatewaydispatch.AccountCandidate) bool { return false }
	}
	return &chainErrorPolicyService{now: now, pool: pool}
}

// Decide 镜像 decideAccountErrorPolicy：2xx 无决策；先试系统额度规则（未被
// 覆盖抑制时），再按 priority 升序匹配账户规则。返回 nil 表示无决策
// （opaque_http 失败）。规则/覆盖/额度恢复策略读取归一失败以错误上抛
// （Node 读取侧同一严格校验函数抛出同步异常）。
func (s *chainErrorPolicyService) Decide(
	account gatewaydispatch.AccountCandidate,
	statusCode int,
	header http.Header,
	bodyText string,
	parsedBody map[string]any,
	settings gatewayruntimecache.GatewaySettings,
) (*accountErrorPolicyDecision, error) {
	if statusCode >= 200 && statusCode <= 299 {
		return nil, nil
	}
	payload := s.errorPayloadOf(bodyText, header, parsedBody)
	errorCode := strings.ToLower(payload.Code)
	errorType := strings.ToLower(payload.Type)
	searchableParts := []string{}
	for _, part := range []string{payload.Message, bodyText} {
		if strings.TrimSpace(part) != "" {
			searchableParts = append(searchableParts, part)
		}
	}
	systemSearchableText := strings.ToLower(strings.Join(searchableParts, "\n"))

	overrides, err := accountErrorPolicyOverridesRead(account.Credentials["error_handling_rule_overrides"])
	if err != nil {
		return nil, err
	}
	quotaOverridden := false
	for _, override := range overrides {
		if override.SystemRuleID == systemInsufficientQuotaRuleID {
			quotaOverridden = true
			break
		}
	}
	if !quotaOverridden && systemInsufficientQuotaRuleMatches(statusCode, errorCode, errorType, systemSearchableText) {
		return s.systemQuotaDecision(account, statusCode, header, bodyText)
	}

	rules, err := accountErrorHandlingRulesRead(account.Credentials["error_handling_rules"])
	if err != nil {
		return nil, err
	}
	enabled := make([]accountErrorHandlingRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			enabled = append(enabled, rule)
		}
	}
	sort.SliceStable(enabled, func(left, right int) bool {
		return enabled[left].Priority < enabled[right].Priority
	})
	searchableText := strings.ToLower(bodyText)
	for _, rule := range enabled {
		if accountErrorRuleMatches(rule, statusCode, errorCode, errorType, searchableText) {
			return s.accountRuleDecision(account, rule, settings)
		}
	}
	return nil, nil
}

// errorPayloadOf 镜像 parseFailureBodyFacts → parseErrorPayload：JSON 体走
// 协议载荷投影（当前链只挂 OpenAI 协议族），否则按文本 + 响应头解析。
func (s *chainErrorPolicyService) errorPayloadOf(bodyText string, header http.Header, parsedBody map[string]any) gatewayproto.ErrorPayload {
	if parsedBody != nil {
		return gatewayopenai.ParseErrorPayloadFromJSONValue(parsedBody)
	}
	return gatewayopenai.ParseErrorPayload(bodyText, header)
}

// systemQuotaDecision 镜像 decideAccountErrorPolicy 的系统额度分支：显式
// 恢复 hint（api_key 账户）优先，其次账户配额恢复策略按账户类型的计划边界
// （确定性被动抖动），兜底 1 小时 duration 冷却。
func (s *chainErrorPolicyService) systemQuotaDecision(
	account gatewaydispatch.AccountCandidate,
	statusCode int,
	header http.Header,
	bodyText string,
) (*accountErrorPolicyDecision, error) {
	apiKeyGenericRecovery := account.Type == "api_key"
	var recoveryMode string
	var hintSource string
	var hintCooldownUntil string
	seed := account.ID + ":" + seedKeyPart(account.SelectedAPIKeyFingerprint)
	// Node：显式恢复 hint 只对 api_key 账户提取；api_key 无 hint 时回落
	// generic 模式，非 api_key 账户的 recoveryMode 保持为空。
	if apiKeyGenericRecovery {
		if hint := extractAPIKeyQuotaRecoveryHint(bodyText, responseHeaderMapOf(header), s.now()); hint != nil {
			recoveryMode = string(hint.Mode)
			hintSource = hint.Source
			hintCooldownUntil = hint.CooldownUntil
		} else {
			recoveryMode = string(quotaRecoveryModeGeneric)
		}
	}
	keyScoped := s.pool(account)

	var cooldownUntil string
	if hintCooldownUntil != "" {
		cooldownUntil = hintCooldownUntil
	} else {
		policyValue, hasPolicy := account.Credentials["quota_recovery_policy"]
		var policy map[string]any
		if hasPolicy && policyValue != nil {
			normalized, err := quotaRecoveryPolicyRead(policyValue)
			if err != nil {
				return nil, err
			}
			policy = normalized
		}
		recoveryAccountType := "oauth"
		switch account.Type {
		case "api_key":
			recoveryAccountType = "api_key"
		case "google_oauth":
			recoveryAccountType = "google_oauth"
		}
		until, err := quotaRecoveryCooldownUntil(policy, recoveryAccountType, seed, s.now())
		if err != nil {
			return nil, err
		}
		cooldownUntil = until
	}
	return &accountErrorPolicyDecision{
		Action:                 decisionActionCooldown,
		RuleID:                 systemInsufficientQuotaRuleID,
		RuleName:               systemQuotaRuleName,
		RuleSource:             "system",
		CooldownStatus:         cooldownStatusRateLimited,
		CooldownUntil:          cooldownUntil,
		KeyScoped:              keyScoped,
		QuotaRecoveryMode:      recoveryMode,
		QuotaRecoveryHintSource: hintSource,
	}, nil
}

// accountRuleDecision 镜像账户规则分支的动作映射。
func (s *chainErrorPolicyService) accountRuleDecision(
	account gatewaydispatch.AccountCandidate,
	rule accountErrorHandlingRule,
	settings gatewayruntimecache.GatewaySettings,
) (*accountErrorPolicyDecision, error) {
	if rule.Action == decisionActionRetryNext {
		return &accountErrorPolicyDecision{Action: decisionActionRetryNext, RuleName: rule.Name, RuleSource: "account"}, nil
	}
	if rule.Action == "error_disabled" {
		return &accountErrorPolicyDecision{Action: decisionActionDisable, RuleName: rule.Name, RuleSource: "account"}, nil
	}
	if rule.Action == "rate_limited" {
		until := accountErrorRuleCooldownUntil(rule, s.now(), account.ID+":"+rule.Name+":"+formatPriority(rule.Priority))
		return &accountErrorPolicyDecision{
			Action:         decisionActionCooldown,
			RuleName:       rule.Name,
			RuleSource:     "account",
			CooldownStatus: cooldownStatusRateLimited,
			CooldownUntil:  until,
		}, nil
	}
	// temp_unschedulable：固定 temporary_unavailable 冷却，时长来自系统设置。
	until := s.now().Add(time.Duration(settings.DefaultTemporaryUnschedulableMinutes) * time.Minute).UTC().Format(rfc3339MillisUTC)
	return &accountErrorPolicyDecision{
		Action:         decisionActionCooldown,
		RuleName:       rule.Name,
		RuleSource:     "account",
		CooldownStatus: cooldownStatusTemporaryUnavailable,
		CooldownUntil:  until,
	}, nil
}

func seedKeyPart(fingerprint *string) string {
	if fingerprint != nil && strings.TrimSpace(*fingerprint) != "" {
		return strings.TrimSpace(*fingerprint)
	}
	return "account"
}

func formatPriority(priority float64) string {
	return fmt.Sprintf("%d", int64(priority))
}

// rfc3339MillisUTC 与 Node toISOString() 输出一致（UTC + 毫秒 + Z）。
const rfc3339MillisUTC = "2006-01-02T15:04:05.000Z07:00"

func responseHeaderMapOf(header http.Header) map[string]string {
	if header == nil {
		return nil
	}
	out := make(map[string]string, len(header))
	for name, values := range header {
		if len(values) == 0 {
			continue
		}
		out[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}

// ---------------------------------------------------------------------------
// 系统额度不足规则（account-error-policy-system-rules.ts 代码内注册表）
// ---------------------------------------------------------------------------

var insufficientQuotaStableCodes = map[string]struct{}{}
var insufficientQuotaTextMarkers = []string{
	"余额不足",
	"额度不足",
	"insufficient balance",
	"insufficient quota",
	"subscription quota insufficient",
	"credit balance too low",
	"wallet balance exhausted",
}
var nonQuota403ErrorIdentifiers = []string{
	"content_policy_violation",
	"content_policy_blocked",
	"prompt_guard_blocked",
	"client_restricted",
	"permission_denied",
	"access_denied",
	"forbidden",
}

func init() {
	for _, code := range []string{
		"insufficient_user_quota",
		"insufficient_quota",
		"insufficient_balance",
		"quota_exceeded",
		"quota_exhausted",
		"default_group_global_quota_exhausted",
		"billing_hard_limit_reached",
		"wallet_balance_exhausted",
		"pre_consume_token_quota_failed",
	} {
		insufficientQuotaStableCodes[normalizeErrorIdentifier(code)] = struct{}{}
	}
}

func normalizeErrorIdentifier(value string) string {
	// Node: (value ?? '').trim().toLowerCase().replace(/[\s-]+/g, '_')
	trimmed := strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	previousSeparator := false
	for _, r := range trimmed {
		if r == ' ' || r == '-' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			previousSeparator = true
			continue
		}
		if previousSeparator {
			builder.WriteByte('_')
			previousSeparator = false
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// systemInsufficientQuotaRuleMatches 镜像 systemInsufficientQuotaRuleMatches：
// 系统规则刻意比通用账户规则更宽 —— code 标记与高置信文本标记是「或」而非
// 「且」，且排除非额度类 403 标识。
func systemInsufficientQuotaRuleMatches(statusCode int, errorCode, errorType, searchableText string) bool {
	if statusCode != http.StatusPaymentRequired && statusCode != http.StatusForbidden {
		return false
	}
	code := normalizeErrorIdentifier(errorCode)
	typ := normalizeErrorIdentifier(errorType)
	if _, ok := insufficientQuotaStableCodes[code]; ok {
		return true
	}
	if _, ok := insufficientQuotaStableCodes[typ]; ok {
		return true
	}
	if strings.Contains(code, "quota") || strings.Contains(typ, "quota") {
		return true
	}
	identifierSet := map[string]struct{}{}
	for _, identifier := range nonQuota403ErrorIdentifiers {
		identifierSet[identifier] = struct{}{}
	}
	if _, ok := identifierSet[code]; ok {
		return false
	}
	if _, ok := identifierSet[typ]; ok {
		return false
	}
	if statusCode == http.StatusPaymentRequired && code == "" && typ == "" {
		return true
	}
	text := strings.ToLower(searchableText)
	for _, identifier := range nonQuota403ErrorIdentifiers {
		if strings.Contains(text, strings.ReplaceAll(identifier, "_", " ")) || strings.Contains(text, identifier) {
			return false
		}
	}
	for _, marker := range insufficientQuotaTextMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 账户规则读取归一（account-error-policy-validation.ts 读取侧）
// ---------------------------------------------------------------------------

// accountErrorHandlingRule 镜像 AccountErrorHandlingRule 的运行时投影。
type accountErrorHandlingRule struct {
	Enabled         bool
	Name            string
	Priority        float64
	Action          string
	StatusCodes     []float64
	ErrorCodes      []string
	ErrorTypes      []string
	Keywords        []string
	ResetStrategy   string
	DurationHours   float64
	DailyResetHour  float64
	WeeklyResetDay  float64
	WeeklyResetHour float64
}

// accountErrorHandlingRulesRead 镜像读取侧 normalizeAccountErrorHandlingRules：
// undefined → 空表；非数组或单条形状非法以错误上抛（与写侧同一严格校验）。
func accountErrorHandlingRulesRead(value any) ([]accountErrorHandlingRule, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("错误处理策略规则格式无效")
	}
	rules := make([]accountErrorHandlingRule, 0, len(list))
	for index, item := range list {
		rule, err := accountErrorHandlingRuleRead(item, index+1)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func accountErrorHandlingRuleRead(value any, index int) (accountErrorHandlingRule, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条错误处理策略规则格式无效", index)
	}
	if readTextEqual(record["source"], "system") || readBoolEqual(record["inherited"], true) || readBoolEqual(record["editable"], false) {
		return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条错误处理策略规则不能写入系统继承规则", index)
	}
	allowed := map[string]bool{
		"enabled": true, "name": true, "priority": true, "status_codes": true,
		"error_codes": true, "error_types": true, "keywords": true, "action": true,
		"reset_strategy": true, "duration_hours": true, "daily_reset_hour": true,
		"weekly_reset_day": true, "weekly_reset_hour": true, "description": true,
	}
	for key := range record {
		if !allowed[key] {
			return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条错误处理策略规则包含不支持字段：%s", index, key)
		}
	}
	enabled, err := readRequiredBool(record["enabled"], fmt.Sprintf("第 %d 条规则启用状态", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	name, err := readRequiredString(record["name"], fmt.Sprintf("第 %d 条规则名称", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	priority, err := readRequiredPositiveInt(record["priority"], fmt.Sprintf("第 %d 条规则优先级", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	action, ok := record["action"].(string)
	if !ok || (action != "retry_next" && action != "temp_unschedulable" && action != "rate_limited" && action != "error_disabled") {
		return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条规则错误处理动作无效", index)
	}
	statusCodes, err := readStatusCodes(record["status_codes"], index)
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	errorCodes, err := readStringList(record["error_codes"], fmt.Sprintf("第 %d 条规则错误码", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	for _, code := range errorCodes {
		if isAllDigits(code) {
			number := 0
			for _, digit := range code {
				number = number*10 + int(digit-'0')
			}
			if number >= 200 && number <= 299 {
				return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条规则错误码不能填写 2xx 成功码，例如 200", index)
			}
		}
	}
	errorTypes, err := readStringList(record["error_types"], fmt.Sprintf("第 %d 条规则错误类型", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	keywords, err := readStringList(record["keywords"], fmt.Sprintf("第 %d 条规则关键字", index))
	if err != nil {
		return accountErrorHandlingRule{}, err
	}
	if enabled && len(statusCodes) == 0 && len(errorCodes) == 0 && len(errorTypes) == 0 && len(keywords) == 0 {
		return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条规则至少需要一个匹配条件", index)
	}
	rule := accountErrorHandlingRule{
		Enabled:     enabled,
		Name:        name,
		Priority:    priority,
		Action:      action,
		StatusCodes: statusCodes,
		ErrorCodes:  errorCodes,
		ErrorTypes:  errorTypes,
		Keywords:    keywords,
	}
	if action == "rate_limited" {
		strategy, ok := record["reset_strategy"].(string)
		if !ok || (strategy != "duration" && strategy != "daily" && strategy != "weekly") {
			return accountErrorHandlingRule{}, fmt.Errorf("第 %d 条限流规则恢复策略无效", index)
		}
		rule.ResetStrategy = strategy
		switch strategy {
		case "duration":
			hours, err := readRequiredPositiveInt(record["duration_hours"], fmt.Sprintf("第 %d 条限流规则恢复小时数", index))
			if err != nil {
				return accountErrorHandlingRule{}, err
			}
			rule.DurationHours = hours
		case "daily":
			hour, err := readHour(record["daily_reset_hour"], fmt.Sprintf("第 %d 条限流规则每日恢复小时", index))
			if err != nil {
				return accountErrorHandlingRule{}, err
			}
			rule.DailyResetHour = hour
		default:
			day, err := readWeekday(record["weekly_reset_day"], fmt.Sprintf("第 %d 条限流规则每周恢复日期", index))
			if err != nil {
				return accountErrorHandlingRule{}, err
			}
			hour, err := readHour(record["weekly_reset_hour"], fmt.Sprintf("第 %d 条限流规则每周恢复小时", index))
			if err != nil {
				return accountErrorHandlingRule{}, err
			}
			rule.WeeklyResetDay = day
			rule.WeeklyResetHour = hour
		}
	}
	return rule, nil
}

func readTextEqual(value any, target string) bool {
	text, ok := value.(string)
	return ok && text == target
}

func readBoolEqual(value any, target bool) bool {
	flag, ok := value.(bool)
	return ok && flag == target
}

func readRequiredBool(value any, label string) (bool, error) {
	if flag, ok := value.(bool); ok {
		return flag, nil
	}
	return false, fmt.Errorf("%s必须是布尔值", label)
}

func readRequiredString(value any, label string) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s不能为空", label)
	}
	return strings.TrimSpace(text), nil
}

func readRequiredPositiveInt(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number <= 0 {
		return 0, fmt.Errorf("%s必须是大于 0 的整数", label)
	}
	return number, nil
}

func readHour(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 0 || number > 23 {
		return 0, fmt.Errorf("%s必须是 0-23 的整数", label)
	}
	return number, nil
}

func readWeekday(value any, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 0 || number > 6 {
		return 0, fmt.Errorf("%s必须是 0-6 的整数", label)
	}
	return number, nil
}

func readStatusCodes(value any, index int) ([]float64, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("第 %d 条规则状态码必须是数字数组", index)
	}
	output := []float64{}
	seen := map[float64]bool{}
	for _, item := range list {
		number, ok := item.(float64)
		if !ok || number != float64(int64(number)) || number < 100 || number > 599 {
			return nil, fmt.Errorf("第 %d 条规则状态码不合法", index)
		}
		if number >= 200 && number <= 299 {
			return nil, fmt.Errorf("第 %d 条规则的状态码不能填写 2xx 成功状态码，例如 200", index)
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		output = append(output, number)
	}
	return output, nil
}

func readStringList(value any, label string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s必须是字符串数组", label)
	}
	output := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%s必须是字符串数组", label)
		}
		text = strings.TrimSpace(text)
		if seen[text] {
			continue
		}
		seen[text] = true
		output = append(output, text)
	}
	return output, nil
}

func isAllDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// accountErrorPolicyOverride 镜像 AccountErrorPolicyOverride。
type accountErrorPolicyOverride struct {
	SystemRuleID string
	Action       string // 'replace' | 'delete'
	RuleIndex    float64
	HasRuleIndex bool
}

// accountErrorPolicyOverridesRead 镜像读取侧 normalizeAccountErrorPolicyOverrides。
func accountErrorPolicyOverridesRead(value any) ([]accountErrorPolicyOverride, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("错误处理策略覆盖格式无效")
	}
	output := make([]accountErrorPolicyOverride, 0, len(list))
	for index, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 条错误处理策略覆盖格式无效", index+1)
		}
		if !readTextEqual(record["system_rule_id"], systemInsufficientQuotaRuleID) {
			return nil, fmt.Errorf("第 %d 条错误处理策略覆盖的系统规则 ID 无效", index+1)
		}
		action, _ := record["action"].(string)
		if action != "replace" && action != "delete" {
			return nil, fmt.Errorf("第 %d 条错误处理策略覆盖动作无效", index+1)
		}
		allowed := map[string]bool{"system_rule_id": true, "action": true}
		if action == "replace" {
			allowed["rule_index"] = true
		}
		for key := range record {
			if !allowed[key] {
				return nil, fmt.Errorf("第 %d 条错误处理策略覆盖包含不支持字段：%s", index+1, key)
			}
		}
		override := accountErrorPolicyOverride{SystemRuleID: systemInsufficientQuotaRuleID, Action: action}
		if action == "replace" {
			ruleIndex, ok := record["rule_index"].(float64)
			if !ok || ruleIndex != float64(int64(ruleIndex)) || ruleIndex < 0 {
				return nil, fmt.Errorf("第 %d 条错误处理策略覆盖规则索引无效", index+1)
			}
			override.RuleIndex = ruleIndex
			override.HasRuleIndex = true
		}
		output = append(output, override)
	}
	return output, nil
}

// ---------------------------------------------------------------------------
// 规则匹配与恢复时间（accountErrorRuleMatches / accountErrorRuleCooldownUntil）
// ---------------------------------------------------------------------------

// accountErrorRuleMatches 镜像 accountErrorRuleMatches：每个条件列表为空
// 表示该维度不限制；errorCode/searchableText 由调用方先做小写归一。
func accountErrorRuleMatches(rule accountErrorHandlingRule, statusCode int, errorCode, errorType, searchableText string) bool {
	if len(rule.StatusCodes) > 0 {
		matched := false
		for _, code := range rule.StatusCodes {
			if code == float64(statusCode) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.ErrorCodes) > 0 {
		matched := false
		for _, code := range rule.ErrorCodes {
			if strings.ToLower(code) == errorCode {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.ErrorTypes) > 0 {
		matched := false
		for _, typ := range rule.ErrorTypes {
			if strings.ToLower(typ) == errorType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.Keywords) > 0 {
		matched := false
		for _, keyword := range rule.Keywords {
			if strings.Contains(searchableText, strings.ToLower(keyword)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// accountErrorRuleCooldownUntil 镜像 accountErrorRuleCooldownUntil：duration
// 策略加确定性抖动；daily/weekly 对齐到目标小时（过去则顺延一天/一周）。
func accountErrorRuleCooldownUntil(rule accountErrorHandlingRule, now time.Time, seed string) string {
	if rule.ResetStrategy == "duration" {
		intervalMs := int64(math.Max(1, rule.DurationHours) * 3_600_000)
		target := now.UnixMilli() + intervalMs + passiveScheduleDeterministicOffsetMs(intervalMs, seed)
		return time.UnixMilli(target).UTC().Format(rfc3339MillisUTC)
	}
	// Node: target = now 截断到日 + setHours(目标小时)；weekly 再按
	// weekly_reset_day 对齐（过去则顺延一天/一周）。
	resetHour := rule.DailyResetHour
	if rule.ResetStrategy == "weekly" {
		resetHour = rule.WeeklyResetHour
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), int(resetHour), 0, 0, 0, now.Location())
	if rule.ResetStrategy == "weekly" {
		daysAhead := (int(rule.WeeklyResetDay) - int(now.Weekday()) + 7) % 7
		target = target.AddDate(daysAhead, 0, 0)
	}
	if !target.After(now) {
		if rule.ResetStrategy == "weekly" {
			target = target.AddDate(7, 0, 0)
		} else {
			target = target.AddDate(0, 0, 1)
		}
	}
	intervalMs := max64(1, target.UnixMilli()-now.UnixMilli())
	return time.UnixMilli(target.UnixMilli()+passiveScheduleDeterministicOffsetMs(intervalMs, seed)).UTC().Format(rfc3339MillisUTC)
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

// ---------------------------------------------------------------------------
// 被动调度确定性抖动（shared/passive-schedule-jitter.ts）
// ---------------------------------------------------------------------------

// passiveScheduleJitterWindowMs 镜像 passiveScheduleJitterWindowMs。
func passiveScheduleJitterWindowMs(intervalMs int64) int64 {
	var windowMs int64
	switch {
	case intervalMs < 60_000:
		windowMs = min64(30_000, intervalMs/2)
	case intervalMs < 3_600_000:
		windowMs = 30_000
	case intervalMs < 24*3_600_000:
		windowMs = 30 * 60_000
	case intervalMs < 7*24*3_600_000:
		windowMs = 60 * 60_000
	default:
		windowMs = 8 * 60 * 60_000
	}
	half := intervalMs / 2
	if half < 0 {
		half = 0
	}
	return min64(windowMs, half)
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

// passiveScheduleDeterministicOffsetMs 镜像同名函数：FNV-1a 32 位散列的
// JS Math.imul 语义（32 位有符号乘法回绕），采样区间为 [−window, +window]，
// 0 改写为 1。
func passiveScheduleDeterministicOffsetMs(intervalMs int64, seed string) int64 {
	windowMs := passiveScheduleJitterWindowMs(intervalMs)
	if windowMs <= 0 {
		return 0
	}
	// FNV-1a 32 位偏移基数 2166136261 超出 int32 正数范围；Go 常量转换不回绕，
	// 直接书写其 int32 两补码表示（2166136261 - 2^32 = -2128831035）。
	hash := int32(-2128831035)
	for _, r := range seed {
		// Node charCodeAt 是 UTF-16 码元；BMP 内 rune 值与码元一致（种子由
		// 账户 ID / Key 指纹 / 规则名组成，非 BMP 代理对差异可忽略）。
		hash ^= int32(r)
		hash = imul32(hash, 16777619)
	}
	span := windowMs*2 + 1
	offset := int64(uint64(uint32(hash))%uint64(span)) - windowMs
	if offset == 0 {
		return 1
	}
	return offset
}

// imul32 复刻 Math.imul：32 位有符号整数乘法（截断回绕）。
func imul32(a, b int32) int32 {
	return int32(int64(a) * int64(b))
}

// ---------------------------------------------------------------------------
// 额度恢复策略与冷却边界（quota-recovery-policy.ts）
// ---------------------------------------------------------------------------

// quotaRecoveryPolicyRead 镜像读取侧 normalizeQuotaRecoveryPolicy：仅接受
// api_key/oauth/google_oauth 三个键，策略项按 Node 规则严格校验。
func quotaRecoveryPolicyRead(value any) (map[string]any, error) {
	input, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("额度恢复策略必须是对象")
	}
	allowed := map[string]bool{"api_key": true, "oauth": true, "google_oauth": true}
	for key := range input {
		if !allowed[key] {
			return nil, fmt.Errorf("额度恢复策略字段 %s 不受支持", key)
		}
	}
	output := map[string]any{}
	for _, accountType := range []string{"api_key", "oauth", "google_oauth"} {
		scheduleValue, exists := input[accountType]
		if !exists {
			continue
		}
		schedule, err := quotaRecoveryScheduleRead(scheduleValue)
		if err != nil {
			return nil, err
		}
		output[accountType] = schedule
	}
	return output, nil
}

func quotaRecoveryScheduleRead(value any) (map[string]any, error) {
	input, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("额度恢复策略项必须是对象")
	}
	strategy, _ := input["reset_strategy"].(string)
	if strategy != "duration" && strategy != "daily" && strategy != "weekly" {
		return nil, fmt.Errorf("额度恢复策略 reset_strategy 必须是 duration、daily 或 weekly")
	}
	output := map[string]any{"reset_strategy": strategy}
	switch strategy {
	case "duration":
		minutes, err := quotaRecoveryIntegerInRange(input["duration_minutes"], 30, 7*24*60, "duration_minutes")
		if err != nil {
			return nil, err
		}
		output["duration_minutes"] = minutes
	case "daily":
		hour, err := quotaRecoveryIntegerInRange(input["daily_reset_hour"], 0, 23, "daily_reset_hour")
		if err != nil {
			return nil, err
		}
		output["daily_reset_hour"] = hour
	default:
		day, err := quotaRecoveryIntegerInRange(input["weekly_reset_day"], 0, 6, "weekly_reset_day")
		if err != nil {
			return nil, err
		}
		hour, err := quotaRecoveryIntegerInRange(input["weekly_reset_hour"], 0, 23, "weekly_reset_hour")
		if err != nil {
			return nil, err
		}
		output["weekly_reset_day"] = day
		output["weekly_reset_hour"] = hour
	}
	if jitter, exists := input["jitter_minutes"]; exists {
		number, ok := jitter.(float64)
		if !ok || int(number) != 15 {
			return nil, fmt.Errorf("额度恢复策略 jitter_minutes固定15，仅作为兼容字段")
		}
	}
	timezone := "UTC"
	if raw, exists := input["timezone"]; exists {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("额度恢复策略 timezone 无效")
		}
		timezone = strings.TrimSpace(text)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("额度恢复策略 timezone 无效：%s", timezone)
	}
	output["timezone"] = timezone
	output["jitter_minutes"] = float64(15)
	return output, nil
}

func quotaRecoveryIntegerInRange(value any, min, max int, label string) (float64, error) {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || int(number) < min || int(number) > max {
		return 0, fmt.Errorf("额度恢复策略 %s 必须是 %d-%d 的整数", label, min, max)
	}
	return number, nil
}

// quotaRecoveryCooldownUntil 镜像 quotaRecoveryCooldownUntil：duration 策略
// 直接加时长；daily/weekly 在策略时区对齐目标小时（过去则顺延），并叠加
// 确定性被动抖动。
func quotaRecoveryCooldownUntil(policy map[string]any, accountType, seed string, now time.Time) (string, error) {
	schedule := quotaRecoveryScheduleForAccount(policy, accountType)
	boundary, err := quotaRecoveryScheduleBoundary(schedule, now)
	if err != nil {
		return "", err
	}
	intervalMs := max64(1, boundary.UnixMilli()-now.UnixMilli())
	return time.UnixMilli(boundary.UnixMilli()+passiveScheduleDeterministicOffsetMs(intervalMs, seed)).UTC().Format(rfc3339MillisUTC), nil
}

// quotaRecoveryScheduleForAccount 镜像 quotaRecoveryScheduleForAccount：
// api_key 缺省 60 分钟 duration，其余缺省每日 0 点；配置项覆盖缺省项。
func quotaRecoveryScheduleForAccount(policy map[string]any, accountType string) map[string]any {
	defaultKey := accountType
	if defaultKey != "api_key" && defaultKey != "oauth" && defaultKey != "google_oauth" {
		defaultKey = "oauth"
	}
	schedule := map[string]any{}
	if defaultKey == "api_key" {
		schedule["reset_strategy"] = "duration"
		schedule["duration_minutes"] = float64(60)
	} else {
		schedule["reset_strategy"] = "daily"
		schedule["daily_reset_hour"] = float64(0)
	}
	schedule["jitter_minutes"] = float64(15)
	schedule["timezone"] = "UTC"
	configured, _ := policy[defaultKey].(map[string]any)
	if configured == nil {
		return schedule
	}
	for _, key := range []string{"duration_minutes", "daily_reset_hour", "weekly_reset_day", "weekly_reset_hour", "timezone", "jitter_minutes"} {
		if value, exists := configured[key]; exists {
			schedule[key] = value
		}
	}
	// reset_strategy 以配置为准（配置项类型随策略切换重新校验过）。
	if value, exists := configured["reset_strategy"]; exists {
		schedule["reset_strategy"] = value
	}
	return schedule
}

// quotaRecoveryScheduleBoundary 镜像 scheduleBoundary。
func quotaRecoveryScheduleBoundary(schedule map[string]any, now time.Time) (time.Time, error) {
	strategy, _ := schedule["reset_strategy"].(string)
	if strategy == "duration" {
		minutes := 60.0
		if value, ok := schedule["duration_minutes"].(float64); ok {
			minutes = value
		}
		return now.Add(time.Duration(minutes) * time.Minute), nil
	}
	timezone := "UTC"
	if value, ok := schedule["timezone"].(string); ok && value != "" {
		timezone = value
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("额度恢复策略 timezone 无效：%s", timezone)
	}
	local := now.In(location)
	targetHour := 0.0
	if strategy == "weekly" {
		if value, ok := schedule["weekly_reset_hour"].(float64); ok {
			targetHour = value
		}
	} else if value, ok := schedule["daily_reset_hour"].(float64); ok {
		targetHour = value
	}
	dayDelta := 0
	if strategy == "weekly" {
		weeklyDay := 0.0
		if value, ok := schedule["weekly_reset_day"].(float64); ok {
			weeklyDay = value
		}
		dayDelta = (int(weeklyDay) - int(local.Weekday()) + 7) % 7
	}
	candidate := time.Date(local.Year(), local.Month(), local.Day()+dayDelta, int(targetHour), 0, 0, 0, location)
	if !candidate.After(now) {
		if strategy == "weekly" {
			dayDelta += 7
		} else {
			dayDelta++
		}
		candidate = time.Date(local.Year(), local.Month(), local.Day()+dayDelta, int(targetHour), 0, 0, 0, location)
	}
	return candidate, nil
}

// ---------------------------------------------------------------------------
// 上游显式恢复 hint（api-key-quota-recovery.ts extractApiKeyQuotaRecoveryHint）
// ---------------------------------------------------------------------------

// apiKeyQuotaRecoveryMode 镜像 ApiKeyQuotaRecoveryMode。
type apiKeyQuotaRecoveryMode string

const (
	quotaRecoveryModeGeneric       apiKeyQuotaRecoveryMode = "generic"
	quotaRecoveryModeExplicitReset apiKeyQuotaRecoveryMode = "explicit_reset"
)

// apiKeyQuotaRecoveryHint 镜像 ApiKeyQuotaRecoveryHint。
type apiKeyQuotaRecoveryHint struct {
	Mode          apiKeyQuotaRecoveryMode
	CooldownUntil string
	Source        string // 'reset_at' | 'retry_after' | 'provider_header'
}

// quotaRecoveryErrorCode 镜像 quotaRecoveryErrorCode。
func quotaRecoveryErrorCode(mode string) string {
	if mode == string(quotaRecoveryModeExplicitReset) {
		return "api_key_quota_insufficient_reset"
	}
	return "api_key_quota_insufficient"
}

// extractAPIKeyQuotaRecoveryHint 镜像 extractApiKeyQuotaRecoveryHint：
// reset_at 族绝对字段 → reset_after/retry_after 秒数字段 → retry-after /
// 供应商 reset 响应头。
func extractAPIKeyQuotaRecoveryHint(bodyText string, headers map[string]string, now time.Time) *apiKeyQuotaRecoveryHint {
	bodyValue := parseJSONValueLoose(bodyText)
	absolute := findFirstJSONField(bodyValue, []string{"reset_at", "resetAt", "quota_reset_at", "quotaResetAt"})
	if absoluteAt := parseAbsoluteRecoveryTime(absolute); absoluteAt != nil && absoluteAt.After(now) {
		return &apiKeyQuotaRecoveryHint{
			Mode:          quotaRecoveryModeExplicitReset,
			CooldownUntil: absoluteAt.UTC().Format(rfc3339MillisUTC),
			Source:        "reset_at",
		}
	}
	delaySeconds := parsePositiveSeconds(findFirstJSONField(bodyValue, []string{
		"reset_after_seconds",
		"resetAfterSeconds",
		"retry_after_seconds",
		"retryAfterSeconds",
	}))
	if delaySeconds != nil {
		cooldownUntil := now.Add(time.Duration(*delaySeconds) * time.Second)
		return &apiKeyQuotaRecoveryHint{
			Mode:          quotaRecoveryModeExplicitReset,
			CooldownUntil: cooldownUntil.UTC().Format(rfc3339MillisUTC),
			Source:        "reset_at",
		}
	}
	if headers != nil {
		if retryAfter := parseRetryAfterHeader(getCaseInsensitiveHeader(headers, "retry-after"), now); retryAfter != nil {
			return &apiKeyQuotaRecoveryHint{
				Mode:          quotaRecoveryModeExplicitReset,
				CooldownUntil: retryAfter.UTC().Format(rfc3339MillisUTC),
				Source:        "retry_after",
			}
		}
		providerReset := parseProviderResetHeader(firstCaseInsensitiveHeader(headers,
			"x-quota-reset-at", "x-ratelimit-reset", "x-rate-limit-reset"), now)
		if providerReset != nil {
			return &apiKeyQuotaRecoveryHint{
				Mode:          quotaRecoveryModeExplicitReset,
				CooldownUntil: providerReset.UTC().Format(rfc3339MillisUTC),
				Source:        "provider_header",
			}
		}
	}
	return nil
}

func parseJSONValueLoose(text string) any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil
	}
	return parsed
}

func findFirstJSONField(value any, names []string) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range names {
		if child, present := obj[name]; present {
			return child
		}
	}
	// 字段搜索是存在性语义：递归全部子对象。
	for _, child := range obj {
		if found := findFirstJSONField(child, names); found != nil {
			return found
		}
	}
	return nil
}

func parseAbsoluteRecoveryTime(value any) *time.Time {
	var milliseconds float64
	switch typed := value.(type) {
	case float64:
		milliseconds = typed
		if milliseconds > 0 {
			if milliseconds <= 10_000_000_000 {
				milliseconds *= 1000
			}
			seconds := int64(milliseconds / 1000)
			nanos := int64((milliseconds - float64(seconds)*1000) * 1e6)
			parsed := time.Unix(seconds, nanos)
			return &parsed
		}
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		if matched, err := parseFloat64(trimmed); err == nil && matched > 0 {
			if matched <= 10_000_000_000 {
				matched *= 1000
			}
			seconds := int64(matched / 1000)
			nanos := int64((matched - float64(seconds)*1000) * 1e6)
			parsed := time.Unix(seconds, nanos)
			return &parsed
		}
		parsed, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return nil
		}
		return &parsed
	default:
		return nil
	}
}

func parseFloat64(text string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(text), 64)
}

func parsePositiveSeconds(value any) *int64 {
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			seconds := int64(math.Ceil(typed))
			return &seconds
		}
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		parsed, err := parseFloat64(trimmed)
		if err != nil || parsed <= 0 {
			return nil
		}
		seconds := int64(math.Ceil(parsed))
		return &seconds
	default:
		return nil
	}
}

func parseRetryAfterHeader(value string, now time.Time) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if seconds := parsePositiveSeconds(value); seconds != nil {
		parsed := now.Add(time.Duration(*seconds) * time.Second)
		return &parsed
	}
	if absolute := parseAbsoluteRecoveryTime(value); absolute != nil && absolute.After(now) {
		return absolute
	}
	if httpDate, err := time.Parse(time.RFC1123, value); err == nil && httpDate.After(now) {
		return &httpDate
	}
	return nil
}

func parseProviderResetHeader(value string, now time.Time) *time.Time {
	if absolute := parseAbsoluteRecoveryTime(value); absolute != nil && absolute.After(now) {
		return absolute
	}
	return nil
}

func getCaseInsensitiveHeader(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func firstCaseInsensitiveHeader(headers map[string]string, names ...string) string {
	for _, name := range names {
		if value := getCaseInsensitiveHeader(headers, name); value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 归因文案（explicitAccountErrorPolicyReason 的投影输入）
// ---------------------------------------------------------------------------

// accountErrorPayloadSummary 镜像 accountErrorPayloadSummary：脱敏后的
// code/message 摘要（message 与 code 相同则去重）。
func accountErrorPayloadSummary(payload gatewayproto.ErrorPayload) string {
	parts := []string{}
	if code := sanitizeDiagnosticText(payload.Code); code != "" {
		parts = append(parts, code)
	}
	if message := sanitizeDiagnosticText(payload.Message); message != "" && message != payload.Code {
		parts = append(parts, message)
	}
	return strings.Join(parts, "；")
}

func sanitizeDiagnosticText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sanitized := gatewayobs.SanitizeDiagnosticPayload(value)
	if text, ok := sanitized.(string); ok {
		return text
	}
	return value
}
