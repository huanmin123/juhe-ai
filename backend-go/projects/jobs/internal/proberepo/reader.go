package proberepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
)

// EffectiveAvailabilityLimitations 说明 effectiveAvailability 派生的已知边界
// （迁移报告同步披露，不构成静默降级）：
//  1. gateway 运行态分支（runtime_precheck_pending / runtime_local_suppressed /
//     runtime_half_open / runtime_precheck_failed）依赖网关进程内的运行态缓存，
//     jobs 进程不可读。这些分支只影响“是否跳过探针”，不影响状态写入正确性
//     （写入路径全部带 dispatch_revision / status / updated_at CAS 围栏），
//     表现为 jobs 可能对 Node 会跳过的账户多发起一次无害探针。
//  2. 授权实例的 authorizationQuotaExceeded 分支依赖用量聚合读模型，jobs 侧
//     未迁移；同上只影响探针跳过判断。
//  3. 模型目录（provider model catalog）的 imageOnly 协议分支未迁移；
//     health_check_endpoint_mode 显式配置为 images_json 时仍走 Images 探针。

// AccountForTestView 是 find_account_for_test 的完整投影（含凭据）。
type AccountForTestView struct {
	accountquality.AccountForTest
	ProviderCode            string
	ProtocolVersion         string
	HealthCheckModel        string
	HealthCheckEndpointMode string
	SupportedModels         []string
	Credentials             map[string]any
	APIKeyRuntime           map[string]string // fingerprint -> status
	// EffectiveAvailabilityStatus 为不可用时的 status 值（诊断用）。
	EffectiveAvailabilityStatus string
	HasEffectiveStatus          bool
	// AvailabilityLimited 为 true 表示第 1/2 类不可用分支未参与派生。
	AvailabilityLimited bool
}

// accountRowSQL 是账户主查询（owner + authorized instance 双路径合一）。
const accountRowSQL = `
    SELECT a.id, a.system_account_id, a.name, a.type, a.status, a.schedulable,
      a.provider_code, a.provider_protocol_profile_id, a.protocol_code, a.protocol_version, a.client_compatibility,
      a.health_check_model, a.health_check_endpoint_mode, a.account_expires_at, a.cooldown_until,
      a.last_error_code, a.last_error_message, a.credentials_encrypted,
      a.authorization_instance_authorization_id, a.authorization_instance_source_account_id,
      a.authorization_instance_owner_system_account_id,
      src.status AS source_status, src.schedulable AS source_schedulable,
      src.account_expires_at AS source_account_expires_at, src.cooldown_until AS source_cooldown_until,
      src.last_error_code AS source_last_error_code, src.last_error_message AS source_last_error_message,
      auth.status AS authorization_status, auth.expires_at AS authorization_expires_at,
      auth.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      (SELECT ga.group_id
        FROM %s ga
        WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = 1
        ORDER BY ga.updated_at DESC, ga.group_id ASC, ga.account_id ASC
        LIMIT 1) AS bound_group_id
    FROM %s a
    LEFT JOIN %s src ON src.id = a.authorization_instance_source_account_id AND src.deleted_at IS NULL
    LEFT JOIN %s auth ON auth.id = a.authorization_instance_authorization_id

`

