package accounts

import (
	"context"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountsbalance"
)

// REFACTOR-0005 阶段 B 桥接层：余额与探子子域（accountsbalance）从根包拆出
// 后，门面在这里集中保留类型别名、错误哨兵别名、Store 方法转发与自由函数
// 转发，保证根包内既有引用（m11_routes.go HTTP 面、patch.go/write.go 写路径、
// w9d/w10a/w13a/w13g/w14b/w14l/w2 各波测试）与外部消费文件零改动。
//
// 降级留根（阶段 A 先例）：prepareBalanceDraft/balanceDraftRow（门面路由按
// 字段读取其私有结构，输入全部是门面 write-path helper）、
// findForceActivateSummary（ListPage 深读，经 Deps.FindAccountSummary 注入
// 子域）、m11ScheduleAllowed（schedule.go 门面面，经 Deps.ScheduleGate 注入）、
// hydrateOAuthUsageSnapshots（ListItem 投影循环）与
// StoreBalanceSnapshotCleaner（依赖门面 retryQueue 泛型 + *Store 统计删除面）
// 保留在门面。
//
// 转发每次以 Store 的活字段构造子域 Service（无缓存指针），因此
// "clone := *store; clone.db = ..." 的故障注入克隆语义保持不变。

// ---- 门面别名（子域导出类型） ----

type (
	// BalanceKeySnapshot mirrors AccountBalanceKeySnapshot.
	BalanceKeySnapshot = accountsbalance.BalanceKeySnapshot
	// BalanceDetails mirrors the GET /:id/balance/details projection.
	BalanceDetails = accountsbalance.BalanceDetails
	// BalanceRefreshCandidate mirrors AccountBalanceRefreshCandidate.
	BalanceRefreshCandidate = accountsbalance.BalanceRefreshCandidate
	// BalanceManualRefreshOutcome mirrors the manual refresh outcome tuple.
	BalanceManualRefreshOutcome = accountsbalance.BalanceManualRefreshOutcome
	// ManualBalanceRefresher is the narrow execution port for the live upstream
	// balance paths.
	ManualBalanceRefresher = accountsbalance.ManualBalanceRefresher
	// BalanceDraftProbeInput mirrors the AccountBalanceQueryCandidate shape.
	BalanceDraftProbeInput = accountsbalance.BalanceDraftProbeInput
	// ModelCatalogRefresher is the model-catalog discovery execution port.
	ModelCatalogRefresher = accountsbalance.ModelCatalogRefresher
	// ModelCatalogDiscoveryInput carries the prepared draft account.
	ModelCatalogDiscoveryInput = accountsbalance.ModelCatalogDiscoveryInput
	// BalanceCapabilityInput mirrors AccountBalanceCapabilityInput.
	BalanceCapabilityInput = accountsbalance.BalanceCapabilityInput
	// OAuthUsageWindow mirrors AccountOAuthUsageWindow.
	OAuthUsageWindow = accountsbalance.OAuthUsageWindow
	// OAuthUsageSnapshot mirrors AccountOAuthUsageSnapshot.
	OAuthUsageSnapshot = accountsbalance.OAuthUsageSnapshot
	// BalanceSnapshotCleanupRequest mirrors AccountBalanceSnapshotCleanupRequest.
	BalanceSnapshotCleanupRequest = accountsbalance.BalanceSnapshotCleanupRequest
	// BalanceSnapshotCleaner is the nil-safe post-commit cleanup port.
	BalanceSnapshotCleaner = accountsbalance.BalanceSnapshotCleaner
)

// 根包历史私有名 → 子域类型别名（测试字面量沿用旧名）.
type balanceSnapshotRecord = accountsbalance.BalanceSnapshotRecord

