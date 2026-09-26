package accountsbalance

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// OAuth usage snapshot read projections over the stats-side
// account_usage_snapshots rows: kind='openai_codex' (BUG-0175 D-206, the port
// of backend/src/storage/oauth-usage-loaders.ts +
// account-summary.repository.ts:1452,1575) and kind='xai_grok' (AI账户Grok用量
// 快照设计 §5, the jobs xai-grok-usage-refresh family's billing snapshots).
// The account summary hydrates the oauthUsage field for gpt-provider oauth
// accounts from the Codex rows and for xai-provider oauth accounts from the
// Grok rows. The ListItem-shaped hydration loop stays in the accounts facade
// (balance_subdomain_bridge.go); this package exposes the snapshot load keyed
// by account id.

// OAuthUsageWindow mirrors AccountOAuthUsageWindow.
type OAuthUsageWindow struct {
	Utilization      float64  `json:"utilization"`
	ResetsAt         *string  `json:"resetsAt,omitempty"`
	RemainingSeconds int      `json:"remainingSeconds"`
	WindowMinutes    *float64 `json:"windowMinutes,omitempty"`
}

// OAuthUsageSnapshot mirrors AccountOAuthUsageSnapshot: the openai_codex form
// uses FiveHour/SevenDay; the xai_grok form (AI账户Grok用量快照设计 §3/§5)
// uses the flat Grok fields below.
type OAuthUsageSnapshot struct {
	Kind             string            `json:"kind"`
	Source           *string           `json:"source,omitempty"`
	UpdatedAt        string            `json:"updatedAt"`
	RefreshStatus    *string           `json:"refreshStatus,omitempty"`
	LastAttemptAt    *string           `json:"lastAttemptAt,omitempty"`
	LastSuccessAt    *string           `json:"lastSuccessAt,omitempty"`
	NextRefreshAfter *string           `json:"nextRefreshAfter,omitempty"`
	LastErrorMessage *string           `json:"lastErrorMessage,omitempty"`
	FiveHour         *OAuthUsageWindow `json:"fiveHour,omitempty"`
	SevenDay         *OAuthUsageWindow `json:"sevenDay,omitempty"`
	// xai_grok 形态：额度已用百分比、订阅周期与套餐；ProductUsage 保留
	// grok_product_usage_json 的原样 JSON，解析失败或缺失时整体省略。
	UsedPercent         *float64        `json:"usedPercent,omitempty"`
	PeriodType          *string         `json:"periodType,omitempty"`
	PeriodStart         *string         `json:"periodStart,omitempty"`
	PeriodEnd           *string         `json:"periodEnd,omitempty"`
	SubscriptionTier    *string         `json:"subscriptionTier,omitempty"`
	ProductUsage        json.RawMessage `json:"productUsage,omitempty"`
	OnDemandUsedPercent *float64        `json:"onDemandUsedPercent,omitempty"`
}

// LoadOpenAICodexUsageSnapshots mirrors loadOpenAICodexUsageSnapshotsByAccountIds:
// the kind='openai_codex' snapshot rows keyed by account id (chunked IN).
func (s *Service) LoadOpenAICodexUsageSnapshots(ctx context.Context, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
	return s.loadOAuthUsageSnapshots(ctx, "openai_codex", accountIDs)
}

// LoadXAIGrokUsageSnapshots mirrors LoadOpenAICodexUsageSnapshots for the
// kind='xai_grok' rows the jobs xai-grok-usage-refresh family upserts
// (AI账户Grok用量快照设计 §4-§5).
func (s *Service) LoadXAIGrokUsageSnapshots(ctx context.Context, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
	return s.loadOAuthUsageSnapshots(ctx, "xai_grok", accountIDs)
}

