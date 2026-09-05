// Package jobssettings implements the background-jobs system_settings read
// model, ported from modules/background/background-jobs.ts
// (settingsNumber:855-871 + sqliteBackgroundJobSettingValue /
// postgresBackgroundJobSettingValue) on top of storage/settings.repository.ts
// (system_settings, system_account_id='sys_admin', JSON value_json) and
// storage/schema-defaults.ts DEFAULT_SYSTEM_SETTINGS.
//
// Semantics kept from Node:
//   - a stored value must decode to a finite integer
//     ("系统设置 %s 必须是整数") and stay inside the caller bounds
//     ("系统设置 %s 必须在 %d 到 %d 之间") — the job task fails otherwise;
//   - a missing row falls back to the DEFAULT_SYSTEM_SETTINGS value
//     (applyCompatibleSystemSettingDefaults guarantees the key exists in
//     Node); the default travels through the same integer/bounds validation;
//   - SQLite: a missing system_settings table degrades to the defaults with a
//     one-time warn (sqliteBackgroundJobSettingValue
//     isMissingSystemSettingsTableError); every other read error propagates
//     and fails the job;
//   - PostgreSQL: a failed settings read degrades to the defaults with a warn
//     (postgresBackgroundJobSettingValue snapshot-refresh failure) instead of
//     failing the job.
//
// Deviation (deliberate): the values are cached per key for 60s — the Node
// PostgreSQL path keeps a 60s backgroundJobSettingsSnapshotTtlMs snapshot and
// the SQLite path an in-process settings cache; a per-key TTL window keeps the
// same staleness bound without wiring the cross-process invalidation the
// settings writer owns.
package jobssettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// SettingsSnapshotTTL mirrors backgroundJobSettingsSnapshotTtlMs.
const SettingsSnapshotTTL = 60 * time.Second

// SystemSettingsAccountID mirrors SYSTEM_SETTINGS_ACCOUNT_ID.
const SystemSettingsAccountID = "sys_admin"

// Mode selects the table qualifier and the failure semantics.
type Mode int

const (
	// SQLite reads the business database (system_settings without schema).
	SQLite Mode = iota
	// Postgres reads juhe_business.system_settings.
	Postgres
)

// WarnFunc receives the Node logger.warn payloads (event fields + message).
type WarnFunc func(event string, fields map[string]any, message string)

// Options carries the read-model collaborators.
type Options struct {
	DB   *sql.DB
	Mode Mode
	// Warn receives the default-fallback warnings; nil drops them.
	Warn WarnFunc
	// Now overrides the cache clock; nil falls back to time.Now.
	Now func() time.Time
	// SnapshotTTL overrides the 60s default (tests).
	SnapshotTTL time.Duration
}

// Source is the read model one background worker keeps per database handle.
type Source struct {
	db          *sql.DB
	mode        Mode
	warn        WarnFunc
	now         func() time.Time
	ttl         time.Duration
	missingOnce sync.Once

	mutex    sync.Mutex
	loadedAt map[string]time.Time
	values   map[string]any
}

// NewSource builds the read model; the DB handle must outlive it.
func NewSource(options Options) *Source {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	ttl := options.SnapshotTTL
	if ttl <= 0 {
		ttl = SettingsSnapshotTTL
	}
	return &Source{
		db:       options.DB,
		mode:     options.Mode,
		warn:     options.Warn,
		now:      now,
		ttl:      ttl,
		loadedAt: map[string]time.Time{},
		values:   map[string]any{},
	}
}

// Number mirrors settingsNumber(key, min, max): integer values inside the
// bounds; missing rows fall back to DEFAULT_SYSTEM_SETTINGS; read failures
// follow the per-driver semantics documented on the package.
func (s *Source) Number(ctx context.Context, key string, min, max int) (int, error) {
	value, err := s.settingValue(ctx, key)
	if err != nil {
		return 0, err
	}
	number, ok := integerSettingValue(value)
	if !ok {
		return 0, fmt.Errorf("系统设置 %s 必须是整数", key)
	}
	if number < min || number > max {
		return 0, fmt.Errorf("系统设置 %s 必须在 %d 到 %d 之间", key, min, max)
	}
	return number, nil
}