// 错误哨兵（子域定义，门面别名保持 errors.Is 身份）.
var (
	errBalanceDetailsDisabled  = accountsbalance.ErrBalanceDetailsDisabled
	errBalanceDetailsForbidden = accountsbalance.ErrBalanceDetailsForbidden
	errBalanceRefreshForbidden = accountsbalance.ErrBalanceRefreshForbidden
)

// 清理 reason 常量（子域定义，门面别名）.
const (
	BalanceSnapshotCleanupReasonConfigurationChanged = accountsbalance.BalanceSnapshotCleanupReasonConfigurationChanged
	BalanceSnapshotCleanupReasonMultipleAPIKeys      = accountsbalance.BalanceSnapshotCleanupReasonMultipleAPIKeys
	BalanceSnapshotCleanupReasonBatchMultipleAPIKeys = accountsbalance.BalanceSnapshotCleanupReasonBatchMultipleAPIKeys
	BalanceSnapshotCleanupReasonBatchIdentityChanged = accountsbalance.BalanceSnapshotCleanupReasonBatchIdentityChanged

	// quotaRecoveryFixedJitterMinutes（原 quota_recovery.go 门面常量，子域
	// 导出后别名；m11_reads.go 展示面沿用旧名）.
	quotaRecoveryFixedJitterMinutes = accountsbalance.QuotaRecoveryFixedJitterMinutes

	// quotaRecoveryMaxPolicyBytes（同上；w14b/w14l 测试引用）.
	quotaRecoveryMaxPolicyBytes = accountsbalance.QuotaRecoveryMaxPolicyBytes
)

// ForceActivateResult mirrors the force-activate outcome the route consumes
// （summary 行是门面 ListItem 深读结果，留在门面定义；子域以
// ForceActivateOutcome{Account any} 跨桥，门面在此还原类型）.
type ForceActivateResult struct {
	Account *ListItem
	Changed bool
}

// ---- StoreBase 适配 + 子域 Service 构造（活字段，无缓存） ----
// storeBaseAdapter 复用 test_subdomain_bridge.go 的实现。

// balanceService builds the subdomain service over this store's live fields.
func (s *Store) balanceService() *accountsbalance.Service {
	return accountsbalance.New(storeBaseAdapter{s}, accountsbalance.Deps{
		AuthorizedReadableIDs:        s.authorizedReadableIDs,
		AdvanceBatchDispatchRevision: s.advanceBatchDispatchRevision,
		FindAccountSummary: func(ctx context.Context, accountID, ownerID string) (any, error) {
			item, err := s.findForceActivateSummary(ctx, accountID, ownerID)
			if item == nil {
				return nil, err
			}
			return item, err
		},
		ScheduleGate: m11ScheduleAllowed,
		NewDispatchID: func() string {
			return newID("dispatch")
		},
	})
}

// ---- 手动余额刷新 / 模型目录注入口（Store 字段保留，组合根装配不变） ----

// SetManualBalanceRefresher wires the port (composition-root handover).
func (s *Store) SetManualBalanceRefresher(refresher ManualBalanceRefresher) {
	s.balanceRefresher = refresher
}

// BalanceRefresherPort exposes the wired manual balance refresher for the
// composition-root tests (nil when the port was left unwired).
func (s *Store) BalanceRefresherPort() ManualBalanceRefresher { return s.balanceRefresher }

// SetModelCatalogRefresher wires the port (composition-root handover).
func (s *Store) SetModelCatalogRefresher(refresher ModelCatalogRefresher) {
	s.modelCatalogRefresher = refresher
}

// ModelCatalogRefresherPort exposes the wired model catalog refresher for the
// composition-root tests (nil when the port was left unwired).
func (s *Store) ModelCatalogRefresherPort() ModelCatalogRefresher { return s.modelCatalogRefresher }

// ---- Store 方法转发（余额与探子子域） ----

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite — the Go test database keeps one file).
func (s *Store) statsTable(name string) string {
	return s.balanceService().StatsTable(name)
}

