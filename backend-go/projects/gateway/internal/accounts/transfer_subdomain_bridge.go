package accounts

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountstransfer"
)

// REFACTOR-0005 阶段 C 桥接层：导入导出与批量编辑子域（accountstransfer）
// 从根包拆出后，门面在这里集中保留类型别名、历史私有名别名、Store 方法转发
// 与自由函数转发，保证根包内既有引用（m09_routes.go HTTP 面、routes.go 错误
// 识别、invalidation.go/write.go/m11_authorized_dispatch.go 跨域写路径、
// patch.go/clone.go 小工具、w2/w13a/w14b/w14l 各波测试）与外部消费文件零改动。
//
// 依赖方向：accountstransfer 只 import accountscore；对门面能力的需要经
// Deps 函数端口注入。transfer→Create 的反向依赖按设计 v2 经 AccountWriter
// 窄写接口收敛（transferWriterAdapter 投影 CreateResult 的 id/status）。
//
// 转发每次以 Store 的活字段构造子域 Service（无缓存指针），因此
// "clone := *store; clone.db = ..." 的故障注入克隆语义保持不变。

// ---- 门面别名（子域导出类型，外部消费面不变） ----

type (
	// ImportOptions mirrors the import options payload (M09 preview/execute).
	ImportOptions = accountstransfer.ImportOptions
	// ImportAccountsSummary mirrors the accounts section summary.
	ImportAccountsSummary = accountstransfer.ImportAccountsSummary
	// ImportProxiesSummary mirrors the proxies section summary.
	ImportProxiesSummary = accountstransfer.ImportProxiesSummary
	// ImportGroupsSummary mirrors the groups section summary.
	ImportGroupsSummary = accountstransfer.ImportGroupsSummary
	// ImportSummary mirrors the whole-document import summary.
	ImportSummary = accountstransfer.ImportSummary
	// ImportItem mirrors one import plan row.
	ImportItem = accountstransfer.ImportItem
	// ImportProxyItem mirrors one import proxy plan row.
	ImportProxyItem = accountstransfer.ImportProxyItem
	// ImportResult mirrors the M09 import preview/execute result.
	ImportResult = accountstransfer.ImportResult
	// ImportRequest mirrors the parsed import request body.
	ImportRequest = accountstransfer.ImportRequest
	// ImportSourceSummary mirrors the per-source adapter summary.
	ImportSourceSummary = accountstransfer.ImportSourceSummary
	// ExportProxy mirrors one exported proxy profile.
	ExportProxy = accountstransfer.ExportProxy
	// ExportAccount mirrors one exported account row.
	ExportAccount = accountstransfer.ExportAccount
	// ExportDocument mirrors the export document envelope.
	ExportDocument = accountstransfer.ExportDocument
	// ExportSummary mirrors the export document summary.
	ExportSummary = accountstransfer.ExportSummary
	// ExportResult mirrors the M09 export result.
	ExportResult = accountstransfer.ExportResult
	// ExportOptions mirrors the export execution options.
	ExportOptions = accountstransfer.ExportOptions
	// BatchUpdateField mirrors one batch edit field update.
	BatchUpdateField = accountstransfer.BatchUpdateField
	// BatchUpdateInput mirrors the batch edit request payload.
	BatchUpdateInput = accountstransfer.BatchUpdateInput
	// BatchUpdateItem mirrors one batch edit result row.
	BatchUpdateItem = accountstransfer.BatchUpdateItem
	// BatchUpdateResult mirrors the batch edit result.
	BatchUpdateResult = accountstransfer.BatchUpdateResult
	// BatchUpdateTarget mirrors one batch edit target.
	BatchUpdateTarget = accountstransfer.BatchUpdateTarget
	// BatchEditContextItem mirrors the batch edit context projection.
	BatchEditContextItem = accountstransfer.BatchEditContextItem
	// CacheInvalidator is the nil-safe post-commit invalidation port shared
	// with the batch edit side-effect chain (Store.SetCacheInvalidator).
	CacheInvalidator = accountstransfer.CacheInvalidator
)