// FindAccountForTest 实现 accountquality.AccountReader（Node find_account_for_test
// 的被消费字段投影；internal 全域访问语义）。
func (s *Store) FindAccountForTest(ctx context.Context, accountID string) (*accountquality.AccountForTest, error) {
	view, err := s.LoadAccountForTest(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	summary := view.AccountForTest
	return &summary, nil
}

// LoadAccountForTest 返回完整视图（含凭据与可用性派生输入）。
func (s *Store) LoadAccountForTest(ctx context.Context, accountID string) (*AccountForTestView, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil
	}
	query := fmt.Sprintf(accountRowSQL+`
    WHERE a.id = ? AND a.deleted_at IS NULL
    LIMIT 1
  `, s.table("group_accounts"), s.table("accounts"), s.table("accounts"), s.table("resource_authorizations"))
	var (
		view                                 AccountForTestView
		id, systemID, name, accountType      sql.NullString
		status                               sql.NullString
		schedulable                          any
		providerCode, protocolCode           sql.NullString
		protocolProfile, protocolVersion     sql.NullString
		clientCompatibility                  sql.NullString
		healthModel, healthMode              sql.NullString
		expiresAt, cooldownUntil             sql.NullString
		lastErrorCode, lastErrorMessage      sql.NullString
		credentials                          sql.NullString
		authzID, sourceID, authzOwner        sql.NullString
		sourceStatus                         sql.NullString
		sourceSchedulable                    any
		sourceExpiresAt, sourceCooldownUntil sql.NullString
		sourceErrorCode, sourceErrorMessage  sql.NullString
		authorizationStatus, authzExpiresAt  sql.NullString
		authzResourceOwner                   sql.NullString
		boundGroupID                         sql.NullString
	)
	row := s.db.QueryRowContext(ctx, query, accountID)
	if err := row.Scan(&id, &systemID, &name, &accountType, &status, &schedulable,
		&providerCode, &protocolProfile, &protocolCode, &protocolVersion, &clientCompatibility,
		&healthModel, &healthMode, &expiresAt, &cooldownUntil,
		&lastErrorCode, &lastErrorMessage, &credentials,
		&authzID, &sourceID, &authzOwner,
		&sourceStatus, &sourceSchedulable,
		&sourceExpiresAt, &sourceCooldownUntil, &sourceErrorCode, &sourceErrorMessage,
		&authorizationStatus, &authzExpiresAt, &authzResourceOwner,
		&boundGroupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	credentialsMap, err := s.DecryptCredentials(credentials.String)
	if err != nil {
		// Node openAIAccountSecretFromRow：解密失败按候选缺失处理。
		return nil, nil
	}
	supportedModels, err := s.loadSupportedModels(ctx, id.String)
	if err != nil {
		return nil, err
	}
	apiKeyRuntime, err := s.loadAPIKeyRuntimeStatuses(ctx, id.String)
	if err != nil {
		return nil, err
	}
	nowMS := s.nowMS()
	available, statusValue, hasStatus, limited := s.deriveEffectiveAvailability(deriveInput{
		accessType:             ternary(authzID.String != "", "authorized", "owner"),
		boundGroupID:           boundGroupID.String,
		status:                 status.String,
		schedulable:            truthy(schedulable),
		expiresAt:              expiresAt.String,
		cooldownUntil:          cooldownUntil.String,
		lastErrorCode:          lastErrorCode.String,
		lastErrorMessage:       lastErrorMessage.String,
		authorizationStatus:    authorizationStatus.String,
		authorizationExpiresAt: authzExpiresAt.String,
		sourceID:               sourceID.String,
		sourceStatus:           sourceStatus.String,
		sourceSchedulable:      sourceSchedulable == nil || truthy(sourceSchedulable),
		sourceExpiresAt:        sourceExpiresAt.String,
		sourceCooldownUntil:    sourceCooldownUntil.String,
		sourceErrorCode:        sourceErrorCode.String,
		sourceErrorMessage:     sourceErrorMessage.String,
		credentials:            credentialsMap,
		apiKeyRuntime:          apiKeyRuntime,
		nowMS:                  nowMS,
	})
	view.AccountForTest = accountquality.AccountForTest{
		ID:                   id.String,
		Name:                 name.String,
		Type:                 accountType.String,
		Status:               status.String,
		Schedulable:          truthy(schedulable),
		BoundGroupID:         boundGroupID.String,
		OwnerSystemAccountID: systemID.String,
		SystemAccountID:      systemID.String,
		ProtocolCode:         protocolCode.String,
		AccountExpiresAt:     expiresAt.String,
		EffectiveAvailable:   available,
		HasEffectiveAvail:    true,
	}
	view.ProviderCode = providerCode.String
	view.ProtocolVersion = protocolVersion.String
	view.HealthCheckModel = healthModel.String
	view.HealthCheckEndpointMode = healthMode.String
	view.SupportedModels = supportedModels
	view.Credentials = credentialsMap
	view.APIKeyRuntime = apiKeyRuntime
	view.EffectiveAvailabilityStatus = statusValue
	view.HasEffectiveStatus = hasStatus
	view.AvailabilityLimited = limited
	return &view, nil
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed == 1
	case int:
		return typed == 1
	}
	return false
}