// balanceAPIKeyFingerprint mirrors accountBalanceApiKeyFingerprint（子域为
// Service 方法，门面保留原 Store 方法形态）.
func (s *Store) balanceAPIKeyFingerprint(value string) string {
	return s.balanceService().BalanceAPIKeyFingerprint(value)
}

func (s *Store) loadBalanceSnapshotRecord(ctx context.Context, accountID string) (*balanceSnapshotRecord, error) {
	return s.balanceService().LoadBalanceSnapshotRecord(ctx, accountID)
}

func (s *Store) loadOpenAICodexUsageSnapshots(ctx context.Context, accountIDs []string) (map[string]*OAuthUsageSnapshot, error) {
	return s.balanceService().LoadOpenAICodexUsageSnapshots(ctx, accountIDs)
}

func (s *Store) FindBalanceDetails(ctx context.Context, accountID string, access AccessScope) (*BalanceDetails, error) {
	return s.balanceService().FindBalanceDetails(ctx, accountID, access)
}

func (s *Store) FindBalanceManualRefreshCandidate(ctx context.Context, accountID string) (*BalanceRefreshCandidate, error) {
	return s.balanceService().FindBalanceManualRefreshCandidate(ctx, accountID)
}

func (s *Store) ForceActivatePending(ctx context.Context, accountID string, access AccessScope) (*ForceActivateResult, error) {
	outcome, err := s.balanceService().ForceActivatePending(ctx, accountID, access)
	if outcome == nil || err != nil {
		return nil, err
	}
	account, _ := outcome.Account.(*ListItem)
	return &ForceActivateResult{Account: account, Changed: outcome.Changed}, nil
}

// ---- 门面留根：findForceActivateSummary / m11ScheduleAllowed ----

// findForceActivateSummary re-reads the sanitized account summary
// (Node findAccountSummaryAsync with the owner access).
func (s *Store) findForceActivateSummary(ctx context.Context, accountID, ownerID string) (*ListItem, error) {
	result, err := s.ListPage(ctx, AccessScope{ViewerID: ownerID}, ListOptions{IDs: []string{accountID}, Page: 1, PageSize: 1})
	if err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	return &result.Items[0], nil
}

// scheduleAllows evaluates the availability schedule at the given instant; a
// blank schedule stays always-allowed (Node
// isAccountAvailabilityScheduleAllowed with a null schedule).
func m11ScheduleAllowed(scheduleJSON string, now time.Time) bool {
	trimmed := strings.TrimSpace(scheduleJSON)
	if trimmed == "" {
		return true
	}
	schedule, err := ParseScheduleJSON(trimmed)
	if err != nil || schedule == nil {
		return true
	}
	if override, ok := ScheduleStatus(schedule, now); ok {
		return override == "active"
	}
	return true
}

// ---- 门面留根：prepareBalanceDraft（路由侧草稿准备） ----

// balanceDraftRow is the draft-test account projection (the strict body
// account plus the group/provider resolution).
type balanceDraftRow struct {
	groupID          string
	groupName        string
	ownerID          string
	accountType      string
	providerCode     string
	providerProfile  *providerProfile
	credentials      Credentials
	proxyProfileID   *string
	supportedModels  []string
	healthCheckModel string
	healthCheckMode  string
}