// 根包历史私有名 → 子域类型别名（跨域代码与测试字面量沿用旧名）。
type (
	importRequest             = accountstransfer.ImportRequest
	exportBody                = accountstransfer.ExportBody
	exportAccountRow          = accountstransfer.ExportAccountRow
	importPlan                = accountstransfer.ImportPlan
	importGroupCreatePlan     = accountstransfer.ImportGroupCreatePlan
	normalizedImportAccount   = accountstransfer.NormalizedImportAccount
	adapterState              = accountstransfer.AdapterState
	batchAccessError          = accountstransfer.BatchAccessError
	batchVersionConflictError = accountstransfer.BatchVersionConflictError
	batchDispatchRevision     = accountstransfer.BatchDispatchRevision
	importGroupReference      = accountstransfer.GroupReference
	importProvider            = accountstransfer.ImportProvider
	importProxyOption         = accountstransfer.ImportProxyOption
)

// 根包历史私有常量 → 子域常量（测试沿用旧名）.
const (
	importSourceNative           = accountstransfer.ImportSourceNative
	importSourceSub2Api          = accountstransfer.ImportSourceSub2Api
	importSourceNewAPI           = accountstransfer.ImportSourceNewAPI
	importSourceCPA              = accountstransfer.ImportSourceCPA
	importSourceOneAPI           = accountstransfer.ImportSourceOneAPI
	defaultOpenAIBaseURL         = accountstransfer.DefaultOpenAIBaseURL
	accountExportMaxAccounts     = accountstransfer.AccountExportMaxAccounts
	accountExportListPageSize    = accountstransfer.AccountExportListPageSize
	accountImportProtocolType    = accountstransfer.AccountImportProtocolType
	accountImportProtocolVersion = accountstransfer.AccountImportProtocolVersion
	importActionCreate           = accountstransfer.ImportActionCreate
	importActionReuse            = accountstransfer.ImportActionReuse
	importActionSkip             = accountstransfer.ImportActionSkip
	importActionFailed           = accountstransfer.ImportActionFailed
	maxImportedAccounts          = accountstransfer.MaxImportedAccounts
	maxImportedProxies           = accountstransfer.MaxImportedProxies
	gptOpenAIV1ProfileID         = accountstransfer.GptOpenAIV1ProfileID
	openAICompatibleProvider     = accountstransfer.OpenAICompatibleProvider
	openAICompatibleProfileID    = accountstransfer.OpenAICompatibleProfileID
	batchAccessDefaultMessage    = accountstransfer.BatchAccessDefaultMessage
	batchSameScopeMessage        = accountstransfer.BatchSameScopeMessage
	batchRangeMessage            = accountstransfer.BatchRangeMessage
	batchDuplicateMessage        = accountstransfer.BatchDuplicateMessage
	batchFieldsPrompt            = accountstransfer.BatchFieldsPrompt
	// gptVendorCode mirrors accountscore.GptVendorCode (facade-wide shared
	// constant formerly carried by import_source.go).
	gptVendorCode = accountscore.GptVendorCode
)

// ---- 子域 Service 构造（活字段，克隆语义保持） ----

// transferWriterAdapter is the AccountWriter narrow write port adapter over
// the live Store (REFACTOR-0005 阶段 C：transfer→Create 经窄写接口收敛).
type transferWriterAdapter struct{ s *Store }

func (a transferWriterAdapter) CreateAccount(ctx context.Context, input accountscore.CreateInput, access accountscore.AccessScope) (string, string, error) {
	created, err := a.s.Create(ctx, input, access)
	if err != nil {
		return "", "", err
	}
	return created.ID, created.Status, nil
}

