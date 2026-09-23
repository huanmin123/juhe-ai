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

// OpenAI Codex OAuth usage snapshot read projection (BUG-0175 D-206), the
// port of backend/src/storage/oauth-usage-loaders.ts +
// account-summary.repository.ts:1452,1575: the account summary hydrates the
// oauthUsage field for gpt-provider oauth accounts from the stats-side
// account_usage_snapshots rows (kind='openai_codex'). The ListItem-shaped
// hydration loop stays in the accounts facade (list.go); this package exposes
// the snapshot load keyed by account id.

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

// LoadOpenAICodexUsageSnapshots mirrors loadOpenAICodexUsageSnapshotsByAccountIds:
// the kind='openai_codex' snapshot rows keyed by account id (chunked IN).
func (s *Service) LoadOpenAICodexUsageSnapshots(ctx context.Context, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
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
			WHERE kind = 'openai_codex' AND account_id IN (`+accountscore.Placeholders(len(chunk))+`)`), accountscore.AnySlice(chunk)...)
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
			snapshot, err := OAuthUsageSnapshotFromRow(source.String, snapshotJSON, refresh.String,
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

// OAuthUsageSnapshotFromRow mirrors oauthUsageSnapshotsFromRows: an
// unparseable snapshot_json skips the row; timestamps render as RFC3339
// instants (Node optionalInstant / requiredRfc3339Instant).
func OAuthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	updatedText, err := RequiredRFC3339Instant(updatedAt, "account_usage_snapshots.updated_at")
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
			return nil, err
		}
		*pair.dest = &text
	}
	if lastErrorMessage != "" {
		text := lastErrorMessage
		out.LastErrorMessage = &text
	}
	fiveHour, err := OAuthUsageWindowFromSnapshot(snapshot, "5h", updatedText)
	if err != nil {
		return nil, err
	}
	out.FiveHour = fiveHour
	sevenDay, err := OAuthUsageWindowFromSnapshot(snapshot, "7d", updatedText)
	if err != nil {
		return nil, err
	}
	out.SevenDay = sevenDay
	return out, nil
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