// prepareBalanceDraft mirrors prepareAccountDraftTestSnapshotAsync for the
// balance probe: the group/provider contract checks plus the credentials
// normalization. Errors carry the Node 400 copy.
func (s *Store) prepareBalanceDraft(ctx context.Context, accountInput map[string]any, access AccessScope) (*balanceDraftRow, error) {
	groupID := strings.TrimSpace(textString(accountInput["groupId"]))
	providerCode := strings.TrimSpace(textString(accountInput["providerCode"]))
	accountType := strings.TrimSpace(textString(accountInput["type"]))
	if groupID == "" || providerCode == "" || accountType == "" {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	group, err := s.groupOwnerAndProvider(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	if group == nil || group.providerCode != providerCode {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	owner := group.systemAccountID
	if owner == "" {
		owner = access.EffectiveViewerID()
	}
	if owner == "" {
		return nil, &ValidationError{Message: "账户分组缺少归属用户，无法测试"}
	}
	profileID := strings.TrimSpace(textString(accountInput["providerProtocolProfileId"]))
	if profileID == "" {
		return nil, &ValidationError{Message: "账户 providerProtocolProfileId 不能为空"}
	}
	profile, err := s.requireEnabledProviderProtocolProfile(ctx, s.db, providerCode, profileID)
	if err != nil {
		return nil, err
	}
	supported := false
	for _, item := range profile.accountTypes {
		if item == accountType {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &ValidationError{Message: "供应商 " + providerCode + " 不支持账户类型 " + accountType}
	}
	credentialsInput, _ := accountInput["credentials"].(map[string]any)
	if credentialsInput == nil {
		credentialsInput = map[string]any{}
	}
	// draftAccountCredentials: the oauth draft falls back to the profile base
	// URL when the input omits base_url.
	if accountType != "oauth" || textString(credentialsInput["base_url"]) == "" {
		if accountType == "oauth" {
			fallback := profile.baseURL
			if strings.TrimSpace(fallback) == "" {
				fallback = "https://api.openai.com/v1"
			}
			credentialsInput["base_url"] = fallback
		}
	}
	credentials, err := NormalizeAccountCredentialsForWrite(accountType, Credentials(credentialsInput), &EndpointModeDefaultContext{
		ProviderCode:              providerCode,
		AccountType:               accountType,
		ProviderProtocolProfileID: profile.id,
		ProtocolCode:              profile.protocolCode,
		ProtocolVersion:           profile.protocolVersion,
	})
	if err != nil {
		return nil, err
	}
	row := &balanceDraftRow{
		groupID:          groupID,
		groupName:        group.name.String,
		ownerID:          owner,
		accountType:      accountType,
		providerCode:     providerCode,
		providerProfile:  profile,
		credentials:      credentials,
		healthCheckModel: textString(accountInput["healthCheckModel"]),
		healthCheckMode:  textString(accountInput["healthCheckEndpointMode"]),
	}
	if list, ok := accountInput["supportedModels"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				row.supportedModels = append(row.supportedModels, strings.TrimSpace(text))
			}
		}
	}
	if text, ok := accountInput["proxyProfileId"].(string); ok && strings.TrimSpace(text) != "" {
		row.proxyProfileID = &text
	}
	return row, nil
}

// ---- 门面留根：hydrateOAuthUsageSnapshots（ListItem 投影循环） ----

// hydrateOAuthUsageSnapshots mirrors the accountSummariesFromRows hydration
// (account-summary.repository.ts:1452 + :1575): gpt-provider oauth rows carry
// the Codex usage snapshot of the fact (credential-source) account id;
// everything else stays undefined.
func (s *Store) hydrateOAuthUsageSnapshots(ctx context.Context, items []ListItem) error {
	factIDs := []string{}
	seen := map[string]bool{}
	eligible := func(item ListItem) bool {
		return isGptVendorCodeToken(item.ProviderCode) && item.Type == "oauth"
	}
	for _, item := range items {
		if !eligible(item) {
			continue
		}
		factID := item.ID
		if item.AuthorizationInstanceSourceAccountID != nil && *item.AuthorizationInstanceSourceAccountID != "" {
			factID = *item.AuthorizationInstanceSourceAccountID
		}
		if !seen[factID] {
			seen[factID] = true
			factIDs = append(factIDs, factID)
		}
	}
	if len(factIDs) == 0 {
		return nil
	}
	snapshots, err := s.loadOpenAICodexUsageSnapshots(ctx, factIDs)
	if err != nil {
		return err
	}
	for index := range items {
		if !eligible(items[index]) {
			continue
		}
		factID := items[index].ID
		if items[index].AuthorizationInstanceSourceAccountID != nil && *items[index].AuthorizationInstanceSourceAccountID != "" {
			factID = *items[index].AuthorizationInstanceSourceAccountID
		}
		if snapshot, ok := snapshots[factID]; ok {
			items[index].OAuthUsage = snapshot
		}
	}
	return nil
}

// ---- 自由函数转发（配置归一、快照投影与配额恢复策略，根包测试沿用旧名） ----

func NormalizeAccountBalanceConfig(input any) (map[string]any, error) {
	return accountsbalance.NormalizeAccountBalanceConfig(input)
}

func EffectiveAccountApiKeys(credentials Credentials) []string {
	return accountsbalance.EffectiveAccountApiKeys(credentials)
}

func EffectiveAccountApiKeyCount(credentials Credentials) int {
	return accountsbalance.EffectiveAccountApiKeyCount(credentials)
}

func ValidateAccountBalanceCapability(account BalanceCapabilityInput, enabled bool) (bool, error) {
	return accountsbalance.ValidateAccountBalanceCapability(account, enabled)
}

func normalizeBalanceCustomConfig(value any, present bool) (map[string]any, error) {
	return accountsbalance.NormalizeBalanceCustomConfig(value, present)
}

func normalizedBalanceBaseURL(value any) string {
	return accountsbalance.NormalizedBalanceBaseURL(value)
}

func canonicalBalanceConfigJSON(config map[string]any) (string, error) {
	return accountsbalance.CanonicalBalanceConfigJSON(config)
}

func balanceSnapshotTimestampMs(value string) (int64, bool) {
	return accountsbalance.BalanceSnapshotTimestampMs(value)
}

func balanceSnapshotMatchesConfiguration(nextRefreshAt string, configRevision int64, record *balanceSnapshotRecord) bool {
	return accountsbalance.BalanceSnapshotMatchesConfiguration(nextRefreshAt, configRevision, record)
}

func maskBalanceAPIKey(value string) string {
	return accountsbalance.MaskBalanceAPIKey(value)
}

func normalizeQuotaRecoveryPolicy(value any) (map[string]any, error) {
	return accountsbalance.NormalizeQuotaRecoveryPolicy(value)
}

func normalizeQuotaRecoverySchedule(value any) (map[string]any, error) {
	return accountsbalance.NormalizeQuotaRecoverySchedule(value)
}

func validQuotaRecoveryTimezone(name string) bool {
	return accountsbalance.ValidQuotaRecoveryTimezone(name)
}

func quotaRecoveryIntegerInRange(value any, min, max int, label string) (float64, error) {
	return accountsbalance.QuotaRecoveryIntegerInRange(value, min, max, label)
}

func jsonEncodeLength(value any) int {
	return accountsbalance.JSONEncodeLength(value)
}

func requiredRFC3339Instant(value, label string) (string, error) {
	return accountsbalance.RequiredRFC3339Instant(value, label)
}

func rfc3339InstantMilliseconds(value string) (int64, error) {
	return accountsbalance.RFC3339InstantMilliseconds(value)
}

func numberFromSnapshot(value any) (*float64, bool) {
	return accountsbalance.NumberFromSnapshot(value)
}

func optionalSnapshotString(value any) string {
	return accountsbalance.OptionalSnapshotString(value)
}

func oauthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt string) (*OAuthUsageSnapshot, error) {
	return accountsbalance.OAuthUsageSnapshotFromRow(source, snapshotJSON, refreshStatus, lastAttemptAt, lastSuccessAt, nextRefreshAfter, lastErrorMessage, updatedAt)
}

func oauthUsageWindowFromSnapshot(snapshot map[string]any, window, updatedAt string) (*OAuthUsageWindow, error) {
	return accountsbalance.OAuthUsageWindowFromSnapshot(snapshot, window, updatedAt)
}