func (a transferWriterAdapter) ReplaceAccountSupportedModels(ctx context.Context, q accountscore.Queryer, accountID, providerCode string, models []string, nowISO string) error {
	return a.s.replaceAccountSupportedModels(ctx, q, accountID, providerCode, models, nowISO)
}

func (a transferWriterAdapter) ReplaceAccountModelMappings(ctx context.Context, q accountscore.Queryer, accountID, providerCode string, mappings []accountscore.ModelMapping, nowISO string) error {
	return a.s.replaceAccountModelMappings(ctx, q, accountID, providerCode, mappings, nowISO)
}

func (a transferWriterAdapter) ReplaceTags(ctx context.Context, q accountscore.Queryer, accountID, systemAccountID string, tagNames []string, nowISO string) error {
	_, err := a.s.replaceAccountTags(ctx, q, accountID, systemAccountID, tagNames, nowISO)
	return err
}

// transferPredicateInput projects the accountscore predicate into the facade
// protocolPredicateInput (field names are identical; visibility differs).
func transferPredicateInput(predicate accountscore.ProtocolPredicate) protocolPredicateInput {
	return protocolPredicateInput{
		providerCode:              predicate.ProviderCode,
		protocolCode:              predicate.ProtocolCode,
		protocolVersion:           predicate.ProtocolVersion,
		providerProtocolProfileID: predicate.ProviderProtocolProfileID,
	}
}

// transferProfileRef projects the accountscore predicate into the facade
// protocolProfileRef for normalizeOpenAIAccountClientCompatibility.
func transferProfileRef(predicate accountscore.ProtocolPredicate) protocolProfileRef {
	return protocolProfileRef{
		ProviderCode:              predicate.ProviderCode,
		ProtocolCode:              predicate.ProtocolCode,
		ProtocolVersion:           predicate.ProtocolVersion,
		ProviderProtocolProfileID: predicate.ProviderProtocolProfileID,
	}
}

// transferService builds the import/export & batch-edit subdomain service over
// the live Store fields (each call re-derives the ports, so composition-root
// tests that clone a Store and swap fields keep observing the clone).
func (s *Store) transferService() *accountstransfer.Service {
	return accountstransfer.New(storeBaseAdapter{s}, accountstransfer.Deps{
		Writer: transferWriterAdapter{s},
		ListExportPage: func(ctx context.Context, access accountscore.AccessScope, options accountscore.ListOptions) ([]string, bool, error) {
			page, err := s.ListPage(ctx, access, options)
			if err != nil {
				return nil, false, err
			}
			ids := make([]string, 0, len(page.Items))
			for i := range page.Items {
				ids = append(ids, page.Items[i].ID)
			}
			return ids, page.HasMore, nil
		},
		AssertSupportedModelsInProviderCatalog: func(ctx context.Context, q accountscore.Queryer, models []string, providerCode, systemAccountID string, predicate accountscore.ProtocolPredicate) error {
			return s.assertAccountSupportedModelsInProviderCatalog(ctx, q, models, providerCode, systemAccountID, transferPredicateInput(predicate))
		},
		AssertModelMappingsInProviderCatalog: func(ctx context.Context, q accountscore.Queryer, providerCode, systemAccountID string, predicate accountscore.ProtocolPredicate, mappings []accountscore.ModelMapping, supportedEndpointModes []string) error {
			return s.assertAccountModelMappingsInProviderCatalog(ctx, q, providerCode, systemAccountID, transferPredicateInput(predicate), mappings, supportedEndpointModes)
		},
		AssertGptRequestOverridesSupported: func(ctx context.Context, providerCode, accountType string, credentials accountscore.Credentials, supportedModels []string, systemAccountID string) error {
			return s.assertAccountGptRequestOverridesSupported(ctx, accountGptRequestOverridesInput{
				ProviderCode:    providerCode,
				AccountType:     accountType,
				Credentials:     credentials,
				SupportedModels: supportedModels,
				SystemAccountID: systemAccountID,
			})
		},
		NormalizeCredentialsForWrite: NormalizeAccountCredentialsForWrite,
		NormalizeOpenAIAccountClientCompatibility: func(providerCode, accountType, value string, predicate accountscore.ProtocolPredicate) (string, error) {
			return normalizeOpenAIAccountClientCompatibility(providerCode, accountType, value, transferProfileRef(predicate))
		},
		ResolveHealthCheckEndpointMode: resolveHealthCheckEndpointMode,
		AssertEndpointModesCompatible: func(providerCode, accountType, clientCompatibility string, predicate accountscore.ProtocolPredicate, modes []string) error {
			return assertEndpointModesCompatible(providerCode, accountType, clientCompatibility, transferPredicateInput(predicate), modes)
		},
		NormalizeErrorHandlingRules:      normalizeAccountErrorHandlingRules,
		NormalizeResponseInspectionRules: normalizeAccountResponseInspectionRules,
		NormalizeTagNames:                normalizeAccountTagNamesInput,
		NormalizeSupportedModels:         normalizeSupportedModelsInput,
		AssertSupportedModelsRequired:    assertSupportedModelsRequired,
		AssertSafeUpstreamBaseURL:        assertSafeUpstreamBaseURL,
		StatusQueryValue:                 statusQueryValue,
		SchedulableQueryValue:            schedulableQueryValue,
		NewResourceID:                    newID,
		NormalizeSchedule:                NormalizeSchedule,
		ParseScheduleJSON:                ParseScheduleJSON,
		ScheduleJSON:                     ScheduleJSON,
		ScheduleStatus:                   ScheduleStatus,
		NextScheduleCheckAt:              NextScheduleCheckAt,
		CacheInvalidator:                 s.invalidator,
	})
}

