package accounts

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountstest"
)

// REFACTOR-0005 阶段 A 桥接层：手动测试会话子域（accountstest）从根包拆出后，
// 门面在这里集中保留类型别名、Store 方法转发与自由函数转发，保证根包内既有
// 引用（含 test_dispatch_routes.go HTTP 面、w10a/w13a/w13g/w14b/w14l/w2 各波
// 测试）与外部 53 个消费文件零改动。
//
// 转发每次以 Store 的活字段构造子域 Service（无缓存指针），因此
// "clone := *store; clone.db = ..." 的故障注入克隆语义保持不变。

// ---- 门面别名（子域导出类型） ----

type (
	// AccountTestSession mirrors AccountTestSession.
	AccountTestSession = accountstest.AccountTestSession
	// AccountTestResult is the pass-through AccountTestResult payload.
	AccountTestResult = accountstest.AccountTestResult
	// AccountTestTask mirrors AccountTestTask.
	AccountTestTask = accountstest.AccountTestTask
	// TestDraftSnapshot mirrors AccountTestDraftSnapshot.
	TestDraftSnapshot = accountstest.TestDraftSnapshot
	// TestTaskCreateInput mirrors CreateAccountTestTaskInput.
	TestTaskCreateInput = accountstest.TestTaskCreateInput
	// TestSessionCancel mirrors the cancelAccountTestSession return shape.
	TestSessionCancel = accountstest.TestSessionCancel
	// ManualTestContext mirrors AccountManualTestOptionsContext.
	ManualTestContext = accountstest.ManualTestContext
	// ManualTestOption mirrors AccountManualTestOption.
	ManualTestOption = accountstest.ManualTestOption
	// ManualTestModelCapabilities mirrors AccountManualTestModelCapabilities.
	ManualTestModelCapabilities = accountstest.ManualTestModelCapabilities
	// ManualTestOptionsQuery mirrors normalizeAccountManualTestOptionsQuery output.
	ManualTestOptionsQuery = accountstest.ManualTestOptionsQuery
	// TestDispatchEffects is the manual-test dispatch port.
	TestDispatchEffects = accountstest.TestDispatchEffects
	// ManualTestModeSource carries the account fields the endpoint-mode
	// resolution consumes.
	ManualTestModeSource = accountstest.ManualTestModeSource
	// ManualTestContextRow mirrors the manual-test context row scan target.
	ManualTestContextRow = accountstest.ManualTestContextRow
	// TestSessionRow mirrors the account_test_sessions row scan target.
	TestSessionRow = accountstest.TestSessionRow
	// TestTaskRow mirrors the account_test_tasks join row scan target.
	TestTaskRow = accountstest.TestTaskRow
	// TestOptionRow mirrors ProviderModelOptionRow.
	TestOptionRow = accountstest.TestOptionRow
	// TestCatalogItem mirrors ProviderModelTestCatalogItem.
	TestCatalogItem = accountstest.TestCatalogItem
	// TestCatalogCandidate mirrors one catalog candidate of the merge.
	TestCatalogCandidate = accountstest.TestCatalogCandidate
	// ResolvedTestModelMapping mirrors the resolved upstream mapping.
	ResolvedTestModelMapping = accountstest.ResolvedTestModelMapping
	// MergedTestOption mirrors the merged option row.
	MergedTestOption = accountstest.MergedTestOption
)

// 根包历史私有名 → 子域类型别名（测试字面量沿用旧名）.
type (
	testSessionRow           = accountstest.TestSessionRow
	testTaskRow              = accountstest.TestTaskRow
	manualTestContextRow     = accountstest.ManualTestContextRow
	testOptionRow            = accountstest.TestOptionRow
	testCatalogItem          = accountstest.TestCatalogItem
	testCatalogCandidate     = accountstest.TestCatalogCandidate
	manualTestModeSource     = accountstest.ManualTestModeSource
	resolvedTestModelMapping = accountstest.ResolvedTestModelMapping
	mergedTestOption         = accountstest.MergedTestOption
)

// 会话/任务状态与目录 scope 常量（子域定义，门面别名）.
const (
	TestSessionRunning   = accountstest.TestSessionRunning
	TestSessionCompleted = accountstest.TestSessionCompleted
	TestSessionCanceled  = accountstest.TestSessionCanceled
	TestSessionExpired   = accountstest.TestSessionExpired

	TestTaskQueued   = accountstest.TestTaskQueued
	TestTaskRunning  = accountstest.TestTaskRunning
	TestTaskSuccess  = accountstest.TestTaskSuccess
	TestTaskFailed   = accountstest.TestTaskFailed
	TestTaskCanceled = accountstest.TestTaskCanceled

	catalogScopeBuiltIn  = accountstest.CatalogScopeBuiltIn
	catalogScopeGlobal   = accountstest.CatalogScopeGlobal
	catalogScopePersonal = accountstest.CatalogScopePersonal

	geminiOpenAIChatV1BetaProfile = accountstest.GeminiOpenAIChatV1BetaProfile
)

