package gatewayupstream

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Codex usage headers, migrated from adapters/gpt-codex/usage.service.ts.
// The persist side goes through the RecordMaintenanceQueue port (the Node
// record-maintenance queue belongs to another slice).

// OpenAICodexUsageSnapshot mirrors OpenAICodexUsageSnapshot.
type OpenAICodexUsageSnapshot struct {
	PrimaryUsedPercent          *float64
	PrimaryResetAfterSeconds    *int64
	PrimaryWindowMinutes        *int64
	SecondaryUsedPercent        *float64
	SecondaryResetAfterSeconds  *int64
	SecondaryWindowMinutes      *int64
	PrimaryOverSecondaryPercent *float64
	UpdatedAt                   string
}

// NormalizedCodexLimits mirrors NormalizedCodexLimits.
type NormalizedCodexLimits struct {
	Used5hPercent   *float64
	Reset5hSeconds  *int64
	Window5hMinutes *int64
	Used7dPercent   *float64
	Reset7dSeconds  *int64
	Window7dMinutes *int64
}

// CodexWindowCandidate mirrors the local candidate type.
type CodexWindowCandidate struct {
	UsedPercent       *float64
	ResetAfterSeconds *int64
	WindowMinutes     *int64
}

// RecordMaintenanceJob mirrors the job envelope the queue port receives.
type RecordMaintenanceJob struct {
	Type      string
	AccountID string
	Kind      string
	Source    string
	Snapshot  map[string]any
	UpdatedAt string
}

// RecordMaintenanceQueue mirrors the enqueue surface of
// record-maintenance/record-maintenance-queue.service.ts.
type RecordMaintenanceQueue interface {
	EnqueueRecordMaintenanceJob(job RecordMaintenanceJob)
}

// Anthropic unified rate limit usage headers (AI账户Grok用量快照设计 §8,
// 证据背书：CLIProxyAPI claude_ratelimit.go)：每个上游响应（不只 429）携带
// 的 5h/7d 双窗利用率与重置时间，被动采集侧与 codex 的 x-codex-* 头同构。
// Job kind 固定 anthropic_claude；source 语义由规格固定为
// anthropic_unified_headers（gatewaycodex.AnthropicUsageSnapshotSource），
// 不随成功/失败面变化。
const (
	// AccountUsageSnapshotKindAnthropicClaude 是 anthropic OAuth 快照行 kind。
	AccountUsageSnapshotKindAnthropicClaude = "anthropic_claude"

	anthropicUnifiedStatusHeader  = "Anthropic-Ratelimit-Unified-Status"
	anthropic5hUtilizationHeader  = "Anthropic-Ratelimit-Unified-5h-Utilization"
	anthropic5hResetHeader        = "Anthropic-Ratelimit-Unified-5h-Reset"
	anthropic7dUtilizationHeader  = "Anthropic-Ratelimit-Unified-7d-Utilization"
	anthropic7dResetHeader        = "Anthropic-Ratelimit-Unified-7d-Reset"
	anthropicUsageTimestampLayout = "2006-01-02T15:04:05.000Z"
)

// AnthropicUsageSnapshot 是解析后的 unified rate limit 头快照：利用率以
// 百分比承载（0-1 浮点 ×100），重置时间统一为 UTC 毫秒精度的 RFC3339 文本；
// 指针/空串字段表示上游未上报（缺字段不致命）。
type AnthropicUsageSnapshot struct {
	UnifiedStatus string
	Used5hPercent *float64
	Reset5hAt     string
	Used7dPercent *float64
	Reset7dAt     string
	UpdatedAt     string
}

// ParseAnthropicUsageHeaders parses the anthropic unified rate limit headers
// (AI账户Grok用量快照设计 §8.1)。nil headers 与五个头全部缺失都返回 nil；
// utilization 的 NaN/Inf 与重置时间的不可解析格式只跳过该字段。
func ParseAnthropicUsageHeaders(headers http.Header) *AnthropicUsageSnapshot {
	if headers == nil {
		return nil
	}
	// Node Date#toISOString() 惯例与 codex 侧一致：UTC + 固定毫秒三位。
	snapshot := &AnthropicUsageSnapshot{UpdatedAt: time.Now().UTC().Format(anthropicUsageTimestampLayout)}
	snapshot.UnifiedStatus = HeaderValueOf(headers, anthropicUnifiedStatusHeader)
	snapshot.Used5hPercent = anthropicUtilizationPercent(headers, anthropic5hUtilizationHeader)
	snapshot.Reset5hAt = anthropicResetAt(headers, anthropic5hResetHeader)
	snapshot.Used7dPercent = anthropicUtilizationPercent(headers, anthropic7dUtilizationHeader)
	snapshot.Reset7dAt = anthropicResetAt(headers, anthropic7dResetHeader)
	if snapshot.UnifiedStatus == "" && snapshot.Used5hPercent == nil && snapshot.Reset5hAt == "" &&
		snapshot.Used7dPercent == nil && snapshot.Reset7dAt == "" {
		return nil
	}
	return snapshot
}