func ternary(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func (s *Store) loadSupportedModels(ctx context.Context, accountID string) ([]string, error) {
	query := fmt.Sprintf(`SELECT model FROM %s WHERE account_id = ? ORDER BY created_at ASC, model ASC`, s.table("account_supported_models"))
	rows, err := s.db.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []string
	seen := map[string]bool{}
	for rows.Next() {
		var model sql.NullString
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		text := strings.TrimSpace(model.String)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		models = append(models, text)
	}
	return models, rows.Err()
}

func (s *Store) loadAPIKeyRuntimeStatuses(ctx context.Context, accountID string) (map[string]string, error) {
	query := fmt.Sprintf(`SELECT key_fingerprint, status FROM %s WHERE account_id = ?`, s.table("account_api_key_runtime_states"))
	rows, err := s.db.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var fingerprint, status sql.NullString
		if err := rows.Scan(&fingerprint, &status); err != nil {
			return nil, err
		}
		if fingerprint.String == "" {
			continue
		}
		states[fingerprint.String] = status.String
	}
	return states, rows.Err()
}

// deriveInput 是 effectiveAvailability 派生的输入集合。
type deriveInput struct {
	accessType             string
	boundGroupID           string
	status                 string
	schedulable            bool
	expiresAt              string
	cooldownUntil          string
	lastErrorCode          string
	lastErrorMessage       string
	authorizationStatus    string
	authorizationExpiresAt string
	sourceID               string
	sourceStatus           string
	sourceSchedulable      bool
	sourceExpiresAt        string
	sourceCooldownUntil    string
	sourceErrorCode        string
	sourceErrorMessage     string
	credentials            map[string]any
	apiKeyRuntime          map[string]string
	nowMS                  int64
}

// deriveEffectiveAvailability 移植 accountEffectiveAvailability 的 DB 分支，
// 返回 (available, blockedStatus, hasStatus, limited)。
func (s *Store) deriveEffectiveAvailability(input deriveInput) (bool, string, bool, bool) {
	limited := false
	if input.accessType == "authorized" {
		// authorizedBindingAvailability
		if input.boundGroupID == "" {
			return false, "binding_missing", true, limited
		}
		// authorizationAvailability（authorizationRuntimeBlockingStatus）
		if input.authorizationStatus != "" && input.authorizationStatus != "active" {
			return false, "authorization_unavailable", true, limited
		}
		if input.authorizationExpiresAt != "" {
			expired, err := isPastInstant(input.authorizationExpiresAt, input.nowMS)
			if err != nil {
				return false, "authorization_unavailable", true, limited
			}
			if expired {
				return false, "authorization_expired", true, limited
			}
		}
		// authorizationQuotaExceeded 分支未迁移（用量聚合读模型）。
		limited = true
		// sourceAccountAvailability
		if input.sourceID == "" || input.sourceStatus == "" {
			return false, "source_deleted", true, limited
		}
		if input.sourceErrorCode == "account_expired" {
			return false, "source_expired", true, limited
		}
		if input.sourceExpiresAt != "" {
			expired, err := isPastInstant(input.sourceExpiresAt, input.nowMS)
			if err == nil && expired {
				return false, "source_expired", true, limited
			}
		}
		switch input.sourceStatus {
		case "disabled":
			return false, "source_disabled", true, limited
		case "pending_test":
			return false, "source_pending_test", true, limited
		case "error":
			return false, "source_error", true, limited
		case "rate_limited":
			return false, "source_rate_limited", true, limited
		case "temporary_unavailable":
			return false, "source_temporary_unavailable", true, limited
		case "quality_isolated":
			return false, "source_quality_isolated", true, limited
		}
		if input.sourceCooldownUntil != "" {
			future, err := isFutureInstant(input.sourceCooldownUntil, input.nowMS)
			if err == nil && future {
				return false, "source_cooldown", true, limited
			}
		}
		if !input.sourceSchedulable {
			return false, "source_unschedulable", true, limited
		}
	}
	// instanceAccountAvailability
	if input.lastErrorCode == "account_expired" {
		return false, "instance_expired", true, limited
	}
	if input.expiresAt != "" {
		expired, err := isPastInstant(input.expiresAt, input.nowMS)
		if err != nil {
			panic(err)
		}
		if expired {
			return false, "instance_expired", true, limited
		}
	}
	switch input.status {
	case "disabled":
		return false, "instance_disabled", true, limited
	case "pending_test":
		return false, "instance_pending_test", true, limited
	case "error":
		return false, "instance_error", true, limited
	case "rate_limited":
		return false, "instance_rate_limited", true, limited
	case "temporary_unavailable":
		return false, "instance_temporary_unavailable", true, limited
	case "quality_isolated":
		return false, "instance_quality_isolated", true, limited
	}
	if input.cooldownUntil != "" {
		future, err := isFutureInstant(input.cooldownUntil, input.nowMS)
		if err != nil {
			panic(err)
		}
		if future {
			return false, "instance_cooldown", true, limited
		}
	}
	if !input.schedulable {
		return false, "instance_unschedulable", true, limited
	}
	// apiKeyPoolAvailability：全部 Key 不可用（runtime 状态缺省视为 active）。
	if s.allKeysUnavailable(input.credentials, input.apiKeyRuntime) {
		return false, "api_key_pool_unavailable", true, limited
	}
	// runtimeAvailability 分支（gateway 进程内运行态）未迁移。
	limited = limited || input.accessType == "authorized"
	return true, "", false, limited
}