// ---- Store 方法转发（导入导出 + 批量编辑子域） ----

// SetCacheInvalidator wires the post-commit invalidation channels (compose
// root handover; nil port keeps the batch self-contained).
func (s *Store) SetCacheInvalidator(invalidator CacheInvalidator) {
	s.invalidator = invalidator
}

// PreviewImport mirrors Store.PreviewImport (M09 preview pipeline).
func (s *Store) PreviewImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access AccessScope) (*ImportResult, error) {
	return s.transferService().PreviewImport(ctx, data, sourceMode, options, access)
}

// ExecuteImport mirrors Store.ExecuteImport (M09 confirm pipeline).
func (s *Store) ExecuteImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access AccessScope) (*ImportResult, error) {
	return s.transferService().ExecuteImport(ctx, data, sourceMode, options, access)
}

// ExportAccounts mirrors Store.ExportAccounts (M09 export document builder).
func (s *Store) ExportAccounts(ctx context.Context, options ExportOptions, access AccessScope) (*ExportResult, error) {
	return s.transferService().ExportAccounts(ctx, options, access)
}

// CollectExportIDs mirrors Store.CollectExportIDs (filter-driven export ids).
func (s *Store) CollectExportIDs(ctx context.Context, filters map[string]any, access AccessScope) ([]string, error) {
	return s.transferService().CollectExportIDs(ctx, filters, access)
}

// BatchUpdate mirrors Store.BatchUpdate (M09 batch edit executor).
func (s *Store) BatchUpdate(ctx context.Context, input BatchUpdateInput, access AccessScope) (*BatchUpdateResult, error) {
	return s.transferService().BatchUpdate(ctx, input, access)
}

// LoadBatchEditContext mirrors Store.LoadBatchEditContext (batch edit form).
func (s *Store) LoadBatchEditContext(ctx context.Context, accountIDs []string, fields []string, access AccessScope) ([]BatchEditContextItem, error) {
	return s.transferService().LoadBatchEditContext(ctx, accountIDs, fields, access)
}

// advanceBatchDispatchRevisionFamily advances the dispatch revision family
// after a committed write (write.go/invalidation.go cross-domain call site).
func (s *Store) advanceBatchDispatchRevisionFamily(ctx context.Context, q queryer, in batchDispatchRevision) error {
	return s.transferService().AdvanceBatchDispatchRevisionFamily(ctx, q, in)
}

