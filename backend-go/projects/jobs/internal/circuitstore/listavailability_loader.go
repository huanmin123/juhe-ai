package circuitstore

// 账户列表可用性投影 LoadItems 物化载荷（网关域读面的 jobs 侧自建）。
// 逐字段对照归档 Node：
//   - modules/accounts/account-list-availability-projection.service.ts
//     loadProjectedAccountListItems（编排 / nextTransition / payload 辅助字段）
//   - storage/account-management-list.repository.ts（列表行 SQL 双模 +
//     accountManagementListItemFromRow 行映射 + listAccountLockStatesAsync）
//   - storage/account-status-snapshot.repository.ts hydrateAccountManagementStatusSeedsDirect
//   - modules/accounts/account-status-snapshot.service.ts getAccountStatusSnapshotFromProjections
//
// 运行态依赖与 Node 同源：
//   - concurrency：Redis account-concurrency-v2 键空间（与 overlay 对账同一实现）；
//   - runtime availability：Redis 网关运行态键空间（gateway-account-recovery
//     probe 状态 + due 调度 + gateway-configured-account-policy-avoidance，
//     对照 loadDistributedGatewayAccountRuntimeAvailability）；
//   - circuit summaries：业务库 account_circuit_incidents 持久账本派生
//     （publicAccountCircuitSummariesFromIncidents；Node worker 同样 DB 直读）。
//
// 登记差异（不做静默降级，见组合根 GoBinding）：
//   - isAccountBalanceSnapshotSuppressed 依赖网关进程内清理协调器内存态；
//     jobs 进程无该组件，等价于协调器空状态（Node 空协调器同样恒 false）。
//
// 任一运行态读面不可用即 fail closed（LoadItems 返回错误 → 维护循环标记
// dependency unavailable 并释放 claim 重放），与 Node 行为一致。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

// CredentialCodec 是账户凭据解密与 API Key 池提取能力（proberepo.Store 同源
// 实现，经组合根注入；可 Mock）。
type CredentialCodec interface {
	DecryptCredentials(envelope string) (map[string]any, error)
	AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry
}

// APIKeyPoolEntry 是 proberepo.KeyEntry 的包内等价形状。
type APIKeyPoolEntry struct {
	ID          string
	Key         string
	Fingerprint string
	Index       int
	Weight      int
}

// ConcurrencySource 是 Redis 账户并发读（account-concurrency-v2 键空间）。
type ConcurrencySource interface {
	LoadConcurrency(ctx context.Context, accountIDs []string) (map[string]int, error)
}

// RuntimeAvailabilitySource 是 Redis 网关运行态可用性读（degraded/precheck/
// policy avoidance → publicAccountRuntimeAvailability 形状）。
type RuntimeAvailabilitySource interface {
	LoadRuntimeAvailability(ctx context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailability, error)
}

// AccountRuntimeAvailability 是 payload runtimeAvailability 的 public 投影。
type AccountRuntimeAvailability struct {
	Status            string         `json:"status"`
	Reason            string         `json:"reason,omitempty"`
	Since             string         `json:"since,omitempty"`
	ProbePresentation map[string]any `json:"probePresentation,omitempty"`
}

// TimezoneSource 提供 usage 统计时区（组合根接 statsverify 读模型）。
type TimezoneSource interface {
	StatsTimezone(ctx context.Context) (*time.Location, error)
}

// ProjectionLoadConfig 组装投影 LoadItems 依赖。
type ProjectionLoadConfig struct {
	// Business 是业务库双模句柄（accounts/授权/分组/锁/标签/circuit 账本）。
	Business *sql.DB
	// Stats 是统计库双模句柄（usage 汇总 + quota 成本 + 余额快照）。
	Stats *sql.DB
	// Postgres / StatsPostgres 标记两个库的方言（Node databaseDriver 双模）。
	Postgres      bool
	StatsPostgres bool
	// Secret 是凭据指纹 HMAC 密钥（runtimeConfig.secret 同源）。
	Secret string
	// Credentials 缺省时 apiKeyRuntime 汇总不可读（fail closed）。
	Credentials CredentialCodec
	// Concurrency / RuntimeAvailability 缺省或读取失败时按 Node fail-closed
	// 语义返回错误（claim 释放重放 + dependency unavailable）。
	Concurrency         ConcurrencySource
	RuntimeAvailability RuntimeAvailabilitySource
	// Timezone 缺省时 usage/quota 窗口键不可计算（fail closed）。
	Timezone TimezoneSource
	// Now 可注入测试时钟。
	Now func() time.Time
}

