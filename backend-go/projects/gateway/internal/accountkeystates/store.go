// Package accountkeystates 移植 Node storage/account-api-key-runtime-state.repository.ts
// 的 account_api_key_runtime_states 域（gateway 侧读+写+池可用性投影）：
//
//   - 运行态选择状态读面（loadAccountApiKeyRuntimeStatesByAccountIdsAsync /
//     loadAccountApiKeyRuntimeStatesForAccountInClient）；
//   - 探针候选 claim + record success/failure + defer probe（与 jobs 侧
//     backend-go-jobs/internal/proberepo 同表同键、SQL 同源：jobs 承担后台
//     claim/探针写，本包承担网关侧被动失败/成功登记与重校验触发；两条写路径
//     共享同一组 CAS 围栏，读写互通不冲突）；
//   - 池 summaries + allUnavailable 判定
//     （loadAccountApiKeyRuntimeSummariesByAccountIdsAsync）；
//   - 池重校验触发（revalidateAccountApiKeyRuntimePoolAsync，runtime-reset 端口的
//     RevalidateAccountAPIKeyRuntimePool 语义）。
//
// SQL 逐字段对照归档 Node（PostgreSQL 取 *Async 变体、SQLite 取同步变体）。
// 已知偏差（与迁移报告同步披露）：
//  1. recordFailure 的 PostgreSQL 分支不再把 next_probe_at 的背期调度放进 SQL
//     （Node 用 statement_timestamp()+random() 原子计算），改为注入时钟在应用侧
//     计算后绑定参数——jobs proberepo 在同一张表上做了同一取舍（同源对齐），
//     避免两个 Go 服务对同一张表产生两种调度行为。
//  2. config_revision 围栏的 EXISTS 子查询不带 Node PG 分支的 FOR UPDATE
//     （Go 写路径运行在 autocommit，行锁无事务边界可持有；proberepo 同款取舍）。
package accountkeystates

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 探针与候选扫描常量（Node account-api-key-runtime-state.repository.ts 顶部）。
const (
	initialProbeBackoffSeconds = 3
	maxProbeBackoffSeconds     = 60 * 60
	// probeClaimLeaseSeconds 对齐 Node probeClaimLeaseSeconds = 600。
	probeClaimLeaseSeconds = 10 * time.Minute
	// probeCandidateScanLimit 对齐 Node
	// runtimeConfig.background.accountApiKeyProbeCandidateScanLimit（默认 10_000）。
	probeCandidateScanLimit = 10_000
	// statsDirtyReason 对齐 Node markRuntimeStateChanged 的 reason 常量。
	statsDirtyReason = "account_api_key_runtime"
	// probeParentStatusesSQL：冷却中的父账户仍允许探针，disabled/error/
	// pending_test/quality_isolated 等硬不可用状态明确排除（Node 原文注释）。
	probeParentStatusesSQL = "'active', 'rate_limited', 'temporary_unavailable'"
)

// 配额恢复错误码（Node modules/gateway/policy/api-key-quota-recovery.ts，
// 与 jobs accountquality 常量同值）。
const (
	QuotaRecoveryGenericErrorCode  = "api_key_quota_insufficient"
	QuotaRecoveryExplicitErrorCode = "api_key_quota_insufficient_reset"
)

// probeCandidateStatuses 等价 accountApiKeyRuntimeProbeCandidateStatuses。
var probeCandidateStatuses = []string{"unverified", "temporary_unavailable", "rate_limited"}

func isProbeCandidateStatus(status string) bool {
	for _, candidate := range probeCandidateStatuses {
		if candidate == status {
			return true
		}
	}
	return false
}