// advanceBatchDispatchRevision advances one account dispatch revision
// (m11_authorized_dispatch.go + balance/reset bridge ports).
func (s *Store) advanceBatchDispatchRevision(ctx context.Context, q queryer, accountID, transitionID string, nowMS int64) error {
	return s.transferService().AdvanceBatchDispatchRevision(ctx, q, accountID, transitionID, nowMS)
}

// markBatchGroupStatsDirty flags the affected groups' stats rows
// (invalidation.go post-commit chain).
func (s *Store) markBatchGroupStatsDirty(ctx context.Context, accountIDs []string, reason string) error {
	return s.transferService().MarkBatchGroupStatsDirty(ctx, accountIDs, reason)
}

// forUpdate renders the dialect FOR UPDATE suffix (write.go/m11 lock sites).
func (s *Store) forUpdate() string { return s.transferService().ForUpdate() }

// loadImportProviders mirrors the provider catalog read behind the import plan
// (w14b scan fault injection keeps calling it on the Store).
func (s *Store) loadImportProviders(ctx context.Context) (map[string]*importProvider, error) {
	return s.transferService().LoadImportProviders(ctx)
}

// findImportGroupByID mirrors the scoped group lookup (w14b fault injection).
func (s *Store) findImportGroupByID(ctx context.Context, groupID string, access AccessScope) (*importGroupReference, error) {
	return s.transferService().FindImportGroupByID(ctx, groupID, access)
}

// findImportProxyByID mirrors the proxy id lookup (w14b fault injection).
func (s *Store) findImportProxyByID(ctx context.Context, id string) (*importProxyOption, error) {
	return s.transferService().FindImportProxyByID(ctx, id)
}

// createImportGroup mirrors the import group creator (w14b fault injection).
func (s *Store) createImportGroup(ctx context.Context, group importGroupCreatePlan, ownerID, nowISO string) (string, bool, error) {
	return s.transferService().CreateImportGroup(ctx, group, ownerID, nowISO)
}

// loadBatchTags / loadBatchSupportedModels / loadBatchModelMappings mirror the
// batch collection reads (w14b/w14l fault injection keeps them on the Store).
func (s *Store) loadBatchTags(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	return s.transferService().LoadBatchTags(ctx, q, ids)
}

func (s *Store) loadBatchSupportedModels(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	return s.transferService().LoadBatchSupportedModels(ctx, q, ids)
}

func (s *Store) loadBatchModelMappings(ctx context.Context, q queryer, ids []string) (map[string][]accountscore.ModelMapping, error) {
	return s.transferService().LoadBatchModelMappings(ctx, q, ids)
}

// ---- 自由函数转发（m09_routes.go 解析面 + 跨域小工具 + 测试沿用旧名） ----

func adaptImportSource(data any, mode string) (any, ImportSourceSummary) {
	return accountstransfer.AdaptImportSource(data, mode, assertSafeUpstreamBaseURL)
}

func adaptChannelSource(input any, mode string, state *adapterState) {
	accountstransfer.SetAdapterStateUpstreamPolicy(state, assertSafeUpstreamBaseURL)
	accountstransfer.AdaptChannelSource(input, mode, state)
}

func adaptCLIProxyAPI(input any, state *adapterState) {
	accountstransfer.SetAdapterStateUpstreamPolicy(state, assertSafeUpstreamBaseURL)
	accountstransfer.AdaptCLIProxyAPI(input, state)
}

func parseSourceJSON(value any, label string) (any, error) {
	return accountstransfer.ParseSourceJSON(value, label)
}

func emptySourceSummary(mode string) ImportSourceSummary {
	return accountstransfer.EmptySourceSummary(mode)
}

func sourceLabel(mode string) string { return accountstransfer.SourceLabel(mode) }

