// Retention ported from modules/background/data-retention-cleanup.service.ts
// (the publicApiLogs stage) plus the settingNumber validation: the cutoff is
// now minus publicApiLogRetentionDays, deletion is strictly created_at <
// cutoff (records equal to the cutoff survive), candidates run oldest-first in
// stable (created_at, id) order, batches are 1000 rows, a run performs at most
// 20 batches and stops at the first non-full batch.
package publicapilogs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Retention limits mirror DATA_RETENTION_CLEANUP_* and the Node constants.
const (
	RetentionBatchSize        = 1000
	RetentionMaxBatchesPerRun = 20
	RetentionBatchPause       = 25 * time.Millisecond
	retentionDayMillis        = 24 * 60 * 60 * 1000

	// SettingPublicApiLogRetentionDays is the settings key.
	SettingPublicApiLogRetentionDays = "publicApiLogRetentionDays"

	retentionDaysMin = 1
	retentionDaysMax = 365
)

// RetentionDaysSource mirrors reading getSettings()[key]; the Node worker
// treats a missing or non-integer setting as an error (fail-closed, no
// silent default).
type RetentionDaysSource func(ctx context.Context) (map[string]any, error)

// Sleeper pauses between full batches; tests inject a no-op.
type Sleeper func(ctx context.Context) error

// Retention runs the public API log cleanup.
type Retention struct {
	store    CleanupStore
	settings RetentionDaysSource
	now      func() time.Time
	sleep    Sleeper
}

// CleanupStore is the retention port; *Store implements it.
type CleanupStore interface {
	CleanupBefore(ctx context.Context, cutoffCreatedAt string, limit int) (int, error)
}

// NewRetention builds the runner; now/sleep fall back to real time.
func NewRetention(store CleanupStore, settings RetentionDaysSource, now func() time.Time, sleep Sleeper) *Retention {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context) error { return nil }
	}
	return &Retention{store: store, settings: settings, now: now, sleep: sleep}
}

// RunOnce mirrors cleanupInBatches over the public API log repository:
// validate the setting, compute the cutoff, then loop up to
// RetentionMaxBatchesPerRun full batches (a non-full batch ends the run).
// Returns the total deleted row count.
func (r *Retention) RunOnce(ctx context.Context) (int, error) {
	ctx = ensureCtx(ctx)
	settings, err := r.settings(ctx)
	if err != nil {
		return 0, err
	}
	days, err := SettingNumber(settings, SettingPublicApiLogRetentionDays, retentionDaysMin, retentionDaysMax)
	if err != nil {
		return 0, err
	}
	nowMillis := r.now().UnixMilli()
	cutoff := isoMillis(time.UnixMilli(nowMillis - int64(days)*retentionDayMillis))
	total := 0
	for batch := 0; batch < RetentionMaxBatchesPerRun; batch++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := r.store.CleanupBefore(ctx, cutoff, RetentionBatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < RetentionBatchSize {
			return total, nil
		}
		if err := r.sleep(ctx); err != nil {
			return total, err
		}
	}
	return total, nil
}

// SettingNumber mirrors the Node settingNumber helper (fail-closed integer
// validation with the exact error messages). Integer-valued float64 inputs
// are accepted, matching Number.isInteger on a JS number.
func SettingNumber(settings map[string]any, key string, min, max int) (int, error) {
	value, ok := settings[key]
	if !ok {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	parsed, err := settingIntegerValue(value)
	if err != nil {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("系统设置 %s 必须在 %d 到 %d 之间", key, min, max)
	}
	return parsed, nil
}

func settingIntegerValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, errNotInteger
		}
		return int(typed), nil
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, errNotInteger
		}
		return int(parsed), nil
	}
	return 0, errNotInteger
}

type notIntegerError struct{}

var errNotInteger = notIntegerError{}

func (notIntegerError) Error() string { return "value is not an integer" }
