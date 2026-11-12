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
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// AccountStateVersionBumper 是账户运行态版本栅栏写入端口（B3b/B4）：Key 运行
// 态 PG 写成功后按受影响账户递增共享序号，gateway runtime 缓存下一次读重载。
type AccountStateVersionBumper interface {
	BumpAccountState(ctx context.Context, accountID string) error
}

// Config 组装 Store。
type Config struct {
	// DB 是业务库句柄（PostgreSQL juhe_business schema）。
	DB *sql.DB
	// Secret 为凭据封套与 Key 指纹 HMAC 密钥（Node runtimeConfig.secret）。
	Secret string
	// Now 注入时间源；为空取 UTC time.Now。
	Now func() time.Time
	// StateVersion 是 Key 运行态/账户状态版本写入端口；nil 保持既有窗口。
	StateVersion AccountStateVersionBumper
}

// Store 承载探针族全部业务库读写。
type Store struct {
	db           *sql.DB
	secret       string
	now          func() time.Time
	stateVersion AccountStateVersionBumper
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
	return &Store{db: config.DB, secret: config.Secret, now: now, stateVersion: config.StateVersion}, nil
}

func (s *Store) nowMS() int64 { return s.now().UnixMilli() }

// table 限定业务表名（PostgreSQL 唯一形态，与组合根 businessDB.table 一致）。
func (s *Store) table(name string) string {
	return "juhe_business." + name
}

// bind 把 `?` 占位符改写为 PG 的 $n（ISSUE-005 同类）：本包查询按 Node
// 语义书写 `?`，直达 pgx 触发 42601/参数错位（2026-09-25 测试环境「测试账号
// 连接」1:1 数据复现定位）。（原非 postgres 透传臂随 sqlite 方言退役删除。）
func (s *Store) bind(query string) string {
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

// timeParam 把时间值绑定为 RFC3339 文本（UTC + 毫秒 + Z，本包 nowISO 口径）。
// 生产 juhe_business DDL 的业务表时间列为 text（maintenance
// pg_schema_business_tables），存量行是 RFC3339 文本；绑定原生 time.Time 会让
// pgx 对 text 参数编码出 Go time.String() 形态，字典序比较与回读解析双双失真
// （W27①/②/③，2026-10-25 修复）。涉及时间比较时统一在 SQL 侧对两侧
// `::timestamptz` 转型（同包 RecordKeyFailure genericGuard 与 manualtestrepo
// startedAtFence 先例），转型对存量文本的格式差异稳健。
func (s *Store) timeParam(t time.Time) any {
	return t.UTC().Format(rfc3339Milli)
}

// instantParam 把规范 RFC3339 文本绑定为 time.Time。Revision 26 起运行态
// 与 accounts 时间列均为 timestamptz，参数类型必须由客户端确定为 timestamptz，
// 否则 COALESCE/CASE 的赋值上下文会退化为 text（SQLSTATE 42804）；解析失败
// 保留原文交给 SQL/调用方报错。
func (s *Store) instantParam(value string) any {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed
}

// instantParamAny 处理 nil/string 混合的 recovery 参数。
func (s *Store) instantParamAny(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return s.instantParam(typed)
	case time.Time:
		return typed
	default:
		return value
	}
}

type invalidError string

func (e invalidError) Error() string { return string(e) }

func errInvalid(message string) error { return invalidError(message) }