// loadOAuthUsageSnapshots is the shared chunked-IN reader behind both kind
// projections; kind is a compile-site literal, never caller input.
func (s *Service) loadOAuthUsageSnapshots(ctx context.Context, kind string, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range accountIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	result := map[string]*OAuthUsageSnapshot{}
	if len(ids) == 0 {
		return result, nil
	}
	const chunkSize = 900
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		rows, err := s.statsDB().QueryContext(ctx, s.store.Bind(`SELECT account_id, source, snapshot_json, refresh_status,
				last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
			FROM `+s.StatsTable("account_usage_snapshots")+`
			WHERE kind = '`+kind+`' AND account_id IN (`+accountscore.Placeholders(len(chunk))+`)`), accountscore.AnySlice(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				accountID    string
				source       sql.NullString
				snapshotJSON string
				refresh      sql.NullString
				lastAttempt  sql.NullString
				lastSuccess  sql.NullString
				nextRefresh  sql.NullString
				lastError    sql.NullString
				updatedAt    string
			)
			if err := rows.Scan(&accountID, &source, &snapshotJSON, &refresh, &lastAttempt, &lastSuccess, &nextRefresh, &lastError, &updatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			snapshot, err := parseOAuthUsageSnapshotRow(kind, source.String, snapshotJSON, refresh.String,
				lastAttempt.String, lastSuccess.String, nextRefresh.String, lastError.String, updatedAt)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if snapshot == nil {
				continue
			}
			result[accountID] = snapshot
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

// parseOAuthUsageSnapshotRow dispatches the row to the kind projection; an
// unparseable snapshot_json skips the row for both kinds.
func parseOAuthUsageSnapshotRow(kind, source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	if kind == "xai_grok" {
		return XAIGrokUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt,
			lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt)
	}
	return OAuthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt,
		lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt)
}

// OAuthUsageSnapshotFromRow mirrors oauthUsageSnapshotsFromRows: an
// unparseable snapshot_json skips the row; timestamps render as RFC3339
// instants (Node optionalInstant / requiredRfc3339Instant).
func OAuthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	out, snapshot, err := oauthUsageSnapshotHeader("openai_codex", source, snapshotJSON, refreshStatus,
		lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt)
	if err != nil || out == nil {
		return out, err
	}
	fiveHour, err := OAuthUsageWindowFromSnapshot(snapshot, "5h", out.UpdatedAt)
	if err != nil {
		return nil, err
	}
	out.FiveHour = fiveHour
	sevenDay, err := OAuthUsageWindowFromSnapshot(snapshot, "7d", out.UpdatedAt)
	if err != nil {
		return nil, err
	}
	out.SevenDay = sevenDay
	return out, nil
}

// XAIGrokUsageSnapshotFromRow mirrors OAuthUsageSnapshotFromRow for the
// kind='xai_grok' rows (AI账户Grok用量快照设计 §3/§5): the grok_* snapshot
// fields are individually optional — a missing field or an unparseable
// grok_period_start/end skips just that field.
func XAIGrokUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	out, snapshot, err := oauthUsageSnapshotHeader("xai_grok", source, snapshotJSON, refreshStatus,
		lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt)
	if err != nil || out == nil {
		return out, err
	}
	if percent, ok := NumberFromSnapshot(snapshot["grok_credit_used_percent"]); ok {
		out.UsedPercent = percent
	}
	if text := OptionalSnapshotString(snapshot["grok_period_type"]); text != "" {
		out.PeriodType = &text
	}
	for _, pair := range []struct {
		key  string
		dest **string
	}{
		{"grok_period_start", &out.PeriodStart},
		{"grok_period_end", &out.PeriodEnd},
	} {
		text := OptionalSnapshotString(snapshot[pair.key])
		if strings.TrimSpace(text) == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(text)); err != nil {
			// 时间字段解析失败只跳过该字段（缺字段不致命的容错对齐）。
			continue
		}
		*pair.dest = &text
	}
	if text := OptionalSnapshotString(snapshot["grok_subscription_tier"]); text != "" {
		out.SubscriptionTier = &text
	}
	// productUsage 原样透传；无法校验为合法 JSON 时整体省略，避免污染外层
	// 响应序列化。
	if text := OptionalSnapshotString(snapshot["grok_product_usage_json"]); json.Valid([]byte(text)) {
		out.ProductUsage = json.RawMessage(text)
	}
	if percent, ok := NumberFromSnapshot(snapshot["grok_on_demand_used_percent"]); ok {
		out.OnDemandUsedPercent = percent
	}
	return out, nil
}

