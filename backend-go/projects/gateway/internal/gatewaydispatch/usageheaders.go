package gatewaydispatch

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
	PrimaryUsedPercent         *float64
	PrimaryResetAfterSeconds   *int64
	PrimaryWindowMinutes       *int64
	SecondaryUsedPercent       *float64
	SecondaryResetAfterSeconds *int64
	SecondaryWindowMinutes     *int64
	PrimaryOverSecondaryPercent *float64
	UpdatedAt                  string
}

// NormalizedCodexLimits mirrors NormalizedCodexLimits.
type NormalizedCodexLimits struct {
	Used5hPercent    *float64
	Reset5hSeconds   *int64
	Window5hMinutes  *int64
	Used7dPercent    *float64
	Reset7dSeconds   *int64
	Window7dMinutes  *int64
}

// codexWindowCandidate mirrors the local candidate type.
type codexWindowCandidate struct {
	usedPercent      *float64
	resetAfterSeconds *int64
	windowMinutes    *int64
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
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
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
	job := buildOpenAICodexUsageRecordMaintenanceJob(accountID, headers, source)
	if job == nil {
		return false
	}
	queue.EnqueueRecordMaintenanceJob(*job)
	return true
}

func buildOpenAICodexUsageRecordMaintenanceJob(accountID string, headers http.Header, source string) *RecordMaintenanceJob {
	snapshot := ParseOpenAICodexUsageHeaders(headers)
	if snapshot == nil {
		return nil
	}
	fallbackNow := time.Now()
	payload := buildOpenAICodexUsageSnapshotPayload(*snapshot, fallbackNow, source)
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

func buildOpenAICodexUsageSnapshotPayload(snapshot OpenAICodexUsageSnapshot, fallbackNow time.Time, source string) map[string]any {
	baseTime := parseIsoDate(snapshot.UpdatedAt)
	if baseTime == nil {
		baseTime = &fallbackNow
	}
	payload := map[string]any{
		"codex_usage_updated_at": baseTime.UTC().Format(time.RFC3339),
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

	if reset5hAt := resetAtFromSeconds(*baseTime, normalized.Reset5hSeconds); reset5hAt != "" {
		payload["codex_5h_reset_at"] = reset5hAt
	}
	if reset7dAt := resetAtFromSeconds(*baseTime, normalized.Reset7dSeconds); reset7dAt != "" {
		payload["codex_7d_reset_at"] = reset7dAt
	}

	return payload
}

func normalizeOpenAICodexUsageSnapshot(snapshot OpenAICodexUsageSnapshot) *NormalizedCodexLimits {
	normalized := &NormalizedCodexLimits{}
	primary := codexWindowCandidate{
		usedPercent:       snapshot.PrimaryUsedPercent,
		resetAfterSeconds: snapshot.PrimaryResetAfterSeconds,
		windowMinutes:     snapshot.PrimaryWindowMinutes,
	}
	secondary := codexWindowCandidate{
		usedPercent:       snapshot.SecondaryUsedPercent,
		resetAfterSeconds: snapshot.SecondaryResetAfterSeconds,
		windowMinutes:     snapshot.SecondaryWindowMinutes,
	}
	primaryKey := windowKeyFromMinutes(primary.windowMinutes)
	secondaryKey := windowKeyFromMinutes(secondary.windowMinutes)

	if primaryKey != "" {
		assignNormalizedWindow(normalized, primaryKey, primary)
	}
	if secondaryKey != "" {
		assignNormalizedWindow(normalized, secondaryKey, secondary)
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

func assignNormalizedWindow(normalized *NormalizedCodexLimits, key string, candidate codexWindowCandidate) {
	if candidate.windowMinutes != nil && *candidate.windowMinutes <= 0 {
		return
	}
	if candidate.usedPercent == nil && candidate.resetAfterSeconds == nil && candidate.windowMinutes == nil {
		return
	}
	if key == "5h" {
		normalized.Used5hPercent = candidate.usedPercent
		normalized.Reset5hSeconds = candidate.resetAfterSeconds
		normalized.Window5hMinutes = candidate.windowMinutes
		return
	}
	normalized.Used7dPercent = candidate.usedPercent
	normalized.Reset7dSeconds = candidate.resetAfterSeconds
	normalized.Window7dMinutes = candidate.windowMinutes
}

func numberHeader(headers http.Header, key string) *float64 {
	value := headerValueOf(headers, key)
	if value == "" {
		return nil
	}
	return numberValueOf(value)
}

func numberValueOf(value string) *float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func resetAtFromSeconds(baseTime time.Time, seconds *int64) string {
	if seconds == nil {
		return ""
	}
	reset := baseTime.Add(time.Duration(maxInt64(0, *seconds)) * time.Second)
	return reset.UTC().Format(time.RFC3339)
}

func parseIsoDate(value string) *time.Time {
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