func (s *Store) allKeysUnavailable(credentials map[string]any, runtime map[string]string) bool {
	if credentials == nil {
		return false
	}
	entries := s.AccountAPIKeyEntries(credentials)
	if len(entries) < 2 {
		return false
	}
	for _, entry := range entries {
		status, ok := runtime[entry.Fingerprint]
		if !ok || status == "active" || status == "" {
			return false
		}
	}
	return true
}

func isPastInstant(value string, nowMS int64) (bool, error) {
	timestamp, err := instantMS(value)
	if err != nil {
		return false, err
	}
	return timestamp <= nowMS, nil
}

func isFutureInstant(value string, nowMS int64) (bool, error) {
	timestamp, err := instantMS(value)
	if err != nil {
		return false, err
	}
	return timestamp > nowMS, nil
}

func instantMS(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("时间戳必须是带 Z 或数值 offset 的 RFC3339 时间：%s", value)
	}
	return parsed.UnixMilli(), nil
}

// ---- find_openai_account_for_group ----

// CandidateAccount 是 find_openai_account_for_group 的窄投影（含凭据）。
type CandidateAccount struct {
	accountquality.OpenAIAccountCandidate
	AccountOwnerSystemAccountID string
	CredentialSourceAccountID   string
	ProviderCode                string
	ProtocolCode                string
	ProtocolVersion             string
	// BaseURL 取自凭据 base_url（探针上游地址）。
	BaseURL     string
	Credentials map[string]any
	APIKeys     []string
	// SelectedAPIKey 为默认凭据（oauth 取 access_token，api_key 取首把 Key）。
	SelectedAPIKey string
	APIKeyEntries  []KeyEntry
}

// FindAccountForGroup 实现 accountquality.AccountReader（ignoreAvailability=true
// 路径：分组访问 + 绑定 + 账户行 + 来源账户 + 凭据解密）。
func (s *Store) FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*accountquality.OpenAIAccountCandidate, error) {
	candidate, err := s.LoadAccountForGroup(ctx, groupID, accountID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, nil
	}
	summary := candidate.OpenAIAccountCandidate
	return &summary, nil
}