// settingValue resolves one key through the 60s window, the stored row and
// the DEFAULT_SYSTEM_SETTINGS fallback.
func (s *Source) settingValue(ctx context.Context, key string) (any, error) {
	now := s.now()
	s.mutex.Lock()
	if loadedAt, ok := s.loadedAt[key]; ok && now.Sub(loadedAt) < s.ttl {
		value := s.values[key]
		s.mutex.Unlock()
		return value, nil
	}
	s.mutex.Unlock()

	value, err := s.readValue(ctx, key)
	if err != nil {
		return nil, err
	}
	s.mutex.Lock()
	s.loadedAt[key] = now
	s.values[key] = value
	s.mutex.Unlock()
	return value, nil
}

func (s *Source) readValue(ctx context.Context, key string) (any, error) {
	query := `SELECT value_json FROM system_settings WHERE system_account_id = ? AND key = ? LIMIT 1`
	if s.mode == Postgres {
		query = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $2 AND key = $1 LIMIT 1`
	}
	var rawValue sql.NullString
	readErr := s.db.QueryRowContext(ctx, query, SystemSettingsAccountID, key).Scan(&rawValue)
	if readErr == nil || readErr == sql.ErrNoRows {
		// A missing row falls back to DEFAULT_SYSTEM_SETTINGS exactly like
		// Node's applyCompatibleSystemSettingDefaults.
		if readErr == nil && rawValue.Valid && strings.TrimSpace(rawValue.String) != "" {
			var decoded any
			if err := jsonUnmarshal([]byte(rawValue.String), &decoded); err != nil {
				return nil, fmt.Errorf("系统设置 %s 必须是整数", key)
			}
			return decoded, nil
		}
		return DefaultSystemSettings[key], nil
	}
	if isMissingSystemSettingsTableError(readErr) {
		// sqliteBackgroundJobSettingValue: the settings table may not exist
		// yet at job startup — one warn, then the defaults.
		s.missingOnce.Do(func() {
			s.warnDefault("background_job_settings_table_missing_default",
				"后台任务启动时系统设置表尚未初始化，将临时使用默认设置", readErr)
		})
		return DefaultSystemSettings[key], nil
	}
	if s.mode == Postgres {
		// postgresBackgroundJobSettingValue: a failed snapshot refresh warns
		// and keeps serving the defaults.
		s.warnDefault("background_job_settings_snapshot_refresh_failed",
			"后台任务系统设置快照刷新失败，将临时使用默认设置", readErr)
		return DefaultSystemSettings[key], nil
	}
	return nil, readErr
}

func (s *Source) warnDefault(event, message string, err error) {
	if s.warn == nil {
		return
	}
	s.warn(event, map[string]any{"error": err.Error()}, message)
}

// isMissingSystemSettingsTableError mirrors isMissingSystemSettingsTableError.
func isMissingSystemSettingsTableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: system_settings")
}

// DefaultNumber returns the DEFAULT_SYSTEM_SETTINGS numeric default clamped
// into [min, max]; ok=false when the key has no numeric default. Callers
// without an error channel (accountquality.SettingsNumber) use it as the
// validation-failure fallback.
func DefaultNumber(key string, min, max int) (int, bool) {
	value, ok := integerSettingValue(DefaultSystemSettings[key])
	if !ok {
		return 0, false
	}
	if value < min {
		value = min
	}
	if value > max {
		value = max
	}
	return value, true
}

// integerSettingValue mirrors the settingsNumber integer guard: finite
// integral numbers only (JSON floats like 2000.0 are integers, 1.5 is not).
func integerSettingValue(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, false
	}
	return int(number), true
}

// jsonUnmarshal decodes the value_json column (Node JSON.parse).
func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
