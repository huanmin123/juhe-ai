// Package proberepo 为 jobs 探针族（account-quality-refresh 的失败前置确认、
// account-api-key-cooldown-retest、normal-route-speed-first-recovery-probe）
// 提供仓储侧移植：
//   - AccountReader（等价 Node find_account_for_test /
//     find_openai_account_for_group 的被消费字段，含 effectiveAvailability
//     派生链 domain/account-effective-availability.ts 的 DB 分支）；
//   - PrecheckMutation（mark_account_precheck_temporary_unavailable 的
//     fence 校验顺序与 CAS 写入）；
//   - CooldownCandidateSource / CooldownMutation
//     （account_api_key_runtime_states 的到期候选 claim 与 record/defer CAS）；
//   - accountprobe.CandidateSource（探针视图组装）；
//   - Redis 降级运行态（normal-route-latency-degradation.service.ts 的
//     state/claim/probe-index 契约，键与 Lua 逐字节对照）。
//
// 不迁移的分支见包内 EffectiveAvailabilityLimitations 注释与迁移报告。
package proberepo

import (
	"database/sql"
	"strings"
	"time"
)

// Config 组装 Store。
type Config struct {
	// DB 是业务库句柄（SQLite 单库或 PostgreSQL juhe_business schema）。
	DB *sql.DB
	// Postgres 为 true 时表名限定 juhe_business schema 并使用原生时间绑定。
	Postgres bool
	// Secret 为凭据封套与 Key 指纹 HMAC 密钥（Node runtimeConfig.secret）。
	Secret string
	// Now 注入时间源；为空取 UTC time.Now。
	Now func() time.Time
}

// Store 承载探针族全部业务库读写。
type Store struct {
	db       *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
}

// NewStore 构建 Store；输入校验失败返回错误。
func NewStore(config Config) (*Store, error) {
	if config.DB == nil {
		return nil, errInvalid("proberepo 缺少业务库句柄")
	}
	if strings.TrimSpace(config.Secret) == "" {
		return nil, errInvalid("proberepo 缺少 JUHE_AI_SECRET（凭据解密与 Key 指纹不可用）")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: config.DB, postgres: config.Postgres, secret: config.Secret, now: now}, nil
}

func (s *Store) nowMS() int64 { return s.now().UnixMilli() }

// table 限定业务表名（与组合根 businessDB.table 一致）。
func (s *Store) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

// rfc3339Milli 与 Node toISOString() 输出一致（UTC + 毫秒 + Z）。
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// timeParam 绑定时间值（PG 原生 timestamptz；SQLite RFC3339Nano 文本）。
func (s *Store) timeParam(t time.Time) any {
	if s.postgres {
		return t
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// instantParam 绑定 RFC3339 文本时间（PG 解析为原生 timestamptz；
// SQLite 保留文本，与 Node nowIso 比较语义一致）。
func (s *Store) instantParam(value string) any {
	if s.postgres {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return value
}

type invalidError string

func (e invalidError) Error() string { return string(e) }

func errInvalid(message string) error { return invalidError(message) }