// anthropicUtilizationPercent 读取 0-1 浮点利用率并换算为百分比
// （0.14 → 14）；非有限数值视为缺失。乘法结果按百分比两位小数取整，
// 吸收 0.14*100 的二进制浮点毛刺（14.000000000000002 → 14）。
func anthropicUtilizationPercent(headers http.Header, key string) *float64 {
	value := HeaderValueOf(headers, key)
	if value == "" {
		return nil
	}
	utilization := NumberValueOf(value)
	if utilization == nil || math.IsNaN(*utilization) || math.IsInf(*utilization, 0) {
		return nil
	}
	percent := math.Round(*utilization*100*100) / 100
	return &percent
}

// anthropicResetAt 归一重置时间为 UTC 毫秒 RFC3339 文本。兼容三种上游格式
// （CLIProxyAPI parseUnixOrTimestamp 同序）：unix 秒浮点、RFC3339、HTTP 时间
// （RFC1123 GMT）；解析失败跳过该字段（缺字段不致命）。
func anthropicResetAt(headers http.Header, key string) string {
	value := HeaderValueOf(headers, key)
	if value == "" {
		return ""
	}
	text := strings.TrimSpace(value)
	if seconds, err := strconv.ParseFloat(text, 64); err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds > 0 {
		return time.UnixMilli(int64(seconds * 1000)).UTC().Format(anthropicUsageTimestampLayout)
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.UTC().Format(anthropicUsageTimestampLayout)
	}
	if parsed, err := http.ParseTime(text); err == nil {
		return parsed.UTC().Format(anthropicUsageTimestampLayout)
	}
	return ""
}

// PersistAnthropicUsageHeaders 镜像 PersistOpenAICodexUsageHeaders 的入队
// 契约：构造维护 job 并经端口入队；无 anthropic 头数据时返回 false。
func PersistAnthropicUsageHeaders(queue RecordMaintenanceQueue, accountID string, headers http.Header, source string) bool {
	job := BuildAnthropicUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return false
	}
	queue.EnqueueRecordMaintenanceJob(*job)
	return true
}

// BuildAnthropicUsageRecordMaintenanceJob projects the anthropic unified
// rate limit headers onto the account_usage_snapshot_upsert job envelope
// (AI账户Grok用量快照设计 §8.2)。payload 字段按 claude_ 前缀全部可选缺失
// 不写；nil 表示头里没有任何 anthropic 用量数据。
func BuildAnthropicUsageRecordMaintenanceJob(accountID string, headers http.Header, source string) *RecordMaintenanceJob {
	snapshot := ParseAnthropicUsageHeaders(headers)
	if snapshot == nil {
		return nil
	}
	payload := map[string]any{
		// 与 codex_usage_updated_at 同形：Node toISOString 毫秒三位。
		"claude_usage_updated_at": snapshot.UpdatedAt,
	}
	if source != "" {
		payload["source"] = source
	}
	if snapshot.Used5hPercent != nil {
		payload["claude_5h_used_percent"] = *snapshot.Used5hPercent
	}
	if snapshot.Reset5hAt != "" {
		payload["claude_5h_reset_at"] = snapshot.Reset5hAt
	}
	if snapshot.Used7dPercent != nil {
		payload["claude_7d_used_percent"] = *snapshot.Used7dPercent
	}
	if snapshot.Reset7dAt != "" {
		payload["claude_7d_reset_at"] = snapshot.Reset7dAt
	}
	if snapshot.UnifiedStatus != "" {
		payload["claude_unified_status"] = snapshot.UnifiedStatus
	}
	return &RecordMaintenanceJob{
		Type:      "account_usage_snapshot_upsert",
		AccountID: accountID,
		Kind:      AccountUsageSnapshotKindAnthropicClaude,
		Source:    source,
		Snapshot:  payload,
		UpdatedAt: snapshot.UpdatedAt,
	}
}

