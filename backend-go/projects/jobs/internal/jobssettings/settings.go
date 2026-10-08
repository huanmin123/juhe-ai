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
	"regexp"
	"strings"
	"sync"
	"time"
)

// SettingsSnapshotTTL mirrors backgroundJobSettingsSnapshotTtlMs.
const SettingsSnapshotTTL = 60 * time.Second

// SystemSettingsAccountID mirrors SYSTEM_SETTINGS_ACCOUNT_ID.
const SystemSettingsAccountID = "sys_admin"

// systemSettingSelectSQLite / systemSettingSelectPostgres 是单键设置读取的
// 唯二查询串（readValue 与 readValueStrict 共用，禁止在调用点内联 SQL）。
// 实参顺序固定为 (system_account_id, key)：SQLite 的 ? 按位置绑定，PG 的
// $1/$2 必须与之对应——$1 = system_account_id、$2 = key。
//
// BUG-0297：PG 分支曾写反为 system_account_id = $2 AND key = $1，与实参
// (SystemSettingsAccountID, key) 错配，PG 下恒 ErrNoRows 回退默认值，jobs
// 进程全部经这两条路径的设置读取在生产从未读到真实值。SQLite 分支不受影响，
// 测试因此全绿未暴露。占位符映射由 TestPostgresSettingSelectPlaceholderOrder
// 锁定。
const (
	systemSettingSelectSQLite   = `SELECT value_json FROM system_settings WHERE system_account_id = ? AND key = ? LIMIT 1`
	systemSettingSelectPostgres = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $1 AND key = $2 LIMIT 1`
)

// systemSettingSelectQuery 按模式返回单键设置查询串。
func systemSettingSelectQuery(mode Mode) string {
	if mode == Postgres {
		return systemSettingSelectPostgres
	}
	return systemSettingSelectSQLite
}

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

// upstreamClientVersionFamilySet 是 upstreamClientVersionOverrides 允许的
// 家族键（与 gateway settings 校验、upstreamidentity 覆盖 API 一致）。
var upstreamClientVersionFamilySet = map[string]bool{
	"codex": true, "claudeCode": true, "geminiCLI": true, "zcode": true, "grokCLI": true,
}

var upstreamClientVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// UpstreamClientVersionOverrides 读取 system_settings 的
// upstreamClientVersionOverrides 键并做防御式过滤：只保留五家族键且值匹配
// semver 三段的项（网关侧保存时已严格校验，这里兜底异常数据）；空对象、
// 缺行（回退默认 {}）与全非法都返回空 map（= 全部使用内置版本）。
//
// 与 Number 不同，本方法走 readValueStrict 而非 settingValue：DB 读失败
// 必须返回 error（调用方保持既有覆盖不动），不得降级成默认 {}——否则 PG
// 瞬断会把已配置的应急覆盖静默清空。不进 per-key TTL 缓存（覆盖项读取
// 频率仅每 60s 一次刷新，无需缓存）。
func (s *Source) UpstreamClientVersionOverrides(ctx context.Context) (map[string]string, error) {
	value, err := s.readValueStrict(ctx, "upstreamClientVersionOverrides")
	if err != nil {
		return nil, err
	}
	decoded, ok := value.(map[string]any)
	if !ok {
		return map[string]string{}, nil
	}
	overrides := make(map[string]string, len(decoded))
	for family, raw := range decoded {
		if !upstreamClientVersionFamilySet[family] {
			continue
		}
		version, ok := raw.(string)
		if !ok || !upstreamClientVersionPattern.MatchString(version) {
			continue
		}
		overrides[family] = version
	}
	return overrides, nil
}

// UpstreamClientVersionAutoOverrides 读取 system_settings 的
// upstreamClientVersionAutoOverrides 键（自动层，跟版任务独占写入、gateway
// 周期消费）并做同款防御式过滤；空对象、缺行（回退默认 {}）与全非法都返回
// 空 map（= 自动层为空，生效值回退手动层/内置）。
//
// 与 UpstreamClientVersionOverrides 同语义：走 readValueStrict 而非
// settingValue，DB 读失败必须返回 error（调用方保持既有自动层不动），不得
// 降级成默认 {}——否则 PG 瞬断会把已跟版成功的自动值静默清空；同样不进
// per-key TTL 缓存。
func (s *Source) UpstreamClientVersionAutoOverrides(ctx context.Context) (map[string]string, error) {
	value, err := s.readValueStrict(ctx, "upstreamClientVersionAutoOverrides")
	if err != nil {
		return nil, err
	}
	decoded, ok := value.(map[string]any)
	if !ok {
		return map[string]string{}, nil
	}
	overrides := make(map[string]string, len(decoded))
	for family, raw := range decoded {
		if !upstreamClientVersionFamilySet[family] {
			continue
		}
		version, ok := raw.(string)
		if !ok || !upstreamClientVersionPattern.MatchString(version) {
			continue
		}
		overrides[family] = version
	}
	return overrides, nil
}

// readValueStrict 直读一行设置：缺行回退 DEFAULT_SYSTEM_SETTINGS，其余
// 读错误原样返回（不走 readValue 的 PG 降级/SQLite 缺表降级路径）。
func (s *Source) readValueStrict(ctx context.Context, key string) (any, error) {
	query := systemSettingSelectQuery(s.mode)
	var rawValue sql.NullString
	readErr := s.db.QueryRowContext(ctx, query, SystemSettingsAccountID, key).Scan(&rawValue)
	if readErr == sql.ErrNoRows {
		return DefaultSystemSettings[key], nil
	}
	if readErr != nil {
		return nil, readErr
	}
	if !rawValue.Valid || strings.TrimSpace(rawValue.String) == "" {
		return DefaultSystemSettings[key], nil
	}
	var decoded any
	if err := jsonUnmarshal([]byte(rawValue.String), &decoded); err != nil {
		return nil, fmt.Errorf("系统设置 %s 不是合法 JSON", key)
	}
	return decoded, nil
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
	query := systemSettingSelectQuery(s.mode)
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