func normalizeYAMLValue(value any) any { return accountstransfer.NormalizeYAMLValue(value) }

func yamlKeyString(key any) string { return accountstransfer.YamlKeyString(key) }

func parseCpaInput(value any, state *adapterState) (any, bool) {
	accountstransfer.SetAdapterStateUpstreamPolicy(state, assertSafeUpstreamBaseURL)
	return accountstransfer.ParseCpaInput(value, state)
}

func parseImportBody(body map[string]any) (importRequest, bool) {
	return accountstransfer.ParseImportBody(body)
}

func parseExportBody(body map[string]any) (exportBody, bool) {
	return accountstransfer.ParseExportBody(body)
}

func batchUpdateBody(body map[string]any) (BatchUpdateInput, string) {
	return accountstransfer.BatchUpdateBody(body)
}

func batchEditContextBody(body map[string]any) ([]string, []string, string) {
	return accountstransfer.BatchEditContextBody(body)
}

var batchUpdateFields = accountstransfer.BatchUpdateFields

func validateBatchUpdateValue(field string, value any) string {
	return accountstransfer.ValidateBatchUpdateValue(field, value)
}

func assertBatchTargets(targets []BatchUpdateTarget) error {
	return accountstransfer.AssertBatchTargets(targets)
}

func applyNullableCredentialOverride(credentials Credentials, updates map[string]BatchUpdateField, updateKey, credentialKey string) {
	accountstransfer.ApplyNullableCredentialOverride(credentials, updates, updateKey, credentialKey)
}

func normalizeBatchModelMappings(value any) ([]accountscore.ModelMapping, error) {
	return accountstransfer.NormalizeBatchModelMappings(value)
}

func batchStatusForcesSchedulableOff(status string) bool {
	return accountstransfer.BatchStatusForcesSchedulableOff(status)
}

func batchFamilyDispatchTransitionID(transitionID, accountID string) string {
	return accountstransfer.BatchFamilyDispatchTransitionID(transitionID, accountID)
}

func batchCircuitEventID() (string, error) { return accountstransfer.BatchCircuitEventID() }

func accountScheduleError(err error) error { return accountstransfer.AccountScheduleError(err) }

func assertMappingUpstreamsAllowed(mappings []accountscore.ModelMapping, supportedModels []string) error {
	return accountstransfer.AssertMappingUpstreamsAllowed(mappings, supportedModels)
}

func boolPtr(value bool) *bool { return accountstransfer.BoolPtr(value) }

func boolValue(value any, fallback bool) bool { return accountstransfer.BoolValue(value, fallback) }

func intValue(value any, fallback int) int { return accountstransfer.IntValue(value, fallback) }

func jsonValueDeepEqual(left, right any) bool {
	return accountstransfer.JSONValueDeepEqual(left, right)
}

func marshalJSONValue(value any) string { return accountstransfer.MarshalJSONValue(value) }

func storedEndpointModes(value any) []string { return accountstransfer.StoredEndpointModes(value) }

func nullableTextEqual(left, right *string) bool {
	return accountstransfer.NullableTextEqual(left, right)
}

func modelMappingsEqual(left, right []accountscore.ModelMapping) bool {
	return accountstransfer.ModelMappingsEqual(left, right)
}

func unorderedStringListEqual(left, right []string) bool {
	return accountstransfer.UnorderedStringListEqual(left, right)
}

func strPtrOrNil(value string) *string { return accountstransfer.StrPtrOrNil(value) }

func textPointerOrNil(value string) *string { return accountstransfer.TextPointerOrNil(value) }

func exportCredentials(accountType string, credentials Credentials) Credentials {
	return accountstransfer.ExportCredentials(accountType, credentials)
}

func normalizeExportAccountIDs(values []string) []string {
	return accountstransfer.NormalizeExportAccountIDs(values)
}

func exportAccountStatus(row *exportAccountRow) string {
	return accountstransfer.ExportAccountStatus(row)
}