// ---- StoreBase 适配 + 子域 Service 构造（活字段，无缓存） ----

type storeBaseAdapter struct{ s *Store }

func (a storeBaseAdapter) DB() *sql.DB                { return a.s.db }
func (a storeBaseAdapter) PG() bool                   { return a.s.pg }
func (a storeBaseAdapter) Secret() string             { return a.s.secret }
func (a storeBaseAdapter) Now() time.Time             { return a.s.now() }
func (a storeBaseAdapter) NewID(prefix string) string { return a.s.newI(prefix) }
func (a storeBaseAdapter) Table(name string) string   { return a.s.table(name) }
func (a storeBaseAdapter) Bind(query string) string   { return a.s.bind(query) }
func (a storeBaseAdapter) BoolTrueLiteral() string    { return accountscore.BoolTrueLiteral(a.s.pg) }
func (a storeBaseAdapter) BoolFalseLiteral() string   { return accountscore.BoolFalseLiteral(a.s.pg) }

// testService builds the subdomain service over this store's live fields.
func (s *Store) testService() *accountstest.Service {
	return accountstest.New(storeBaseAdapter{s}, s.testEffects)
}

// ---- TestDispatchEffects 注入口（门面保留同名转发，cmd 三处消费零改动） ----

// SetTestDispatchEffects wires the dispatch port (composition-root handover;
// nil keeps the family self-contained).
func (s *Store) SetTestDispatchEffects(effects TestDispatchEffects) {
	s.testEffects = effects
}

// TestDispatchEffects exposes the wired port for composition-root
// effectiveness assertions (装配断线零容忍：组合根测试用它断言端口已接线).
func (s *Store) TestDispatchEffects() TestDispatchEffects {
	if s == nil {
		return nil
	}
	return s.testEffects
}

// SetTestDispatchEffects is the Deps-level alias so composition roots can set
// the field and let Mount wire it, mirroring the Authorized reader precedent.
func (d *Deps) wireTestEffects() {
	if d.TestDispatch != nil {
		d.Store.SetTestDispatchEffects(d.TestDispatch)
	}
}

func (s *Store) testEffectsOrNil() TestDispatchEffects {
	if s.testEffects == nil {
		slog.Debug("test dispatch effects port not wired; manual test dispatch stays unavailable")
	}
	return s.testEffects
}

// ---- Store 方法转发（手动测试会话子域） ----

func (s *Store) CreateTestSession(ctx context.Context, access AccessScope) (*AccountTestSession, error) {
	return s.testService().CreateTestSession(ctx, access)
}

func (s *Store) GetTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	return s.testService().GetTestSession(ctx, sessionID, access)
}

func (s *Store) GetTestSessionDetail(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, []AccountTestTask, error) {
	return s.testService().GetTestSessionDetail(ctx, sessionID, access)
}

func (s *Store) HeartbeatTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	return s.testService().HeartbeatTestSession(ctx, sessionID, access)
}

func (s *Store) CompleteTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	return s.testService().CompleteTestSession(ctx, sessionID, access)
}

func (s *Store) CancelTestSession(ctx context.Context, sessionID string, access *AccessScope, message string) (*TestSessionCancel, error) {
	return s.testService().CancelTestSession(ctx, sessionID, access, message)
}

func (s *Store) CreateTestTask(ctx context.Context, input TestTaskCreateInput) (*AccountTestTask, error) {
	return s.testService().CreateTestTask(ctx, input)
}

func (s *Store) GetTestTask(ctx context.Context, taskID string, access *AccessScope) (*AccountTestTask, error) {
	return s.testService().GetTestTask(ctx, taskID, access)
}

func (s *Store) ListTestTasks(ctx context.Context, ids []string, access *AccessScope) ([]AccountTestTask, error) {
	return s.testService().ListTestTasks(ctx, ids, access)
}

func (s *Store) CancelTestTask(ctx context.Context, taskID string, access *AccessScope) (*AccountTestTask, error) {
	return s.testService().CancelTestTask(ctx, taskID, access)
}

func (s *Store) FailTestTask(ctx context.Context, taskID string, message string) error {
	return s.testService().FailTestTask(ctx, taskID, message)
}

