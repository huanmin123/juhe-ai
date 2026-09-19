package gatewayupstream

import (
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