// LoadAccountForGroup 返回完整候选。
func (s *Store) LoadAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*CandidateAccount, error) {
	groupOwner, providerCode, ok, err := s.resolveGroupAccess(ctx, groupID, systemAccountID)
	if err != nil || !ok {
		return nil, err
	}
	bindingQuery := fmt.Sprintf(`
    SELECT 1 FROM %s
    WHERE group_id = ? AND system_account_id = ? AND account_id = ? AND enabled = 1
    LIMIT 1
  `, s.table("group_accounts"))
	var one int
	if err := s.db.QueryRowContext(ctx, bindingQuery, groupID, groupOwner, accountID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	query := fmt.Sprintf(`
    SELECT a.id, a.system_account_id, a.name, a.type, a.status, a.provider_code,
      a.protocol_code, a.protocol_version, a.client_compatibility,
      a.config_revision, a.dispatch_revision, a.credentials_encrypted,
      a.authorization_instance_authorization_id, a.authorization_instance_source_account_id,
      a.authorization_instance_owner_system_account_id,
      src.id AS resource_account_id, src.provider_code AS resource_provider_code,
      src.protocol_code AS resource_protocol_code, src.protocol_version AS resource_protocol_version,
      src.type AS resource_type, src.status AS resource_status,
      src.credentials_encrypted AS resource_credentials_encrypted
    FROM %s a
    LEFT JOIN %s src ON src.id = a.authorization_instance_source_account_id AND src.deleted_at IS NULL
    WHERE a.id = ? AND a.provider_code = ? AND a.deleted_at IS NULL
    LIMIT 1
  `, s.table("accounts"), s.table("accounts"))
	var (
		id, systemID, name, accountType, status   sql.NullString
		provider, protocolCode, protocolVersion   sql.NullString
		clientCompatibility                       sql.NullString
		credentials                               sql.NullString
		authzID, sourceID, authzOwner             sql.NullString
		resourceAccountID, resourceProvider       sql.NullString
		resourceProtocol, resourceProtocolVersion sql.NullString
		resourceType, resourceStatus              sql.NullString
		resourceCredentials                       sql.NullString
		configRevision, dispatchRevision          sql.NullInt64
	)
	row := s.db.QueryRowContext(ctx, query, accountID, providerCode)
	if err := row.Scan(&id, &systemID, &name, &accountType, &status, &provider,
		&protocolCode, &protocolVersion, &clientCompatibility,
		&configRevision, &dispatchRevision, &credentials,
		&authzID, &sourceID, &authzOwner,
		&resourceAccountID, &resourceProvider,
		&resourceProtocol, &resourceProtocolVersion,
		&resourceType, &resourceStatus,
		&resourceCredentials); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	_ = clientCompatibility
	_ = configRevision
	// resolveOpenAIAccountAccess 窄投影。
	accessOwner := systemAccountID
	accountAccessType := ""
	switch {
	case authzID.String != "":
		if systemID.String != systemAccountID {
			return nil, nil
		}
		authorizationOwner, ok, err := s.activeAuthorizationOwnerByID(ctx, authzID.String, systemAccountID)
		if err != nil || !ok {
			return nil, err
		}
		accessOwner = authorizationOwner
		accountAccessType = "account_authorized"
	case systemID.String == systemAccountID:
		accountAccessType = "owner"
	default:
		return nil, nil
	}
	resourceTypeValue := resourceType.String
	if resourceTypeValue == "" {
		resourceTypeValue = accountType.String
	}
	resourceCredentialsText := resourceCredentials.String
	if resourceCredentialsText == "" {
		resourceCredentialsText = credentials.String
	}
	resourceProviderValue := resourceProvider.String
	if resourceProviderValue == "" {
		resourceProviderValue = provider.String
	}
	resourceProtocolValue := resourceProtocol.String
	if resourceProtocolValue == "" {
		resourceProtocolValue = protocolCode.String
	}
	resourceProtocolVersionValue := resourceProtocolVersion.String
	if resourceProtocolVersionValue == "" {
		resourceProtocolVersionValue = protocolVersion.String
	}
	credentialsMap, err := s.DecryptCredentials(resourceCredentialsText)
	if err != nil {
		return nil, nil
	}
	entries := s.AccountAPIKeyEntries(credentialsMap)
	selectedKey := ""
	if resourceTypeValue == "oauth" || resourceTypeValue == "google_oauth" {
		if token, _ := credentialsMap["access_token"].(string); token != "" {
			selectedKey = token
		} else if token, _ := credentialsMap["refresh_token"].(string); token != "" {
			selectedKey = token
		}
	} else if len(entries) > 0 {
		selectedKey = entries[0].Key
	}
	if selectedKey == "" {
		return nil, nil
	}
	apiKeys := make([]string, 0, len(entries))
	if resourceTypeValue == "api_key" {
		for _, entry := range entries {
			apiKeys = append(apiKeys, entry.Key)
		}
	}
	candidate := &CandidateAccount{
		OpenAIAccountCandidate: accountquality.OpenAIAccountCandidate{
			ID:                  id.String,
			Name:                name.String,
			Type:                resourceTypeValue,
			Status:              status.String,
			DispatchRevision:    dispatchRevision.Int64,
			HasDispatchRevision: dispatchRevision.Valid,
			QuotaRecoveryPolicy: mapField(credentialsMap, "quota_recovery_policy"),
		},
		AccountOwnerSystemAccountID: accessOwner,
		CredentialSourceAccountID:   resourceAccountID.String,
		ProviderCode:                resourceProviderValue,
		ProtocolCode:                resourceProtocolValue,
		ProtocolVersion:             resourceProtocolVersionValue,
		BaseURL:                     textCredential(credentialsMap, "base_url"),
		Credentials:                 credentialsMap,
		APIKeys:                     apiKeys,
		SelectedAPIKey:              selectedKey,
		APIKeyEntries:               entries,
	}
	_ = accountAccessType
	_ = resourceStatus
	_ = configRevision
	return candidate, nil
}

func mapField(credentials map[string]any, key string) map[string]any {
	if credentials == nil {
		return nil
	}
	if value, ok := credentials[key].(map[string]any); ok {
		return value
	}
	return nil
}

// resolveGroupAccess 等价 resolveGroupUsageAccessMetadata（owner + authorized 两分支）。
func (s *Store) resolveGroupAccess(ctx context.Context, groupID, systemAccountID string) (string, string, bool, error) {
	query := fmt.Sprintf(`
    SELECT system_account_id, provider_code, enabled
    FROM %s WHERE id = ? LIMIT 1
  `, s.table("groups"))
	var owner, providerCode sql.NullString
	var enabled any
	if err := s.db.QueryRowContext(ctx, query, groupID).Scan(&owner, &providerCode, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if owner.String == "" || providerCode.String == "" || !truthy(enabled) {
		return "", "", false, nil
	}
	if owner.String == systemAccountID {
		return owner.String, providerCode.String, true, nil
	}
	// 授权分组：active group authorization + 本地设置未被停用。
	authOwner, ok, err := s.activeGroupAuthorizationOwner(ctx, groupID, systemAccountID)
	if err != nil || !ok {
		return "", "", false, err
	}
	var localEnabled any
	localQuery := fmt.Sprintf(`
    SELECT enabled FROM %s
    WHERE authorization_id = ? AND system_account_id = ? AND group_id = ?
    LIMIT 1
  `, s.table("group_authorization_settings"))
	row := s.db.QueryRowContext(ctx, localQuery, authOwner.authorizationID, systemAccountID, groupID)
	if err := row.Scan(&localEnabled); err == nil && !truthy(localEnabled) {
		return "", "", false, nil
	}
	return owner.String, providerCode.String, true, nil
}

type authorizationRef struct {
	authorizationID string
	ownerID         string
}

func (s *Store) activeGroupAuthorizationOwner(ctx context.Context, groupID, granteeSystemAccountID string) (*authorizationRef, bool, error) {
	query := fmt.Sprintf(`
    SELECT id, resource_owner_system_account_id FROM %s
    WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ?
      AND status = 'active' AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1
  `, s.table("resource_authorizations"))
	var id, resourceOwner sql.NullString
	if err := s.db.QueryRowContext(ctx, query, groupID, granteeSystemAccountID, s.timeParam(s.now())).Scan(&id, &resourceOwner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &authorizationRef{authorizationID: id.String, ownerID: resourceOwner.String}, true, nil
}

func (s *Store) activeAuthorizationOwnerByID(ctx context.Context, authorizationID, granteeSystemAccountID string) (string, bool, error) {
	query := fmt.Sprintf(`
    SELECT resource_owner_system_account_id FROM %s
    WHERE id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1
  `, s.table("resource_authorizations"))
	var owner sql.NullString
	if err := s.db.QueryRowContext(ctx, query, authorizationID, granteeSystemAccountID, s.timeParam(s.now())).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return owner.String, true, nil
}

// HasAPIKeyEntry 实现 accountquality.AccountReader：keyFingerprint+apiKey 是否
// 仍在当前凭据池（等价 accountApiKeyEntries(...).find(...)）。
func (s *Store) HasAPIKeyEntry(ctx context.Context, candidate *accountquality.OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error) {
	if candidate == nil {
		return false, nil
	}
	full, err := s.LoadAccountForGroupFull(ctx, candidate.ID)
	if err != nil {
		return false, err
	}
	if full == nil {
		return false, nil
	}
	for _, entry := range full.APIKeyEntries {
		if entry.Fingerprint == fingerprint && entry.Key == apiKey {
			return true, nil
		}
	}
	return false, nil
}

// LoadAccountForGroupFull 按账户 ID 重载候选凭据（HasAPIKeyEntry 使用）。
func (s *Store) LoadAccountForGroupFull(ctx context.Context, accountID string) (*CandidateAccount, error) {
	query := fmt.Sprintf(`
    SELECT a.id, a.name, a.type, a.status, a.provider_code, a.protocol_code, a.protocol_version,
      a.config_revision, a.dispatch_revision, a.credentials_encrypted, a.system_account_id
    FROM %s a
    WHERE a.id = ? AND a.deleted_at IS NULL
    LIMIT 1
  `, s.table("accounts"))
	var (
		id, name, accountType, status           sql.NullString
		provider, protocolCode, protocolVersion sql.NullString
		credentials, systemID                   sql.NullString
		configRevision, dispatchRevision        sql.NullInt64
	)
	if err := s.db.QueryRowContext(ctx, query, accountID).Scan(&id, &name, &accountType, &status,
		&provider, &protocolCode, &protocolVersion,
		&configRevision, &dispatchRevision, &credentials, &systemID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	credentialsMap, err := s.DecryptCredentials(credentials.String)
	if err != nil {
		return nil, nil
	}
	entries := s.AccountAPIKeyEntries(credentialsMap)
	selectedKey := ""
	if accountType.String == "oauth" || accountType.String == "google_oauth" {
		if token, _ := credentialsMap["access_token"].(string); token != "" {
			selectedKey = token
		}
	} else if len(entries) > 0 {
		selectedKey = entries[0].Key
	}
	return &CandidateAccount{
		OpenAIAccountCandidate: accountquality.OpenAIAccountCandidate{
			ID:                  id.String,
			Name:                name.String,
			Type:                accountType.String,
			Status:              status.String,
			DispatchRevision:    dispatchRevision.Int64,
			HasDispatchRevision: dispatchRevision.Valid,
			QuotaRecoveryPolicy: mapField(credentialsMap, "quota_recovery_policy"),
		},
		AccountOwnerSystemAccountID: systemID.String,
		ProviderCode:                provider.String,
		ProtocolVersion:             protocolVersion.String,
		Credentials:                 credentialsMap,
		APIKeyEntries:               entries,
		SelectedAPIKey:              selectedKey,
	}, nil
}

// LoadProbeView 实现 accountprobe.CandidateSource：为一次探针组装完整视图。
func (s *Store) LoadProbeView(ctx context.Context, req accountquality.ProbeRequest) (*accountprobe.View, error) {
	account, err := s.LoadAccountForTest(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}
	systemAccountID := req.SystemAccountID
	if systemAccountID == "" {
		systemAccountID = account.OwnerSystemAccountID
	}
	if systemAccountID == "" {
		systemAccountID = account.SystemAccountID
	}
	candidate, err := s.LoadAccountForGroup(ctx, req.GroupID, req.AccountID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, nil
	}
	view := &accountprobe.View{
		AccountID:               account.ID,
		AccountName:             account.Name,
		Type:                    candidate.Type,
		Status:                  candidate.Status,
		ProviderCode:            candidate.ProviderCode,
		ProtocolCode:            candidate.ProtocolCode,
		ProtocolVersion:         candidate.ProtocolVersion,
		HealthCheckModel:        account.HealthCheckModel,
		HealthCheckEndpointMode: account.HealthCheckEndpointMode,
		SupportedModels:         account.SupportedModels,
		BaseURL:                 textCredential(candidate.Credentials, "base_url"),
		Credentials:             candidate.Credentials,
		SelectedAPIKey:          candidate.SelectedAPIKey,
		QuotaRecoveryPolicy:     candidate.QuotaRecoveryPolicy,
		NormalizeEndpointModes:  s.normalizedEndpointModes(candidate, account),
	}
	entries := make([]accountprobe.KeyEntry, 0, len(candidate.APIKeyEntries))
	for _, entry := range candidate.APIKeyEntries {
		entries = append(entries, accountprobe.KeyEntry{Key: entry.Key, Fingerprint: entry.Fingerprint, Index: entry.Index})
	}
	view.APIKeyEntries = entries
	if req.FixedAPIKey != "" {
		view.FixedKey = &accountprobe.KeyEntry{Key: req.FixedAPIKey, Fingerprint: req.FixedKeyFingerprint, Index: req.FixedKeyIndex}
		view.SelectedAPIKey = req.FixedAPIKey
	}
	return view, nil
}

// LoadAccountMetadataByIds 实现 accountquality.BusinessLookup
// （loadQualityAccountMetadataByIds 的移植：id -> system_account_id/provider_code，
// 仅返回存在且未删除的账户）。
func (s *Store) LoadAccountMetadataByIds(ctx context.Context, ids []string) (map[string]accountquality.AccountMetadata, error) {
	output := map[string]accountquality.AccountMetadata{}
	if len(ids) == 0 {
		return output, nil
	}
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	for start := 0; start < len(unique); start += 500 {
		end := start + 500
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for index, id := range chunk {
			placeholders[index] = "?"
			args[index] = id
		}
		query := "SELECT id, system_account_id, provider_code FROM " + s.table("accounts") +
			" WHERE id IN (" + strings.Join(placeholders, ", ") + ") AND deleted_at IS NULL"
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, systemID, providerCode sql.NullString
			if err := rows.Scan(&id, &systemID, &providerCode); err != nil {
				rows.Close()
				return nil, err
			}
			output[id.String] = accountquality.AccountMetadata{
				SystemAccountID: systemID.String,
				ProviderCode:    providerCode.String,
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return output, nil
}

func textCredential(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	if value, ok := credentials[key].(string); ok {
		return value
	}
	return ""
}

// normalizedEndpointModes 等价 normalizeGatewayEndpointModesForRuntime 的窄投影：
// credentials.supported_endpoint_modes 数组有效值过滤，空/无效回落协议默认。
func (s *Store) normalizedEndpointModes(candidate *CandidateAccount, account *AccountForTestView) map[accountprobe.EndpointMode]bool {
	modes := map[accountprobe.EndpointMode]bool{}
	addDefaults := func() {
		switch {
		case strings.EqualFold(strings.TrimSpace(account.ProtocolCode), "anthropic"):
			modes[accountprobe.ModeMessagesJSON] = true
			modes[accountprobe.ModeMessagesSSE] = true
		case strings.EqualFold(strings.TrimSpace(account.ProtocolCode), "gemini"):
			modes[accountprobe.ModeGenerateContentJSON] = true
			modes[accountprobe.ModeGenerateContentSSE] = true
			modes[accountprobe.ModeInteractionsJSON] = true
			modes[accountprobe.ModeInteractionsSSE] = true
		case candidate.Type == "oauth":
			modes[accountprobe.ModeResponsesJSON] = true
			modes[accountprobe.ModeResponsesSSE] = true
		default:
			modes[accountprobe.ModeChatJSON] = true
			modes[accountprobe.ModeChatSSE] = true
			modes[accountprobe.ModeResponsesJSON] = true
			modes[accountprobe.ModeResponsesSSE] = true
		}
	}
	list, ok := candidate.Credentials["supported_endpoint_modes"].([]any)
	if !ok {
		addDefaults()
		return modes
	}
	for _, item := range list {
		text, isText := item.(string)
		if !isText {
			continue
		}
		switch accountprobe.EndpointMode(text) {
		case accountprobe.ModeChatJSON, accountprobe.ModeChatSSE,
			accountprobe.ModeResponsesJSON, accountprobe.ModeResponsesSSE,
			accountprobe.ModeMessagesJSON, accountprobe.ModeMessagesSSE,
			accountprobe.ModeGenerateContentJSON, accountprobe.ModeGenerateContentSSE,
			accountprobe.ModeInteractionsJSON, accountprobe.ModeInteractionsSSE,
			accountprobe.ModeImagesJSON:
			modes[accountprobe.EndpointMode(text)] = true
		}
	}
	if len(modes) == 0 {
		addDefaults()
	}
	return modes
}