// oauthUsageSnapshotHeader parses the row-level header shared by both kind
// projections: updated_at, source, the refresh-state timestamps and the last
// error; an unparseable snapshot_json returns (nil, nil, nil) to skip the row.
func oauthUsageSnapshotHeader(kind, source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, map[string]any, error) {
	updatedText, err := RequiredRFC3339Instant(updatedAt, "account_usage_snapshots.updated_at")
	if err != nil {
		return nil, nil, err
	}
	snapshot := map[string]any{}
	if strings.TrimSpace(snapshotJSON) != "" {
		if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			return nil, nil, nil
		}
	}
	out := &OAuthUsageSnapshot{Kind: kind, UpdatedAt: updatedText}
	if source != "" {
		text := source
		out.Source = &text
	} else if text := OptionalSnapshotString(snapshot["source"]); text != "" {
		out.Source = &text
	}
	if refreshStatus != "" {
		text := refreshStatus
		out.RefreshStatus = &text
	}
	for _, pair := range []struct {
		key   string
		value string
		dest  **string
	}{
		{"account_usage_snapshots.last_attempt_at", lastAttemptAt, &out.LastAttemptAt},
		{"account_usage_snapshots.last_success_at", lastSuccessAt, &out.LastSuccessAt},
		{"account_usage_snapshots.next_refresh_after", nextRefreshAfter, &out.NextRefreshAfter},
	} {
		if strings.TrimSpace(pair.value) == "" {
			continue
		}
		text, err := RequiredRFC3339Instant(pair.value, pair.key)
		if err != nil {
			return nil, nil, err
		}
		*pair.dest = &text
	}
	if lastErrorMessage != "" {
		text := lastErrorMessage
		out.LastErrorMessage = &text
	}
	return out, snapshot, nil
}

// OAuthUsageWindowFromSnapshot mirrors oauthUsageWindowFromSnapshot: the
// utilization presence gates the window; an elapsed reset collapses the
// utilization to 0 with remainingSeconds 0.
func OAuthUsageWindowFromSnapshot(snapshot map[string]any, window, updatedAt string) (*OAuthUsageWindow, error) {
	utilization, hasUtilization := NumberFromSnapshot(snapshot["codex_"+window+"_used_percent"])
	if !hasUtilization {
		return nil, nil
	}
	var resetAt *string
	rawReset, hasReset := snapshot["codex_"+window+"_reset_at"]
	if !hasReset || rawReset == nil {
		// resetAtFromSeconds: a missing/non-positive offset leaves the reset
		// instant undefined.
		if seconds, ok := NumberFromSnapshot(snapshot["codex_"+window+"_reset_after_seconds"]); ok && *seconds > 0 {
			base, err := RFC3339InstantMilliseconds(updatedAt)
			if err != nil {
				return nil, &accountscore.ValidationError{Message: "account_usage_snapshots.updated_at 必须是带 Z 或数值 offset 的 RFC3339 时间"}
			}
			reset := accountscore.IsoMillis(time.UnixMilli(base + int64(*seconds*1000)).UTC())
			resetAt = &reset
		}
	} else {
		text, ok := rawReset.(string)
		if !ok {
			return nil, &accountscore.ValidationError{Message: "account_usage_snapshots " + window + " resetAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
		canonical, err := RequiredRFC3339Instant(text, "account_usage_snapshots "+window+" resetAt")
		if err != nil {
			return nil, err
		}
		resetAt = &canonical
	}
	var remainingSeconds int
	utilizationValue := *utilization
	if resetAt != nil {
		ms, err := RFC3339InstantMilliseconds(*resetAt)
		if err != nil {
			return nil, &accountscore.ValidationError{Message: "account_usage_snapshots " + window + " resetAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
		nowMS := int64(time.Now().UnixMilli())
		remaining := int(math.Ceil(float64(ms-nowMS) / 1000))
		if remaining < 0 {
			remaining = 0
		}
		remainingSeconds = remaining
		if ms <= nowMS {
			utilizationValue = 0
		}
	}
	out := &OAuthUsageWindow{Utilization: utilizationValue, ResetsAt: resetAt, RemainingSeconds: remainingSeconds}
	if minutes, ok := NumberFromSnapshot(snapshot["codex_"+window+"_window_minutes"]); ok {
		out.WindowMinutes = minutes
	}
	return out, nil
}

// NumberFromSnapshot mirrors numberFromUnknown: finite JSON numbers only.
func NumberFromSnapshot(value any) (*float64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, false
	}
	return &number, true
}

// OptionalSnapshotString mirrors optionalString.
func OptionalSnapshotString(value any) string {
	text, _ := value.(string)
	return text
}

// RequiredRFC3339Instant mirrors requiredRfc3339Instant: the value must parse
// as an RFC3339 instant; the original text passes through.
func RequiredRFC3339Instant(value, label string) (string, error) {
	if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err != nil {
		return "", &accountscore.ValidationError{Message: label + " 必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	return value, nil
}

// RFC3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func RFC3339InstantMilliseconds(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.UnixMilli(), nil
}