// ParseOpenAICodexUsageHeaders mirrors parseOpenAICodexUsageHeaders.
func ParseOpenAICodexUsageHeaders(headers http.Header) *OpenAICodexUsageSnapshot {
	if headers == nil {
		return nil
	}
	// Node Date#toISOString() always emits UTC with millisecond precision.
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
	hasData := false
	assign := func(key string, apply func(value float64)) {
		value := numberHeader(headers, key)
		if value == nil {
			return
		}
		apply(*value)
		hasData = true
	}
	assign("x-codex-primary-used-percent", func(value float64) { snapshot.PrimaryUsedPercent = &value })
	assign("x-codex-primary-reset-after-seconds", func(value float64) { truncated := int64(value); snapshot.PrimaryResetAfterSeconds = &truncated })
	assign("x-codex-primary-window-minutes", func(value float64) { truncated := int64(value); snapshot.PrimaryWindowMinutes = &truncated })
	assign("x-codex-secondary-used-percent", func(value float64) { snapshot.SecondaryUsedPercent = &value })
	assign("x-codex-secondary-reset-after-seconds", func(value float64) { truncated := int64(value); snapshot.SecondaryResetAfterSeconds = &truncated })
	assign("x-codex-secondary-window-minutes", func(value float64) { truncated := int64(value); snapshot.SecondaryWindowMinutes = &truncated })
	assign("x-codex-primary-over-secondary-limit-percent", func(value float64) { snapshot.PrimaryOverSecondaryPercent = &value })

	if !hasData {
		return nil
	}
	return snapshot
}

// PersistOpenAICodexUsageHeaders mirrors persistOpenAICodexUsageHeadersAsync:
// builds the maintenance job and enqueues it through the port; returns false
// when there is nothing to persist.
func PersistOpenAICodexUsageHeaders(queue RecordMaintenanceQueue, accountID string, headers http.Header, source string) bool {
	job := BuildOpenAICodexUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return false
	}
	queue.EnqueueRecordMaintenanceJob(*job)
	return true
}

// BuildOpenAICodexUsageRecordMaintenanceJob mirrors
// buildOpenAICodexUsageRecordMaintenanceJob (usage.service.ts:74-87): parse
// the codex headers, project the snapshot payload and normalize the job
// envelope for the record-maintenance channel; nil when the headers carry no
// codex usage data.
func BuildOpenAICodexUsageRecordMaintenanceJob(accountID string, headers http.Header, source string) *RecordMaintenanceJob {
	return buildOpenAICodexUsageRecordMaintenanceJob(accountID, headers, source)
}

func buildOpenAICodexUsageRecordMaintenanceJob(accountID string, headers http.Header, source string) *RecordMaintenanceJob {
	snapshot := ParseOpenAICodexUsageHeaders(headers)
	if snapshot == nil {
		return nil
	}
	fallbackNow := time.Now()
	payload := BuildOpenAICodexUsageSnapshotPayload(*snapshot, fallbackNow, source)
	if len(payload) == 0 {
		return nil
	}
	updatedAt, _ := payload["codex_usage_updated_at"].(string)
	if updatedAt == "" {
		updatedAt = snapshot.UpdatedAt
	}
	return &RecordMaintenanceJob{
		Type:      "account_usage_snapshot_upsert",
		AccountID: accountID,
		Kind:      "openai_codex",
		Source:    source,
		Snapshot:  payload,
		UpdatedAt: updatedAt,
	}
}