// ProjectionItemLoader 实现 opsjobs.ItemLoader 的载荷组装
// （circuitstore 对 ListAvailabilityRepo 17 方法之外的 LoadItems 补齐）。
type ProjectionItemLoader struct {
	db            *sql.DB
	statsDB       *sql.DB
	postgres      bool
	statsPostgres bool
	secret        string
	credentials   CredentialCodec
	concurrency   ConcurrencySource
	runtime       RuntimeAvailabilitySource
	timezone      TimezoneSource
	now           func() time.Time
}

// NewProjectionItemLoader 构建载荷组装器；依赖缺失即 fail closed。
func NewProjectionItemLoader(config ProjectionLoadConfig) (*ProjectionItemLoader, error) {
	if config.Business == nil {
		return nil, errors.New("circuitstore 投影 loader 缺少业务库句柄")
	}
	if config.Stats == nil {
		return nil, errors.New("circuitstore 投影 loader 缺少统计库句柄")
	}
	if config.Credentials == nil {
		return nil, errors.New("circuitstore 投影 loader 缺少凭据解码器")
	}
	if config.Concurrency == nil || config.RuntimeAvailability == nil {
		return nil, errors.New("circuitstore 投影 loader 缺少运行态读面（Redis concurrency/runtime availability）")
	}
	if config.Timezone == nil {
		return nil, errors.New("circuitstore 投影 loader 缺少统计时区读面")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ProjectionItemLoader{
		db:            config.Business,
		statsDB:       config.Stats,
		postgres:      config.Postgres,
		statsPostgres: config.StatsPostgres,
		secret:        config.Secret,
		credentials:   config.Credentials,
		concurrency:   config.Concurrency,
		runtime:       config.RuntimeAvailability,
		timezone:      config.Timezone,
		now:           now,
	}, nil
}

// hydratedEntry 是水合后的单账户载荷（AccountListItem 等价 + 投影列输入）。
type hydratedEntry struct {
	// payload 是 AccountListItem 的 JSON 形状（camelCase 键，与 Node 写入
	// projection.payload_json 逐字段一致；undefined 字段不出现）。
	payload map[string]any
	// 投影列输入（accountListAvailabilityProjectionWrite 的取值面）。
	accountID           string
	effectiveStatus     string
	effectiveAvailable  bool
	currentConcurrency  int
	providerCode        string
	profileID           string
	accountType         string
	boundGroupID        string
	name                string
	priority            int
	superPriority       bool
	fallback            bool
	concurrencyLimit    int
	sourceAccountID     string
	authorizationID     string
	// sortLastUsedAt 是 accounts.last_used_at（legacy 排序键，与公共
	// lastUsedAt 字段分离；authorized 行公共字段展示授权用量）。
	sortLastUsedAt *string
	// nextTransition 候选输入（Node nextTransitionAt 的候选集合）。
	accountExpiresAt    *string // 本账户到期（payload 无此键，仅作候选）
	authorizationExpiresAt *string
	sourceExpiresAt     *string
	sourceCooldownUntil *string
	cooldownUntil       *string
	apiKeyNextProbeAt   *string
	runtimeNextAttemptAt *string
	runtimeRecoveryAt   *string
	statusBoundaryAt    *string
	quotaResetAt        *string
	availabilityScheduleJSON       *string
	sourceAvailabilityScheduleJSON *string
}

// LoadItems 对齐 Node loadProjectedAccountListItems：可见范围行 → 运行态水合
// → ProjectionItem（payload = AccountListItem 去投影辅助字段）。
func (l *ProjectionItemLoader) LoadItems(ctx context.Context, viewerSystemAccountID string, accountIDs []string) ([]opsjobs.ProjectionItem, error) {
	viewer := strings.TrimSpace(viewerSystemAccountID)
	if viewer == "" {
		return nil, errors.New("账户列表投影缺少 viewer 系统账户上下文")
	}
	ids, err := normalizedIDList(accountIDs)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []opsjobs.ProjectionItem{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := l.now()
	page, err := l.loadManagementPage(ctx, viewer, ids)
	if err != nil {
		return nil, err
	}
	if len(page.rows) == 0 {
		return nil, fmt.Errorf("账户列表投影账户 %v 在当前可见范围中缺失", ids)
	}
	entries, err := l.hydratePage(ctx, page, now)
	if err != nil {
		return nil, err
	}
	items := make([]opsjobs.ProjectionItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, l.projectionItemFromEntry(entry, now))
	}
	return items, nil
}

// projectionItemFromEntry 对齐 accountListAvailabilityProjectionWrite 的取值
// 面：payload 保留整个 AccountListItem（Node 通过解构排除
// authorizationInstanceSourceAccountAvailabilitySchedule /
// accountListProjectionNextTransitionAt / accountListProjectionSortLastUsedAt
// 三个辅助字段；Go 侧 payload 从构建起即不含它们），复合到期排序键与
// nextTransition 候选在 loader 侧合成。
func (l *ProjectionItemLoader) projectionItemFromEntry(entry hydratedEntry, now time.Time) opsjobs.ProjectionItem {
	// Node accountExpiresAtSortKey = authorizationExpiresAt ?? instanceSourceExpiresAt
	// ?? accountExpiresAt；port 契约把复合结果放在 AccountExpiresAt。
	accountExpiresAtSortKey := entry.authorizationExpiresAt
	if accountExpiresAtSortKey == nil || *accountExpiresAtSortKey == "" {
		accountExpiresAtSortKey = entry.sourceExpiresAt
	}
	if accountExpiresAtSortKey == nil || *accountExpiresAtSortKey == "" {
		accountExpiresAtSortKey = entry.accountExpiresAt
	}
	// Node nextTransitionAt 候选：item.accountExpiresAt / cooldownUntil /
	// authorizationExpiresAt / instanceSourceExpiresAt /
	// instanceSourceCooldownUntil / apiKeyRuntime.nextProbeAt /
	// runtimeAvailability.probePresentation.schedule.nextAttemptAt /
	// runtimeAvailability.probePresentation.recoveryAt /
	// availabilityPresentation.statusBoundary.at /
	// accountListProjectionNextTransitionAt（= quotaResetAt）+ 两个
	// availability schedule 的下一次检查点。
	candidates := []string{
		derefString(entry.accountExpiresAt),
		derefString(entry.cooldownUntil),
		derefString(entry.authorizationExpiresAt),
		derefString(entry.sourceExpiresAt),
		derefString(entry.sourceCooldownUntil),
		derefString(entry.apiKeyNextProbeAt),
		derefString(entry.runtimeNextAttemptAt),
		derefString(entry.runtimeRecoveryAt),
		derefString(entry.statusBoundaryAt),
		derefString(entry.quotaResetAt),
		nextAvailabilityScheduleCheckAt(parseSchedulePtr(entry.availabilityScheduleJSON), now),
		nextAvailabilityScheduleCheckAt(parseSchedulePtr(entry.sourceAvailabilityScheduleJSON), now),
	}
	return opsjobs.ProjectionItem{
		AccountID:                 entry.accountID,
		EffectiveStatus:           entry.effectiveStatus,
		CurrentConcurrency:        entry.currentConcurrency,
		SourceAccountID:           entry.sourceAccountID,
		AuthorizationID:           entry.authorizationID,
		ProviderCode:              entry.providerCode,
		ProviderProtocolProfileID: entry.profileID,
		AccountType:               entry.accountType,
		BoundGroupID:              entry.boundGroupID,
		Name:                      entry.name,
		Priority:                  entry.priority,
		SuperPriorityEnabled:      entry.superPriority,
		FallbackEnabled:           entry.fallback,
		ConcurrencyLimit:          entry.concurrencyLimit,
		AccountExpiresAt:          derefString(accountExpiresAtSortKey),
		LastUsedAt:                derefString(entry.sortLastUsedAt),
		Payload:                   entry.payload,
		TagIDs:                    payloadTagIDs(entry.payload),
		EffectiveAvailable:        boolPtr(entry.effectiveAvailable),
		NextTransitionCandidates:  candidates,
	}
}

func payloadTagIDs(payload map[string]any) []string {
	// buildBasePayload 以 []map[string]any 填充 tags；JSON 反序列化形状为
	// []any，两种来源都兼容。
	var tags []map[string]any
	switch typed := payload["tags"].(type) {
	case []map[string]any:
		tags = typed
	case []any:
		for _, item := range typed {
			if tagMap, ok := item.(map[string]any); ok {
				tags = append(tags, tagMap)
			}
		}
	}
	ids := make([]string, 0, len(tags))
	for _, tagMap := range tags {
		if id, ok := tagMap["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func parseSchedulePtr(raw *string) *oauthrefresh.AvailabilitySchedule {
	if raw == nil || *raw == "" {
		return nil
	}
	schedule, err := oauthrefresh.ParseScheduleJSON(*raw)
	if err != nil {
		return nil
	}
	return schedule
}

// nextAvailabilityScheduleCheckAt 对齐 nextAccountAvailabilityScheduleCheckAt。
func nextAvailabilityScheduleCheckAt(schedule *oauthrefresh.AvailabilitySchedule, now time.Time) string {
	if schedule == nil {
		return ""
	}
	value, ok := oauthrefresh.NextScheduleCheckAt(schedule, now)
	if !ok {
		return ""
	}
	return value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolPtr(value bool) *bool { return &value }
