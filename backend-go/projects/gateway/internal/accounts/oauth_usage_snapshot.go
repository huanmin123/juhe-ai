package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// OpenAI Codex OAuth usage snapshot read projection (BUG-0175 D-206), the
// port of backend/src/storage/oauth-usage-loaders.ts +
// account-summary.repository.ts:1452,1575: the account summary hydrates the
// oauthUsage field for gpt-provider oauth accounts from the stats-side
// account_usage_snapshots rows (kind='openai_codex').

// OAuthUsageWindow mirrors AccountOAuthUsageWindow.
type OAuthUsageWindow struct {
	Utilization      float64  `json:"utilization"`
	ResetsAt         *string  `json:"resetsAt,omitempty"`
	RemainingSeconds int      `json:"remainingSeconds"`
	WindowMinutes    *float64 `json:"windowMinutes,omitempty"`
}

// OAuthUsageSnapshot mirrors AccountOAuthUsageSnapshot.
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
}

// hydrateOAuthUsageSnapshots mirrors the accountSummariesFromRows hydration
// (account-summary.repository.ts:1452 + :1575): gpt-provider oauth rows carry
// the Codex usage snapshot of the fact (credential-source) account id;
// everything else stays undefined.
func (s *Store) hydrateOAuthUsageSnapshots(ctx context.Context, items []ListItem) error {
	factIDs := []string{}
	seen := map[string]bool{}
	eligible := func(item ListItem) bool {
		return isGptVendorCodeToken(item.ProviderCode) && item.Type == "oauth"
	}
	for _, item := range items {
		if !eligible(item) {
			continue
		}
		factID := item.ID
		if item.AuthorizationInstanceSourceAccountID != nil && *item.AuthorizationInstanceSourceAccountID != "" {
			factID = *item.AuthorizationInstanceSourceAccountID
		}
		if !seen[factID] {
			seen[factID] = true
			factIDs = append(factIDs, factID)
		}
	}
	if len(factIDs) == 0 {
		return nil
	}
	snapshots, err := s.loadOpenAICodexUsageSnapshots(ctx, factIDs)
	if err != nil {
		return err
	}
	for index := range items {
		if !eligible(items[index]) {
			continue
		}
		factID := items[index].ID
		if items[index].AuthorizationInstanceSourceAccountID != nil && *items[index].AuthorizationInstanceSourceAccountID != "" {
			factID = *items[index].AuthorizationInstanceSourceAccountID
		}
		if snapshot, ok := snapshots[factID]; ok {
			items[index].OAuthUsage = snapshot
		}
	}
	return nil
}

// loadOpenAICodexUsageSnapshots mirrors loadOpenAICodexUsageSnapshotsByAccountIds:
// the kind='openai_codex' snapshot rows keyed by account id (chunked IN).
func (s *Store) loadOpenAICodexUsageSnapshots(ctx context.Context, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
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
		rows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_id, source, snapshot_json, refresh_status,
				last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
			FROM `+s.statsTable("account_usage_snapshots")+`
			WHERE kind = 'openai_codex' AND account_id IN (`+placeholders(len(chunk))+`)`), anySlice(chunk)...)
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
			snapshot, err := oauthUsageSnapshotFromRow(source.String, snapshotJSON, refresh.String,
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

// oauthUsageSnapshotFromRow mirrors oauthUsageSnapshotsFromRows: an
// unparseable snapshot_json skips the row; timestamps render as RFC3339
// instants (Node optionalInstant / requiredRfc3339Instant).
func oauthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	updatedText, err := requiredRFC3339Instant(updatedAt, "account_usage_snapshots.updated_at")
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{}
	if strings.TrimSpace(snapshotJSON) != "" {
		if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			return nil, nil
		}
	}
	out := &OAuthUsageSnapshot{Kind: "openai_codex", UpdatedAt: updatedText}
	if source != "" {
		text := source
		out.Source = &text
	} else if text := optionalSnapshotString(snapshot["source"]); text != "" {
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
		text, err := requiredRFC3339Instant(pair.value, pair.key)
		if err != nil {
			return nil, err
		}
		*pair.dest = &text
	}
	if lastErrorMessage != "" {
		text := lastErrorMessage
		out.LastErrorMessage = &text
	}
	fiveHour, err := oauthUsageWindowFromSnapshot(snapshot, "5h", updatedText)
	if err != nil {
		return nil, err
	}
	out.FiveHour = fiveHour
	sevenDay, err := oauthUsageWindowFromSnapshot(snapshot, "7d", updatedText)
	if err != nil {
		return nil, err
	}
	out.SevenDay = sevenDay
	return out, nil
}

// oauthUsageWindowFromSnapshot mirrors oauthUsageWindowFromSnapshot: the
// utilization presence gates the window; an elapsed reset collapses the
// utilization to 0 with remainingSeconds 0.
func oauthUsageWindowFromSnapshot(snapshot map[string]any, window, updatedAt string) (*OAuthUsageWindow, error) {
	utilization, hasUtilization := numberFromSnapshot(snapshot["codex_"+window+"_used_percent"])
	if !hasUtilization {
		return nil, nil
	}
	var resetAt *string
	rawReset, hasReset := snapshot["codex_"+window+"_reset_at"]
	if !hasReset || rawReset == nil {
		// resetAtFromSeconds: a missing/non-positive offset leaves the reset
		// instant undefined.
		if seconds, ok := numberFromSnapshot(snapshot["codex_"+window+"_reset_after_seconds"]); ok && *seconds > 0 {
			base, err := rfc3339InstantMilliseconds(updatedAt)
			if err != nil {
				return nil, &ValidationError{Message: "account_usage_snapshots.updated_at 必须是带 Z 或数值 offset 的 RFC3339 时间"}
			}
			reset := isoMillis(time.UnixMilli(base + int64(*seconds*1000)).UTC())
			resetAt = &reset
		}
	} else {
		text, ok := rawReset.(string)
		if !ok {
			return nil, &ValidationError{Message: "account_usage_snapshots " + window + " resetAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
		}
		canonical, err := requiredRFC3339Instant(text, "account_usage_snapshots "+window+" resetAt")
		if err != nil {
			return nil, err
		}
		resetAt = &canonical
	}
	var remainingSeconds int
	utilizationValue := *utilization
	if resetAt != nil {
		ms, err := rfc3339InstantMilliseconds(*resetAt)
		if err != nil {
			return nil, &ValidationError{Message: "account_usage_snapshots " + window + " resetAt 必须是带 Z 或数值 offset 的 RFC3339 时间"}
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
	if minutes, ok := numberFromSnapshot(snapshot["codex_"+window+"_window_minutes"]); ok {
		out.WindowMinutes = minutes
	}
	return out, nil
}

// numberFromSnapshot mirrors numberFromUnknown: finite JSON numbers only.
func numberFromSnapshot(value any) (*float64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, false
	}
	return &number, true
}

// optionalSnapshotString mirrors optionalString.
func optionalSnapshotString(value any) string {
	text, _ := value.(string)
	return text
}

// requiredRFC3339Instant mirrors requiredRfc3339Instant: the value must parse
// as an RFC3339 instant; the original text passes through.
func requiredRFC3339Instant(value, label string) (string, error) {
	if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err != nil {
		return "", &ValidationError{Message: label + " 必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	return value, nil
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds.
func rfc3339InstantMilliseconds(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.UnixMilli(), nil
}
