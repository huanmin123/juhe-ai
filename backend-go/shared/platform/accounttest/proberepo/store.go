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
	"strconv"
	"strings"
	"time"
)

// Config 组装 Store。
type Config struct {
	// DB 是业务库句柄（PostgreSQL juhe_business schema）。
	DB *sql.DB
	// Postgres 为 true 时表名限定 juhe_business schema 并使用原生时间绑定。
	// 2026-10-25 sqlite 方言退役：生产组合根（jobs worker_probe_jobs）恒传
	// true；gateway compose_account_test_local 的方言旗标随 B2 收口，非
	// postgres 臂届时一并物理删除。
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

// bind 把 `?` 占位符改写为 PG 的 $n（ISSUE-005 同类）：本包查询按 Node
// 语义书写 `?`，直达 pgx 触发 42601/参数错位（2026-09-25 测试环境「测试账号
// 连接」1:1 数据复现定位）；非 postgres 臂原样透传（历史 sqlite 方言残臂，
// 随组合根方言旗标收口清理）。
func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// rfc3339Milli 与 Node toISOString() 输出一致（UTC + 毫秒 + Z）。
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// timeParam 绑定时间值。事实基线（W27 根因登记，2026-10-25 注释纠偏）：
// 生产 juhe_business DDL 的时间列为 text（maintenance pg_schema_business_tables），
// 并非本注释早先声称的「PG 原生 timestamptz」；postgres 臂返回原生 time.Time，
// pgx 对 text 列回退文本编码（Go time.String() 形态），与存量 RFC3339 行的
// 字典序比较永不命中——ListDueForProbe 候选扫描恒空（W27①）与授权过期臂
// 放行（W27③）皆源于此，行为修正待用户裁决，本批不改 PG 路径行为。
// 非 postgres 臂返回 RFC3339Nano 文本（历史 sqlite 方言残臂）。
func (s *Store) timeParam(t time.Time) any {
	if s.postgres {
		return t
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// instantParam 绑定 RFC3339 文本时间：postgres 臂解析为原生 time.Time 后
// 绑定（同受上述 text 列事实约束，解析失败原样透传文本）；非 postgres 臂
// 保留文本（历史 sqlite 方言残臂，与 Node nowIso 比较语义一致）。
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