func orderedKeys(c Credentials) []string { return accountstransfer.OrderedKeys(c) }

func textValueOrJoin(value any) string { return accountstransfer.TextValueOrJoin(value) }

func textListOrSingle(value any) []string { return accountstransfer.TextListOrSingle(value) }

func importCreateInput(source *normalizedImportAccount, groupID, proxyProfileID string) accountscore.CreateInput {
	return accountstransfer.ImportCreateInput(source, groupID, proxyProfileID)
}

func importOptionalTextField(record map[string]any, key, label string, messages *[]string) string {
	return accountstransfer.ImportOptionalTextField(record, key, label, messages)
}

func importOptionalBooleanField(record map[string]any, key, label string, messages *[]string) *bool {
	return accountstransfer.ImportOptionalBooleanField(record, key, label, messages)
}

func importOptionalPositiveIntegerField(record map[string]any, key, label string, messages *[]string) int {
	return accountstransfer.ImportOptionalPositiveIntegerField(record, key, label, messages)
}

func importOptionalNonNegativeIntegerField(record map[string]any, key, label string, messages *[]string) int {
	return accountstransfer.ImportOptionalNonNegativeIntegerField(record, key, label, messages)
}

func importOptionalStringArrayField(record map[string]any, key, label string, messages *[]string) []string {
	return accountstransfer.ImportOptionalStringArrayField(record, key, label, messages)
}

func importOptionalHealthCheckEndpointModeField(record map[string]any, key, label string, messages *[]string) string {
	return accountstransfer.ImportOptionalHealthCheckEndpointModeField(record, key, label, messages)
}

func importOptionalTagsField(record map[string]any, key, label string, messages *[]string) []string {
	return accountstransfer.ImportOptionalTagsField(record, key, label, messages)
}

func importOptionalDateTimeField(record map[string]any, key, label string, messages *[]string) string {
	return accountstransfer.ImportOptionalDateTimeField(record, key, label, messages)
}

func importModelMappingsField(record map[string]any, key, label string, messages *[]string) []accountscore.ModelMapping {
	return accountstransfer.ImportModelMappingsField(record, key, label, messages)
}

func importMappingText(value any) string { return accountstransfer.ImportMappingText(value) }

func importMappingSourceFamily(value any) string {
	return accountstransfer.ImportMappingSourceFamily(value)
}

func importMappingUpstreamFamily(value any) string {
	return accountstransfer.ImportMappingUpstreamFamily(value)
}

func normalizeImportStatus(value any) string { return accountstransfer.NormalizeImportStatus(value) }

func normalizeImportProxyType(value any) string {
	return accountstransfer.NormalizeImportProxyType(value)
}

func duplicateProxyNameError(err error, name string) error {
	return accountstransfer.DuplicateProxyNameError(err, name)
}

func duplicateImportGroupNameError(err error, name string) error {
	return accountstransfer.DuplicateImportGroupNameError(err, name)
}

func planSkipDuplicates(plan *importPlan) bool { return accountstransfer.PlanSkipDuplicates(plan) }

// safeSourceBaseURL forwards the upstream base-URL adapter hook. 根包原版
// 恒用包级 assertSafeUpstreamBaseURL（无视 state 既有策略），这里经
// SetAdapterStateUpstreamPolicy 恢复同一语义，保证测试直达路径与生产
// AdaptImportSource 注入路径行为一致。
func safeSourceBaseURL(value string, state *adapterState) bool {
	accountstransfer.SetAdapterStateUpstreamPolicy(state, assertSafeUpstreamBaseURL)
	return accountstransfer.SafeSourceBaseURL(value, state)
}

// exportFiltersOptions forwards the export list-options builder; the shared
// service only normalizes values (no live Store state).
func exportFiltersOptions(filters map[string]any, page int) ListOptions {
	return (&Store{}).transferService().ExportFiltersOptions(filters, page)
}