// ---- Store 方法转发（原子域私有读写，根包测试经由同名转发访问） ----

func (s *Store) cleanupExpiredTestTasks(ctx context.Context) error {
	return s.testService().CleanupExpiredTestTasks(ctx)
}

func (s *Store) listSessionTasks(ctx context.Context, q queryer, sessionID string, access *AccessScope) ([]AccountTestTask, error) {
	return s.testService().ListSessionTasks(ctx, q, sessionID, access)
}

func (s *Store) loadTestAccountModelMappings(ctx context.Context, q queryer, accountID, model string) ([]accountscore.ModelMapping, error) {
	return s.testService().LoadTestAccountModelMappings(ctx, q, accountID, model)
}

func (s *Store) findManualTestContextRow(ctx context.Context, accountID string, access *AccessScope, includeCredentials bool) (*manualTestContextRow, error) {
	return s.testService().FindManualTestContextRow(ctx, accountID, access, includeCredentials)
}

func (s *Store) testCatalogSourceCodes(ctx context.Context, providerCode string) ([]string, error) {
	return s.testService().TestCatalogSourceCodes(ctx, providerCode)
}

func (s *Store) protocolProviderCodes(ctx context.Context, protocolCode, protocolVersion string) ([]string, error) {
	return s.testService().ProtocolProviderCodes(ctx, protocolCode, protocolVersion)
}

func (s *Store) listBuiltInTestCatalogOptions(ctx context.Context, providerCodes []string, query ManualTestOptionsQuery) ([]testOptionRow, error) {
	return s.testService().ListBuiltInTestCatalogOptions(ctx, providerCodes, query)
}

func (s *Store) listCustomTestCatalogOptions(ctx context.Context, providerCodes []string, systemAccountID string, query ManualTestOptionsQuery) ([]testOptionRow, error) {
	return s.testService().ListCustomTestCatalogOptions(ctx, providerCodes, systemAccountID, query)
}

func (s *Store) findTestCatalogItem(ctx context.Context, providerCode, systemAccountID, model string) (*testCatalogItem, error) {
	return s.testService().FindTestCatalogItem(ctx, providerCode, systemAccountID, model)
}

func (s *Store) collectTestCatalogCandidates(ctx context.Context, builtInCodes, sourceCodes []string, model, availability string) ([]testCatalogCandidate, error) {
	return s.testService().CollectTestCatalogCandidates(ctx, builtInCodes, sourceCodes, model, availability)
}

// ---- Store 方法转发（test-options 面，test_dispatch_routes.go 消费） ----

func (s *Store) FindManualTestOptionsContext(ctx context.Context, accountID string, access *AccessScope) (*ManualTestContext, error) {
	return s.testService().FindManualTestOptionsContext(ctx, accountID, access)
}

func (s *Store) FindManualTestCapabilitiesContext(ctx context.Context, accountID, modelID string, access *AccessScope) (*ManualTestContext, error) {
	return s.testService().FindManualTestCapabilitiesContext(ctx, accountID, modelID, access)
}

func (s *Store) AccountManualTestOptions(ctx context.Context, account *ManualTestContext, query ManualTestOptionsQuery) ([]ManualTestOption, error) {
	return s.testService().AccountManualTestOptions(ctx, account, query)
}

func (s *Store) AccountManualTestModelCapabilities(ctx context.Context, account *ManualTestContext, modelInput string) (*ManualTestModelCapabilities, error) {
	return s.testService().AccountManualTestModelCapabilities(ctx, account, modelInput)
}

func (s *Store) ResolveAccountManualTestSelection(ctx context.Context, account *ManualTestContext, modelInput, testEndpointMode string) (model, resolvedMode string, err error) {
	return s.testService().ResolveAccountManualTestSelection(ctx, account, modelInput, testEndpointMode)
}

// ---- 自由函数转发（查询归一、端点能力解析、行投影与目录辅助） ----

func NormalizeManualTestOptionsQuery(query map[string][]string) (ManualTestOptionsQuery, string) {
	return accountstest.NormalizeManualTestOptionsQuery(query)
}

func firstQueryText(values []string) string {
	return accountstest.FirstQueryText(values)
}

func normalizedQueryTextList(groups ...[]string) []string {
	return accountstest.NormalizedQueryTextList(groups...)
}

func scopedOwnerID(access *AccessScope) string {
	return accountstest.ScopedOwnerID(access)
}

func supportedEndpointModesFromCredentials(credentials Credentials) []string {
	return accountstest.SupportedEndpointModesFromCredentials(credentials)
}

func normalizeOpenAIEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	return accountstest.NormalizeOpenAIEndpointModesForRuntime(value, toModeDefaultContext(defaults))
}

func normalizeAnthropicEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	return accountstest.NormalizeAnthropicEndpointModesForRuntime(value, toModeDefaultContext(defaults))
}

func normalizeGeminiEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	return accountstest.NormalizeGeminiEndpointModesForRuntime(value, toModeDefaultContext(defaults))
}

func normalizeHybridEndpointModesForRuntime(value []string) []string {
	return accountstest.NormalizeHybridEndpointModesForRuntime(value)
}

func accountManualTestEndpointModes(source manualTestModeSource) []string {
	return accountstest.AccountManualTestEndpointModes(source)
}

func accountTestEndpointModeOrder(source manualTestModeSource) []string {
	return accountstest.AccountTestEndpointModeOrder(source)
}

func isTestMappingSourceFamily(value string) bool {
	return accountstest.IsTestMappingSourceFamily(value)
}

func isOpenAIModelMappingRuntimeConversionSupported(mapping accountscore.ModelMapping, source manualTestModeSource) bool {
	return accountstest.IsOpenAIModelMappingRuntimeConversionSupported(mapping, source)
}

func resolveTestAccountModelMapping(source manualTestModeSource, model, sourceFamily string) *resolvedTestModelMapping {
	return accountstest.ResolveTestAccountModelMapping(source, model, sourceFamily)
}

func dedupeTestStrings(values []string) []string {
	return accountstest.DedupeTestStrings(values)
}

func normalizeTestProviderCodeList(codes []string) []string {
	return accountstest.NormalizeTestProviderCodeList(codes)
}

func testCatalogBuiltInSourceCodes(providerCode string, sourceCodes []string) []string {
	return accountstest.TestCatalogBuiltInSourceCodes(providerCode, sourceCodes)
}

func testCatalogScopePriority(scope string) int {
	return accountstest.TestCatalogScopePriority(scope)
}

func testCatalogReleaseDate(value string) string {
	return accountstest.TestCatalogReleaseDate(value)
}

func mergeTestOptionRows(rows []testOptionRow, query ManualTestOptionsQuery) []mergedTestOption {
	return accountstest.MergeTestOptionRows(rows, query)
}

func optionScopePriorityValue(scope string) int {
	return accountstest.OptionScopePriorityValue(scope)
}

func normalizedOptionReleaseDate(value string) string {
	return accountstest.NormalizedOptionReleaseDate(value)
}

func endpointModeProtocolFamily(mode string) (string, error) {
	return accountstest.EndpointModeProtocolFamily(mode)
}

func isAccountManualTestModel(item testCatalogItem, source manualTestModeSource) bool {
	return accountstest.IsAccountManualTestModel(item, source)
}

func hasEnabledTestModelMapping(mappings []accountscore.ModelMapping, model string) bool {
	return accountstest.HasEnabledTestModelMapping(mappings, model)
}

func testParseJSONArray(value sql.NullString) []string {
	return accountstest.TestParseJSONArray(value)
}

func testQueuedDeadlineAt(queuedAt string) string {
	return accountstest.TestQueuedDeadlineAt(queuedAt)
}

func testTaskStatus(value string) string {
	return accountstest.TestTaskStatus(value)
}

func testSessionStatus(value string) string {
	return accountstest.TestSessionStatus(value)
}

func testEndpointModeOrNull(value sql.NullString) *string {
	return accountstest.TestEndpointModeOrNull(value)
}

func testOptionalTimestamp(value sql.NullString) *string {
	return accountstest.TestOptionalTimestamp(value)
}

func testTrimmedOrNull(value string) any {
	return accountstest.TestTrimmedOrNull(value)
}

func canReadTestTask(requestSystemAccountID string, requestFilterID sql.NullString, access *AccessScope) bool {
	return accountstest.CanReadTestTask(requestSystemAccountID, requestFilterID, access)
}

func canReadTestSession(requestSystemAccountID string, requestFilterID sql.NullString, access *AccessScope) bool {
	return accountstest.CanReadTestSession(requestSystemAccountID, requestFilterID, access)
}

func testRequestRole(access AccessScope) string {
	return accountstest.TestRequestRole(access)
}

func testSessionCancelReason(row *testSessionRow) string {
	return accountstest.SessionCancelReason(row)
}

// testTodayText renders the SQL "today" literal（子域内为 Service 方法，门面
// 保留原自由函数形态）.
func testTodayText(s *Store) string {
	return s.testService().TestTodayText()
}