func BuildOpenAICodexUsageSnapshotPayload(snapshot OpenAICodexUsageSnapshot, fallbackNow time.Time, source string) map[string]any {
	baseTime := ParseIsoDate(snapshot.UpdatedAt)
	if baseTime == nil {
		baseTime = &fallbackNow
	}
	payload := map[string]any{
		// Keep the persisted payload byte-shape aligned with Node
		// Date#toISOString(), including the mandatory three millisecond digits.
		"codex_usage_updated_at": baseTime.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if source != "" {
		payload["source"] = source
	}

	if snapshot.PrimaryUsedPercent != nil {
		payload["codex_primary_used_percent"] = *snapshot.PrimaryUsedPercent
	}
	if snapshot.PrimaryResetAfterSeconds != nil {
		payload["codex_primary_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
	}
	if snapshot.PrimaryWindowMinutes != nil {
		payload["codex_primary_window_minutes"] = *snapshot.PrimaryWindowMinutes
	}
	if snapshot.SecondaryUsedPercent != nil {
		payload["codex_secondary_used_percent"] = *snapshot.SecondaryUsedPercent
	}
	if snapshot.SecondaryResetAfterSeconds != nil {
		payload["codex_secondary_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
	}
	if snapshot.SecondaryWindowMinutes != nil {
		payload["codex_secondary_window_minutes"] = *snapshot.SecondaryWindowMinutes
	}
	if snapshot.PrimaryOverSecondaryPercent != nil {
		payload["codex_primary_over_secondary_percent"] = *snapshot.PrimaryOverSecondaryPercent
	}

	normalized := normalizeOpenAICodexUsageSnapshot(snapshot)
	if normalized == nil {
		return payload
	}

	if normalized.Used5hPercent != nil {
		payload["codex_5h_used_percent"] = *normalized.Used5hPercent
	}
	if normalized.Reset5hSeconds != nil {
		payload["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
	}
	if normalized.Window5hMinutes != nil {
		payload["codex_5h_window_minutes"] = *normalized.Window5hMinutes
	}
	if normalized.Used7dPercent != nil {
		payload["codex_7d_used_percent"] = *normalized.Used7dPercent
	}
	if normalized.Reset7dSeconds != nil {
		payload["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
	}
	if normalized.Window7dMinutes != nil {
		payload["codex_7d_window_minutes"] = *normalized.Window7dMinutes
	}

	if reset5hAt := ResetAtFromSeconds(*baseTime, normalized.Reset5hSeconds); reset5hAt != "" {
		payload["codex_5h_reset_at"] = reset5hAt
	}
	if reset7dAt := ResetAtFromSeconds(*baseTime, normalized.Reset7dSeconds); reset7dAt != "" {
		payload["codex_7d_reset_at"] = reset7dAt
	}

	return payload
}

func normalizeOpenAICodexUsageSnapshot(snapshot OpenAICodexUsageSnapshot) *NormalizedCodexLimits {
	normalized := &NormalizedCodexLimits{}
	primary := CodexWindowCandidate{
		UsedPercent:       snapshot.PrimaryUsedPercent,
		ResetAfterSeconds: snapshot.PrimaryResetAfterSeconds,
		WindowMinutes:     snapshot.PrimaryWindowMinutes,
	}
	secondary := CodexWindowCandidate{
		UsedPercent:       snapshot.SecondaryUsedPercent,
		ResetAfterSeconds: snapshot.SecondaryResetAfterSeconds,
		WindowMinutes:     snapshot.SecondaryWindowMinutes,
	}
	primaryKey := windowKeyFromMinutes(primary.WindowMinutes)
	secondaryKey := windowKeyFromMinutes(secondary.WindowMinutes)

	if primaryKey != "" {
		AssignNormalizedWindow(normalized, primaryKey, primary)
	}
	if secondaryKey != "" {
		AssignNormalizedWindow(normalized, secondaryKey, secondary)
	}

	if normalized.Used5hPercent == nil && normalized.Reset5hSeconds == nil && normalized.Window5hMinutes == nil &&
		normalized.Used7dPercent == nil && normalized.Reset7dSeconds == nil && normalized.Window7dMinutes == nil {
		return nil
	}
	return normalized
}

func windowKeyFromMinutes(minutes *int64) string {
	if minutes == nil || *minutes <= 0 {
		return ""
	}
	if *minutes <= 360 {
		return "5h"
	}
	return "7d"
}

func AssignNormalizedWindow(normalized *NormalizedCodexLimits, key string, candidate CodexWindowCandidate) {
	if candidate.WindowMinutes != nil && *candidate.WindowMinutes <= 0 {
		return
	}
	if candidate.UsedPercent == nil && candidate.ResetAfterSeconds == nil && candidate.WindowMinutes == nil {
		return
	}
	if key == "5h" {
		normalized.Used5hPercent = candidate.UsedPercent
		normalized.Reset5hSeconds = candidate.ResetAfterSeconds
		normalized.Window5hMinutes = candidate.WindowMinutes
		return
	}
	normalized.Used7dPercent = candidate.UsedPercent
	normalized.Reset7dSeconds = candidate.ResetAfterSeconds
	normalized.Window7dMinutes = candidate.WindowMinutes
}

func numberHeader(headers http.Header, key string) *float64 {
	value := HeaderValueOf(headers, key)
	if value == "" {
		return nil
	}
	return NumberValueOf(value)
}

func NumberValueOf(value string) *float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func ResetAtFromSeconds(baseTime time.Time, seconds *int64) string {
	if seconds == nil {
		return ""
	}
	reset := baseTime.Add(time.Duration(MaxInt64(0, *seconds)) * time.Second)
	return reset.UTC().Format("2006-01-02T15:04:05.000Z")
}

func ParseIsoDate(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		// Node Date.parse also accepts loose ISO forms; try a second layout.
		loose, looseErr := time.Parse("2006-01-02T15:04:05.999Z0700", value)
		if looseErr != nil {
			return nil
		}
		return &loose
	}
	return &parsed
}