// Config 组装 Store。
type Config struct {
	// DB 是业务库句柄（SQLite 单库或 PostgreSQL juhe_business schema 共享池）。
	DB *sql.DB
	// Postgres 为 true 时表名限定 juhe_business schema、占位符绑定为 $n、
	// 时间参数按原生 timestamptz 绑定。
	Postgres bool
	// Secret 是凭据封套与 Key 指纹 HMAC 密钥（Node runtimeConfig.secret）。
	Secret string
	// Now 注入时间源；为空取 UTC time.Now。
	Now func() time.Time
	// InvalidateRuntimeCache 等价 notifyGatewayRuntimeCacheInvalidation 的进程内
	// 投影：组合根把它接到 inval.Bus 的 TopicGatewayRuntime；为空时跳过通知
	// （group_account_stats_dirty 的 SQL 落库不受影响）。
	InvalidateRuntimeCache func(reason string)
}

// Store 承载 account_api_key_runtime_states 域全部业务库读写。
type Store struct {
	db       *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
	inval    func(reason string)
}

// NewStore 构建 Store；输入校验失败返回错误。
func NewStore(config Config) (*Store, error) {
	if config.DB == nil {
		return nil, errInvalid("accountkeystates 缺少业务库句柄")
	}
	if strings.TrimSpace(config.Secret) == "" {
		return nil, errInvalid("accountkeystates 缺少 JUHE_AI_SECRET（凭据解密与 Key 指纹不可用）")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: config.DB, postgres: config.Postgres, secret: config.Secret, now: now, inval: config.InvalidateRuntimeCache}, nil
}

// rfc3339Milli 与 Node toISOString() 输出一致（UTC + 毫秒 + Z）；与 jobs
// proberepo 的同名常量同源。
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

func (s *Store) nowISO() string { return s.now().UTC().Format(rfc3339Milli) }

// statesTable 等价 accountApiKeyRuntimeStatesTable(client)。
func (s *Store) statesTable() string { return s.businessTable("account_api_key_runtime_states") }

// businessTable 等价 accountApiKeyRuntimeBusinessTable(client, tableName)：
// PostgreSQL 限定 juhe_business schema，SQLite 使用裸表名。
func (s *Store) businessTable(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

// bind 把 ? 占位符改写为 PostgreSQL 的 $n 序号（SQLite 原样返回）。
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

// timeParam 绑定生成时间（PG 原生 timestamptz；SQLite RFC3339 文本）。
func (s *Store) timeParam(t time.Time) any {
	if s.postgres {
		return t
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// instantParam 绑定 RFC3339 文本时间（PG 解析为原生 timestamptz 比较；
// SQLite 保留文本，与 Node nowIso 字符串比较语义一致）。
func (s *Store) instantParam(value string) any {
	if s.postgres {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return value
}

// argOrNull 把空字符串绑定为 SQL NULL（Node 传 null 的字段）。
func argOrNull(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type invalidError string

func (e invalidError) Error() string { return string(e) }

func errInvalid(message string) error { return invalidError(message) }

// chunkValues 等价 Node query-utils chunkValues：按 size 切片。
func chunkValues(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

// placeholders 生成 n 个 ? 占位符（等价 sqlPlaceholders）。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// normalizeAccountIds 等价 Node 各入口的 id 归一化（trim + 去空 + 去重）。
func normalizeAccountIds(accountIds []string) []string {
	unique := make([]string, 0, len(accountIds))
	seen := map[string]bool{}
	for _, id := range accountIds {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	return unique
}

// instantMS 等价 rfc3339InstantMilliseconds；解析失败返回错误。
func instantMS(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("时间戳必须是带 Z 或数值 offset 的 RFC3339 时间：%s", value)
	}
	return parsed.UnixMilli(), nil
}

// canonicalInstant 等价 canonicalizeRfc3339Instant：解析后重新以 UTC 毫秒
// 精度序列化（与 jobs proberepo canonicalInstant 同源）。
func canonicalInstant(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("时间戳必须是带 Z 或数值 offset 的 RFC3339 时间：%s", value)
	}
	return parsed.UTC().Format(rfc3339Milli), nil
}

// formatMillis 以 Node toISOString 形状输出毫秒时间。
func formatMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(rfc3339Milli)
}
