package mockdata

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// seedBusiness 是 business 库的造数域：系统账户与配套用户、AI 账户、分组、
// 路由策略与分组绑定、API Key（含本地网关明文 Key）、授权与授权来源、团队、
// 代理、公告、外部来源系统、响应检查策略、自定义模型目录、账户测试任务与
// 模型检测题库。
//
// 范围与覆盖点见 docs/functions/Mockdata造数设计.md「数据边界」；本文件只放
// 该域函数与它的私有辅助，其他域不得在此写入。
//
// 样本清单移植自 Node 归档实现
// migration-backup-1/node/final-archive/backend/src/scripts/maintenance/mockdata/
// （business/{foundation,extras,oidc-provider}.ts 与 core/{accounts,api-keys,
// authorizations,teams,group-writes,availability-schedules,quota-limits}.ts）
// 的语义，不逐行翻译：Node 通过仓储写入的行这里按同一批列直接落库。
//
// 字段合法性依据 internal/schema/sqlite_schema_business.go 的 DDL（NOT NULL /
// CHECK / 唯一索引）与 gateway 读路径的真实查询（分组调度策略必须是完整 16 键
// 文档、API Key 额度窗口是 {hourly,daily,weekly,monthly,total} 对象、时间计划是
// {enabled,timezone,mode,windows[,...]} 文档）。
func seedBusiness(ctx context.Context, e *env) (DomainResult, error) {
	guard, err := resolveBusinessGuard(ctx, e)
	if err != nil {
		return DomainResult{}, err
	}
	if guard.skip != "" {
		// 不静默降级：原因写进日志与（Counts 为空 ⇒ 未接线）覆盖报告的
		// notCovered 归属，让「business 库为什么没数据」可查。
		e.logger.Info("mockdata business 域跳过", "reason", guard.skip)
		return DomainResult{Name: DomainBusiness}, nil
	}

	writer := &businessWriter{
		ctx:    ctx,
		now:    e.options.clock(),
		secret: businessRuntimeSecret(e.options.Secret),
		counts: map[string]int{},
	}
	if err := e.tx(ctx, StoreBusiness, func(tx *sql.Tx) error {
		return writer.seed(tx, guard)
	}); err != nil {
		return DomainResult{}, err
	}

	// 摘要登记放在事务成功之后：半批数据不进摘要（事务失败时整体回滚）。
	e.setOwner(mockOwner{ID: guard.adminID, Username: guard.adminUsername, DisplayName: guard.adminDisplayName})
	for _, user := range writer.users {
		e.addMockUser(user)
	}
	for _, key := range writer.apiKeys {
		e.addAPIKey(key)
	}
	return DomainResult{Name: DomainBusiness, Counts: writer.counts}, nil
}

// businessRuntimeSecret 解析凭据信封密钥：Options.Secret 为空时回落到与
// gateway/jobs/schema seed 相同的开发默认值（Node runtime.ts:399 的
// defaultRuntimeSecret），否则解密的账户凭据在默认开发环境读不出来。
func businessRuntimeSecret(secret string) string {
	if trimmed := strings.TrimSpace(secret); trimmed != "" {
		return trimmed
	}
	return mockDefaultRuntimeSecret
}

// mockDefaultRuntimeSecret 与 gateway/cmd/juhe-ai-gateway/runtime.go 的
// defaultRuntimeSecret、schema 的 seedDefaultRuntimeSecret 同值；三处必须一致，
// 否则同一批造数数据在不同进程里解不开凭据。
const mockDefaultRuntimeSecret = "juhe-ai-dev-secret-change-me"

// mockPasswordIterations 与 gateway authsys 的 pbkdf2$sha512$120000 口令约定一致。
const mockPasswordIterations = 120000

// businessGuard 是域前置条件：没有 schema 或没有 seed 的库不造业务数据。
//
// 为什么不硬报错：mockdata 也要能在只建了空库的数据根上跑完（CLI 入口测试与
// 首次联调都会命中），此时该域没有数据、覆盖断言按「未接线」呈现，而不是让整个
// 造数失败。
type businessGuard struct {
	skip             string
	adminID          string
	adminUsername    string
	adminDisplayName string
	catalog          businessCatalog
}

// businessCatalog 是从 seed 数据里运行时解析出的真实 ID 组合：账户、分组、
// 策略都必须引用这些值，硬编码猜测会在 seed 变化后写出读不出来的行。
type businessCatalog struct {
	mainProvider   string
	compatProvider string
	profiles       []businessProfile
	models         []string
	imageModels    []string
	healthModel    string
}

// businessProfile 是 provider_protocol_profiles 的一行读投影。
type businessProfile struct {
	id           string
	provider     string
	protocol     string
	protocolVer  string
	healthModel  string
	accountTypes map[string]bool
}

// resolveBusinessGuard 读取运行时的真实 ID；缺 schema/seed 时返回 skip 原因。
func resolveBusinessGuard(ctx context.Context, e *env) (businessGuard, error) {
	exists, err := e.existsTable(ctx, StoreBusiness, "system_accounts")
	if err != nil {
		return businessGuard{}, err
	}
	if !exists {
		return businessGuard{skip: "business 库尚未初始化 schema（system_accounts 表缺失）"}, nil
	}
	db, err := e.openExisting(StoreBusiness)
	if err != nil {
		return businessGuard{}, err
	}
	if db == nil {
		return businessGuard{skip: "business 库文件不存在"}, nil
	}
	for _, table := range []string{"providers", "provider_protocol_profiles", "groups", "accounts", "api_keys"} {
		present, tableErr := queryExistsTable(ctx, db, table)
		if tableErr != nil {
			return businessGuard{}, tableErr
		}
		if !present {
			return businessGuard{skip: "business 库缺少表 " + table + "（schema 未 ensure）"}, nil
		}
	}
	guard := businessGuard{}
	if err := db.QueryRowContext(ctx, "SELECT id, username, display_name FROM system_accounts WHERE username = ? LIMIT 1", defaultAdminUsername).
		Scan(&guard.adminID, &guard.adminUsername, &guard.adminDisplayName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return businessGuard{skip: "未找到 seed 的 admin 系统账户（请先执行 --seed）"}, nil
		}
		return businessGuard{}, err
	}
	catalog, err := loadBusinessCatalog(ctx, db)
	if err != nil {
		return businessGuard{}, err
	}
	if catalog.mainProvider == "" || len(catalog.profiles) == 0 {
		return businessGuard{skip: "business 库缺少 seed 的 provider 或协议档案（请先执行 --seed）"}, nil
	}
	guard.catalog = catalog
	return guard, nil
}

// defaultAdminUsername 与 seed 的 system_accounts 行一致（Node 的 adminUsername）。
const defaultAdminUsername = "admin"

// loadBusinessCatalog 解析 provider / profile / 目录模型：账户与分组只能引用
// 真实存在的 provider_code、profile id 与 active 目录模型。
func loadBusinessCatalog(ctx context.Context, db *sql.DB) (businessCatalog, error) {
	catalog := businessCatalog{}
	rows, err := db.QueryContext(ctx, "SELECT code FROM providers WHERE enabled = 1 ORDER BY code")
	if err != nil {
		return catalog, err
	}
	var enabledProviders []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return catalog, err
		}
		enabledProviders = append(enabledProviders, code)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return catalog, err
	}
	rows.Close()
	for _, preferred := range []string{mockProviderGPT, mockProviderCompatOpenAI} {
		for _, code := range enabledProviders {
			if code != preferred {
				continue
			}
			if catalog.mainProvider == "" {
				catalog.mainProvider = code
			}
			if preferred == mockProviderCompatOpenAI {
				catalog.compatProvider = code
			}
		}
	}
	if catalog.mainProvider == "" && len(enabledProviders) > 0 {
		catalog.mainProvider = enabledProviders[0]
	}
	if catalog.compatProvider == "" {
		catalog.compatProvider = catalog.mainProvider
	}

	profileRows, err := db.QueryContext(ctx, `SELECT id, provider_code, protocol_code, protocol_version, default_health_check_model, account_types_json
		FROM provider_protocol_profiles WHERE enabled = 1 ORDER BY provider_code, id`)
	if err != nil {
		return catalog, err
	}
	for profileRows.Next() {
		var (
			profile      businessProfile
			healthModel  string
			accountTypes string
		)
		if err := profileRows.Scan(&profile.id, &profile.provider, &profile.protocol, &profile.protocolVer, &healthModel, &accountTypes); err != nil {
			profileRows.Close()
			return catalog, err
		}
		profile.healthModel = healthModel
		profile.accountTypes = map[string]bool{}
		var decoded []string
		if err := json.Unmarshal([]byte(accountTypes), &decoded); err == nil {
			for _, item := range decoded {
				profile.accountTypes[item] = true
			}
		}
		catalog.profiles = append(catalog.profiles, profile)
	}
	if err := profileRows.Err(); err != nil {
		profileRows.Close()
		return catalog, err
	}
	profileRows.Close()

	modelRows, err := db.QueryContext(ctx, `SELECT model, mode FROM provider_model_catalog
		WHERE status = 'active' AND catalog_visible = 1 ORDER BY catalog_order, model`)
	if err != nil {
		return catalog, err
	}
	for modelRows.Next() {
		var (
			model string
			mode  sql.NullString
		)
		if err := modelRows.Scan(&model, &mode); err != nil {
			modelRows.Close()
			return catalog, err
		}
		if len(catalog.models) < 24 {
			catalog.models = append(catalog.models, model)
		}
		if mode.Valid && mode.String == "image" && len(catalog.imageModels) < 6 {
			catalog.imageModels = append(catalog.imageModels, model)
		}
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return catalog, err
	}
	modelRows.Close()
	catalog.healthModel = catalog.textModel()
	return catalog, nil
}

// textModel 取第一个可用目录模型作为体检模型兜底。
func (c businessCatalog) textModel() string {
	if len(c.models) > 0 {
		return c.models[0]
	}
	return mockCustomModelLongContext
}

// modelAt 取第 index 个目录模型，越界回落到首个可用模型。
func (c businessCatalog) modelAt(index int) string {
	if len(c.models) == 0 {
		return mockCustomModelLongContext
	}
	if index < len(c.models) {
		return c.models[index]
	}
	return c.models[len(c.models)-1]
}

// imageModelAt 取第 index 个图像目录模型；目录里没有 image 模式模型时回落到
// 造数自建的全域图像模型（两种取值都在账户支持模型里有定义）。
func (c businessCatalog) imageModelAt(index int) string {
	if len(c.imageModels) == 0 {
		return mockCustomModelImage
	}
	if index < len(c.imageModels) {
		return c.imageModels[index]
	}
	return c.imageModels[len(c.imageModels)-1]
}

// profileFor 按首选 provider 与账户类型挑一个可用档案；没有匹配时返回 false，
// 调用方据此跳过该类账户而不是写出协议不兼容的行。
func (c businessCatalog) profileFor(provider string, accountType string) (businessProfile, bool) {
	for _, profile := range c.profiles {
		if profile.provider == provider && profile.accountTypes[accountType] {
			return profile, true
		}
	}
	for _, profile := range c.profiles {
		if profile.accountTypes[accountType] {
			return profile, true
		}
	}
	for _, profile := range c.profiles {
		if profile.provider == provider {
			return profile, true
		}
	}
	return businessProfile{}, false
}

// 造数标识：ID/名称前缀与 cleanup.go 的定义同源（这里只做本域内引用）。
const (
	mockAccountStatusActive     = "active"
	mockAccountStatusPending    = "pending_test"
	mockAccountStatusDisabled   = "disabled"
	mockAccountStatusError      = "error"
	mockAccountStatusRateLimit  = "rate_limited"
	mockAccountStatusTemporary  = "temporary_unavailable"
	mockAccountStatusExpired    = "expired"
	mockProviderGPT             = "gpt"
	mockProviderCompatOpenAI    = "openai"
	mockAuthorizationActive     = "active"
	mockAuthorizationPaused     = "paused"
	mockAuthorizationExpired    = "expired"
	mockAuthorizationRevoked    = "revoked"
	mockAuthorizationReturned   = "returned"
	mockCustomModelLongContext  = "mockdata-global-long-context"
	mockCustomModelImage        = "mockdata-global-image"
	mockModelMappingSourceModel = "mockdata-global-long-context"
)

// businessWriter 承载一次 business 域写入的上下文：统一时钟、信封密钥与按表
// 统计的行数（DomainResult.Counts 直接取它）。
type businessWriter struct {
	ctx    context.Context
	now    time.Time
	secret string
	counts map[string]int

	users   []mockUser
	apiKeys []mockAPIKey
}

// stamp 返回本次造数基准时间的毫秒 ISO 字符串（与 Node nowIso 一致）。
func (w *businessWriter) stamp() string {
	return w.now.UTC().Format(isoMillisLayout)
}

// at 返回基准时间偏移后的毫秒 ISO 字符串。
func (w *businessWriter) at(offset time.Duration) string {
	return w.now.Add(offset).UTC().Format(isoMillisLayout)
}

// dateKey 返回基准时间偏移后的 YYYY-MM-DD。
func (w *businessWriter) dateKey(offsetDays int) string {
	return w.now.AddDate(0, 0, offsetDays).UTC().Format("2006-01-02")
}

// put 写入一行并记账。INSERT OR REPLACE + 确定性主键让重复执行不报错也不叠加
// （骨架的 cleanupAll 会先按前缀清理，这里是第二道幂等保证）。
func (w *businessWriter) put(tx *sql.Tx, table string, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("mockdata business 写入 %s 需要至少一列", table)
	}
	names := make([]string, 0, len(columns))
	for column := range columns {
		names = append(names, column)
	}
	sortStrings(names)
	placeholders := make([]string, len(names))
	values := make([]any, len(names))
	for index, column := range names {
		placeholders[index] = "?"
		values[index] = columns[column]
	}
	query := "INSERT OR REPLACE INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := tx.ExecContext(w.ctx, query, values...); err != nil {
		return fmt.Errorf("写入 %s: %w", table, err)
	}
	w.counts[table]++
	return nil
}

// putJSON 把值编码成 JSON 文本列；编码失败即错（造数不接受静默空 JSON）。
func (w *businessWriter) putJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// seal 用 accountcrypto 的 v1 信封封装凭据明文，保证 gateway 读路径能解密。
func (w *businessWriter) seal(value any) (string, error) {
	return accountcrypto.EncryptJSON(w.secret, value)
}

// sortStrings 是 sort.Strings 的本地别名，避免为单点排序引入额外 import 语义。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// mockPasswordHash 生成 pbkdf2$sha512$120000$salt$digest 信封（Node hashPassword
// 的格式；salt 与 digest 都是 raw base64url），配套用户才能用固定口令登录。
func mockPasswordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	saltText := base64.RawURLEncoding.EncodeToString(salt)
	derived, err := pbkdf2.Key(sha512.New, password, []byte(saltText), mockPasswordIterations, 32)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"pbkdf2", "sha512", fmt.Sprint(mockPasswordIterations),
		saltText, base64.RawURLEncoding.EncodeToString(derived),
	}, "$"), nil
}

// secretHash 是 sha256 hex（gateway HashSecret 的算法），用于 token_hash 与
// API Key 的 key_hash。
func secretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// mockUserSeed 是一条配套系统账户样本。
type mockUserSeed struct {
	key        string
	id         string
	username   string
	display    string
	desc       string
	role       string
	status     string
	imageGen   bool
	aiLimit    int
	requestLim map[string]any
	lastLogin  time.Duration
}

// mockGroupSeed 是一条分组样本。
type mockGroupSeed struct {
	id             string
	name           string
	owner          string
	provider       string
	desc           string
	groupType      string
	enabled        bool
	isDefault      bool
	policyOverride map[string]any
}

// mockProxySeed 是一条代理样本。
type mockProxySeed struct {
	id       string
	name     string
	desc     string
	typ      string
	host     string
	port     int
	username string
	password string
	enabled  bool
}

// seed 按依赖顺序写入 business 库的全部样本。
func (w *businessWriter) seed(tx *sql.Tx, guard businessGuard) error {
	users, err := w.seedUsers(tx, guard)
	if err != nil {
		return err
	}
	proxies, err := w.seedProxies(tx, guard)
	if err != nil {
		return err
	}
	groups, groupInfo, err := w.seedGroups(tx, guard, users)
	if err != nil {
		return err
	}
	accounts, err := w.seedAccounts(tx, guard, users, groups, proxies)
	if err != nil {
		return err
	}
	if err := w.seedAccountProjections(tx, guard, accounts); err != nil {
		return err
	}
	if err := w.seedAccountRuntimeSamples(tx, guard, accounts); err != nil {
		return err
	}
	teams, err := w.seedTeams(tx, guard, users)
	if err != nil {
		return err
	}
	granteeIDs, err := w.seedAuthorizations(tx, guard, users, groups, accounts, teams)
	if err != nil {
		return err
	}
	if err := w.seedAuthorizationInstances(tx, guard, accounts, granteeIDs); err != nil {
		return err
	}
	if err := w.seedGroupAuthorizationSettings(tx, guard, groupInfo); err != nil {
		return err
	}
	keys, err := w.seedApiKeys(tx, guard, users, groups)
	if err != nil {
		return err
	}
	if err := w.seedCustomProviderModels(tx, guard, users); err != nil {
		return err
	}
	if err := w.seedAnnouncements(tx, guard, users); err != nil {
		return err
	}
	if err := w.seedResponseInspectionPolicies(tx, guard, groups); err != nil {
		return err
	}
	if err := w.seedExternalIntegration(tx, guard); err != nil {
		return err
	}
	if err := w.seedOpenAICompatibleStorage(tx, guard, keys); err != nil {
		return err
	}
	if err := w.seedQuestionBank(tx, guard, users); err != nil {
		return err
	}
	if err := w.seedOidcProvider(tx, guard); err != nil {
		return err
	}
	return nil
}

// seedUsers 写入配套系统账户（mockdata_admin + 6 个普通用户），并登记摘要。
func (w *businessWriter) seedUsers(tx *sql.Tx, guard businessGuard) ([]mockUserSeed, error) {
	seeds := []mockUserSeed{
		{
			key: "manager", id: CleanupIDPrefix + "user_admin", username: CleanupIDPrefix + "admin",
			display: CleanupNamePrefix + "管理员用户",
			desc:    "Mockdata 普通管理员账号，用于管理员模式下验证管理员自有资源、筛选和创建目标",
			role:    "admin", status: mockAccountStatusActive, imageGen: true, aiLimit: 60,
			requestLim: map[string]any{"perMinute": 600, "perDay": 20000, "perMonth": 300000},
			lastLogin:  -2 * time.Hour,
		},
		{
			key: "ops", id: CleanupIDPrefix + "user_ops", username: CleanupIDPrefix + "ops",
			display: CleanupNamePrefix + "运维用户",
			desc:    "Mockdata 运维协作用户，用于账户授权、操作日志和调用方统计",
			role:    "user", status: mockAccountStatusActive, aiLimit: 40,
			requestLim: map[string]any{"perMinute": 240, "perDay": 8000},
			lastLogin:  -6 * time.Hour,
		},
		{
			key: "dev", id: CleanupIDPrefix + "user_dev", username: CleanupIDPrefix + "dev",
			display: CleanupNamePrefix + "研发用户",
			desc:    "Mockdata 研发协作用户，用于分组授权和团队授权",
			role:    "user", status: mockAccountStatusActive, aiLimit: 30,
			requestLim: map[string]any{"perMinute": 200, "perDay": 6000, "perWeek": 30000},
			lastLogin:  -30 * time.Hour,
		},
		{
			key: "tester", id: CleanupIDPrefix + "user_tester", username: CleanupIDPrefix + "tester",
			display: CleanupNamePrefix + "测试用户",
			desc:    "Mockdata 测试协作用户，用于团队授权和回归验证",
			role:    "user", status: mockAccountStatusActive, imageGen: true, aiLimit: 20,
			requestLim: map[string]any{"perMinute": 120, "perDay": 4000},
			lastLogin:  -3 * 24 * time.Hour,
		},
		{
			key: "finance", id: CleanupIDPrefix + "user_finance", username: CleanupIDPrefix + "finance",
			display: CleanupNamePrefix + "财务用户",
			desc:    "Mockdata 财务观察用户，用于额度和授权展示",
			role:    "user", status: mockAccountStatusActive, aiLimit: 10,
			requestLim: map[string]any{"perMinute": 60, "perDay": 1200, "perMonth": 20000},
		},
		{
			key: "viewer", id: CleanupIDPrefix + "user_viewer", username: CleanupIDPrefix + "viewer",
			display: CleanupNamePrefix + "只读观察用户",
			desc:    "Mockdata 观察用户，用于公告已读和操作可见性",
			role:    "user", status: mockAccountStatusActive, aiLimit: 5,
			requestLim: map[string]any{"perDay": 600},
		},
		{
			key: "disabled", id: CleanupIDPrefix + "user_disabled", username: CleanupIDPrefix + "disabled",
			display: CleanupNamePrefix + "停用用户",
			desc:    "Mockdata 停用用户，用于系统账号状态展示",
			role:    "user", status: "disabled",
		},
	}
	passwordHash, err := mockPasswordHash(MockUserPassword)
	if err != nil {
		return nil, err
	}
	now := w.stamp()
	for _, seed := range seeds {
		requestLimits := any(nil)
		if len(seed.requestLim) > 0 {
			encoded, encodeErr := w.putJSON(seed.requestLim)
			if encodeErr != nil {
				return nil, encodeErr
			}
			requestLimits = encoded
		}
		var lastLogin any
		if seed.lastLogin != 0 {
			lastLogin = w.at(seed.lastLogin)
		}
		var aiLimit any
		if seed.aiLimit > 0 {
			aiLimit = seed.aiLimit
		}
		if err := w.put(tx, "system_accounts", map[string]any{
			"id": seed.id, "username": seed.username, "display_name": seed.display,
			"description": seed.desc, "role": seed.role, "status": seed.status,
			"password_hash": passwordHash, "must_change_password": 0,
			"image_generation_enabled": boolInt(seed.imageGen), "ai_account_limit": aiLimit,
			"request_limits_json": requestLimits, "last_login_at": lastLogin,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return nil, err
		}
		w.users = append(w.users, mockUser{
			Name: seed.key, ID: seed.id, Username: seed.username, DisplayName: seed.display,
			Role: seed.role, Status: seed.status, Password: MockUserPassword,
		})
	}
	// 会话样本：token_hash 用 sha256 hex（与 gateway 会话校验同算法），明文不落库。
	if err := w.put(tx, "system_sessions", map[string]any{
		"id": CleanupIDPrefix + "session_admin", "system_account_id": CleanupIDPrefix + "user_admin",
		"token_hash": secretHash(CleanupTracePrefix + "session-token-admin"),
		"expires_at": w.at(7 * 24 * time.Hour), "created_at": w.at(-1 * time.Hour),
		"last_seen_at": w.at(-10 * time.Minute),
	}); err != nil {
		return nil, err
	}
	return seeds, nil
}

// seedProxies 写入代理样本（http/socks5h/停用），返回 loginsuffix → id 映射。
func (w *businessWriter) seedProxies(tx *sql.Tx, guard businessGuard) (map[string]string, error) {
	seeds := []mockProxySeed{
		{
			id: CleanupIDPrefix + "proxy_http", name: CleanupNamePrefix + "HTTP 代理",
			desc: "Mockdata HTTP 代理，绑定到主力 API Key 账户", typ: "http",
			host: "127.0.0.1", port: 7890, username: "mock_proxy", password: "mock_proxy_password",
			enabled: true,
		},
		{
			id: CleanupIDPrefix + "proxy_socks", name: CleanupNamePrefix + "SOCKS 代理",
			desc: "Mockdata SOCKS 代理，绑定到 OAuth 账户", typ: "socks5h",
			host: "127.0.0.1", port: 1080, enabled: true,
		},
		{
			id: CleanupIDPrefix + "proxy_disabled", name: CleanupNamePrefix + "停用代理",
			desc: "Mockdata 停用代理，用于代理状态展示", typ: "http",
			host: "127.0.0.1", port: 18080, enabled: false,
		},
	}
	now := w.stamp()
	ids := map[string]string{}
	for _, seed := range seeds {
		var sealed any
		if seed.password != "" {
			envelope, err := w.seal(map[string]string{"password": seed.password})
			if err != nil {
				return nil, err
			}
			sealed = envelope
		}
		var username any
		if seed.username != "" {
			username = seed.username
		}
		testStatus := "success"
		var latency, lastTested, outboundIP, outboundRegion, lastMessage any
		if seed.enabled {
			latency, lastTested, outboundIP, outboundRegion = 128, w.at(-45*time.Minute), "203.0.113.10", "本地"
			lastMessage = "Mockdata 代理连通性正常"
		} else {
			testStatus = "unknown"
		}
		if err := w.put(tx, "proxy_profiles", map[string]any{
			"id": seed.id, "system_account_id": guard.adminID, "name": seed.name,
			"description": seed.desc, "type": seed.typ, "host": seed.host, "port": seed.port,
			"username": username, "password_encrypted": sealed, "enabled": boolInt(seed.enabled),
			"test_status": testStatus, "latency_ms": latency, "outbound_ip": outboundIP,
			"outbound_region": outboundRegion, "last_test_message": lastMessage,
			"last_tested_at": lastTested, "created_at": now, "updated_at": now,
		}); err != nil {
			return nil, err
		}
		switch seed.id {
		case CleanupIDPrefix + "proxy_http":
			ids["http"] = seed.id
		case CleanupIDPrefix + "proxy_socks":
			ids["socks"] = seed.id
		default:
			ids["disabled"] = seed.id
		}
	}
	return ids, nil
}

// boolInt 把布尔投影成 SQLite 的 0/1（与 Node 的 boolean→integer 一致）。
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// mockHighConcurrencyPolicy 生成高并发分组的完整调度策略。
//
// 为什么是完整 16 键而不是只写 5 个可写键：gateway 的
// parseStoredSchedulingPolicy 对 high_concurrency 分组做严格读校验（缺键即
// 500），写入端必须落整份文档；Node 的创建路径也把可写覆盖合并进默认值后整体
// 持久化。
func mockHighConcurrencyPolicy(overrides map[string]any) map[string]any {
	policy := map[string]any{
		"mode":                            "balanced_fast",
		"defaultSoftConcurrency":          5000,
		"fastFirstEnabled":                true,
		"fallbackOnQueueEnabled":          true,
		"breakAffinityOnSoftLimit":        true,
		"breakAffinityOnQueueWaitMs":      0,
		"slowRequestThresholdMs":          30000,
		"firstOutputSlowThresholdMs":      15000,
		"recentTimeoutWindowSeconds":      120,
		"recentTimeoutPenaltyThreshold":   2,
		"maxQueueWaitMs":                  60000,
		"maxQueueSize":                    5000,
		"perApiKeyQueueLimit":             5000,
		"clientIpConcurrencyLimit":        0,
		"clientIpConcurrencyOverflowMode": "reject",
		"imageLaneMaxConcurrency":         0,
	}
	for key, value := range overrides {
		policy[key] = value
	}
	return policy
}

// mockGroupInfo 是分组写入后的读回投影（授权分组设置需要复用分组类型与策略）。
type mockGroupInfo struct {
	id        string
	groupType string
	policy    any
}

// seedGroups 写入分组样本：7 个管理员级分组、每用户 1 个默认分组、管理员自有
// 分组与 3 个"授权给别人"的分组。
func (w *businessWriter) seedGroups(tx *sql.Tx, guard businessGuard, users []mockUserSeed) (map[string]string, map[string]mockGroupInfo, error) {
	mainProvider := guard.catalog.mainProvider
	compatProvider := guard.catalog.compatProvider
	highPolicy := map[string]any{
		"defaultSoftConcurrency": 12, "maxQueueWaitMs": 45000,
		"clientIpConcurrencyLimit": 8, "clientIpConcurrencyOverflowMode": "queue",
		"imageLaneMaxConcurrency": 6,
	}
	managerHighPolicy := map[string]any{
		"defaultSoftConcurrency": 10, "maxQueueWaitMs": 40000,
		"clientIpConcurrencyLimit": 6, "clientIpConcurrencyOverflowMode": "queue",
		"imageLaneMaxConcurrency": 4,
	}
	opsGrantedPolicy := map[string]any{
		"defaultSoftConcurrency": 6, "maxQueueWaitMs": 30000,
		"clientIpConcurrencyLimit": 4, "clientIpConcurrencyOverflowMode": "reject",
		"imageLaneMaxConcurrency": 2,
	}
	seeds := []mockGroupSeed{
		{id: CleanupIDPrefix + "group_main", name: CleanupNamePrefix + "主力分组", owner: guard.adminID,
			provider: mainProvider, desc: "主力业务分组，包含高优先级与常规账户", enabled: true},
		{id: CleanupIDPrefix + "group_high", name: CleanupNamePrefix + "高并发 AI 分组", owner: guard.adminID,
			provider: mainProvider, desc: "高并发调度分组，用于分组管理页展示软并发、短队列和单 IP 并发限制",
			groupType: "high_concurrency", enabled: true, policyOverride: highPolicy},
		{id: CleanupIDPrefix + "group_backup", name: CleanupNamePrefix + "备用分组", owner: guard.adminID,
			provider: mainProvider, desc: "备用调度分组，包含降级账户和冷却样例", enabled: true},
		{id: CleanupIDPrefix + "group_oauth", name: CleanupNamePrefix + "OAuth 分组", owner: guard.adminID,
			provider: mainProvider, desc: "OAuth 账户分组，用于 Codex 额度快照展示", enabled: true},
		{id: CleanupIDPrefix + "group_compat", name: CleanupNamePrefix + "OpenAI 兼容分组", owner: guard.adminID,
			provider: compatProvider, desc: "OpenAI-compatible 标准客户端分组，用于客户端兼容覆盖和通用 API Key 透传展示",
			enabled: true},
		{id: CleanupIDPrefix + "group_experiment", name: CleanupNamePrefix + "实验分组", owner: guard.adminID,
			provider: mainProvider, desc: "实验分组，用于授权、额度和模型策略演示", enabled: true},
		{id: CleanupIDPrefix + "group_empty", name: CleanupNamePrefix + "空分组", owner: guard.adminID,
			provider: mainProvider, desc: "空分组样本（enabled=0 且无账户绑定）", enabled: false},
		{id: CleanupIDPrefix + "group_manager_main", name: CleanupNamePrefix + "管理员自有分组",
			owner: CleanupIDPrefix + "user_admin", provider: mainProvider,
			desc: "普通管理员自有分组，用于管理员模式按管理员角色筛选和创建目标验收", enabled: true},
		{id: CleanupIDPrefix + "group_manager_high", name: CleanupNamePrefix + "管理员高并发分组",
			owner: CleanupIDPrefix + "user_admin", provider: mainProvider,
			desc:      "普通管理员自有高并发分组，用于管理员角色下的 AI 分组管理验收",
			groupType: "high_concurrency", enabled: true, policyOverride: managerHighPolicy},
		{id: CleanupIDPrefix + "group_dev_granted", name: CleanupNamePrefix + "研发授权分组",
			owner: CleanupIDPrefix + "user_dev", provider: mainProvider,
			desc: "研发用户自有分组，主动授权给超级管理员，用于 AI 分组管理查看授权分组", enabled: true},
		{id: CleanupIDPrefix + "group_ops_granted", name: CleanupNamePrefix + "运维授权高并发分组",
			owner: CleanupIDPrefix + "user_ops", provider: mainProvider,
			desc:      "运维用户自有高并发分组，授权给超级管理员后暂停，用于授权状态展示",
			groupType: "high_concurrency", enabled: true, policyOverride: opsGrantedPolicy},
		{id: CleanupIDPrefix + "group_tester_granted", name: CleanupNamePrefix + "测试授权过期分组",
			owner: CleanupIDPrefix + "user_tester", provider: mainProvider,
			desc: "测试用户自有分组，授权给超级管理员后过期，用于 AI 分组管理过期状态展示", enabled: true},
	}
	// 每个配套用户 1 个默认分组（is_default 唯一索引要求 (owner, provider) 至多一行）。
	for _, user := range users {
		seeds = append(seeds, mockGroupSeed{
			id: CleanupIDPrefix + "group_default_" + user.key, name: CleanupNamePrefix + "默认分组",
			owner: user.id, provider: mainProvider, enabled: true, isDefault: true,
			desc: "配套用户的默认分组，用于默认分组与授权分组的分流展示",
		})
	}
	now := w.stamp()
	ids := map[string]string{}
	info := map[string]mockGroupInfo{}
	for _, seed := range seeds {
		groupType := seed.groupType
		if groupType == "" {
			groupType = "personal"
		}
		var policy any
		if groupType == "high_concurrency" {
			encoded, err := w.putJSON(mockHighConcurrencyPolicy(seed.policyOverride))
			if err != nil {
				return nil, nil, err
			}
			policy = encoded
		}
		if err := w.put(tx, "groups", map[string]any{
			"id": seed.id, "system_account_id": seed.owner, "name": seed.name,
			"provider_code": seed.provider, "description": seed.desc,
			"enabled": boolInt(seed.enabled), "is_default": boolInt(seed.isDefault),
			"group_type": groupType, "scheduling_policy_json": policy,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return nil, nil, err
		}
		key := strings.TrimPrefix(seed.id, CleanupIDPrefix+"group_")
		ids[key] = seed.id
		info[key] = mockGroupInfo{id: seed.id, groupType: groupType, policy: policy}
	}
	return ids, info, nil
}

// mockMappingSeed 是一条账户模型映射样本。
type mockMappingSeed struct {
	sourceModel  string
	sourceFamily string
	upstream     string
	upstreamFam  string
	enabled      bool
}

// mockAccountSeed 是一条 AI 账户样本。
type mockAccountSeed struct {
	id          string
	name        string
	owner       string
	group       string
	slug        string
	typ         string
	status      string
	notes       string
	priority    int
	concurrency int
	super       bool
	fallback    bool
	balance     bool
	// unschedulable 用反向布尔：样本里表达"可调度"的是绝大多数，默认零值即
	// 可调度，只有停调账户显式置位。
	unschedulable bool
	schedule      string
	proxy         string
	compat        string
	mode          string
	tags          []string
	modelSlots    []int
	extraModels   []string
	expiresIn     time.Duration
	cooldownIn    time.Duration
	errorCode     string
	errorMsg      string
	keyPool       int
	keyStrategy   string
	mappings      []mockMappingSeed
	oauth         bool
}

// seedAccounts 写入 AI 账户及其分组绑定、支持模型、标签与模型映射。
func (w *businessWriter) seedAccounts(tx *sql.Tx, guard businessGuard, users []mockUserSeed, groups map[string]string, proxies map[string]string) ([]mockAccountSeed, error) {
	keyProfile, ok := guard.catalog.profileFor(guard.catalog.mainProvider, "api_key")
	if !ok {
		return nil, fmt.Errorf("mockdata business 找不到可用的 api_key 档案（provider=%s）", guard.catalog.mainProvider)
	}
	oauthProfile, oauthOK := guard.catalog.profileFor(guard.catalog.mainProvider, "oauth")
	compatProfile, compatOK := guard.catalog.profileFor(guard.catalog.compatProvider, "api_key")
	if !compatOK {
		compatProfile = keyProfile
	}

	seeds := []mockAccountSeed{
		{id: CleanupIDPrefix + "acc_primary", name: CleanupNamePrefix + "主力 API Key 账户", owner: guard.adminID,
			group: groups["main"], slug: "primary", typ: "api_key", status: mockAccountStatusActive,
			priority: 0, concurrency: 80, super: true, proxy: proxies["http"], schedule: "active",
			mode: "responses_sse", modelSlots: []int{0, 1, 2}, tags: []string{"主力", "超级优先"},
			notes: "Mockdata 主力账号，超级优先"},
		{id: CleanupIDPrefix + "acc_proxied", name: CleanupNamePrefix + "带代理 API Key 账户", owner: guard.adminID,
			group: groups["main"], slug: "proxied", typ: "api_key", status: mockAccountStatusActive,
			priority: 10, concurrency: 45, proxy: proxies["http"], mode: "responses_sse",
			modelSlots: []int{1, 2}, tags: []string{"代理"},
			notes: "Mockdata 代理账号，用于账户授权给用户"},
		{id: CleanupIDPrefix + "acc_normal", name: CleanupNamePrefix + "普通 API Key 账户", owner: guard.adminID,
			group: groups["main"], slug: "normal", typ: "api_key", status: mockAccountStatusActive,
			priority: 30, concurrency: 35, schedule: "inactive", mode: "responses_sse",
			modelSlots: []int{2, 3}, notes: "Mockdata 普通账号"},
		{id: CleanupIDPrefix + "acc_standard", name: CleanupNamePrefix + "OpenAI 标准兼容账户", owner: guard.adminID,
			group: groups["main"], slug: "standard-client", typ: "api_key", status: mockAccountStatusActive,
			priority: 35, concurrency: 30, mode: "responses_sse", modelSlots: []int{0, 1, 2},
			tags: []string{"标准兼容", "模型映射"},
			mappings: []mockMappingSeed{
				{sourceModel: mockModelMappingSourceModel, sourceFamily: "chat_completions",
					upstream: "", upstreamFam: "chat_completions", enabled: true},
				{sourceModel: "", sourceFamily: "chat_completions",
					upstream: "", upstreamFam: "chat_completions", enabled: false},
			},
			notes: "Mockdata OpenAI 标准兼容账号，用于客户端兼容、模型映射和标签展示"},
		{id: CleanupIDPrefix + "acc_compat", name: CleanupNamePrefix + "OpenAI 兼容 API Key 账户", owner: guard.adminID,
			group: groups["compat"], slug: "openai-compatible", typ: "api_key", status: mockAccountStatusActive,
			priority: 36, concurrency: 28, mode: "chat_json", modelSlots: []int{1, 2},
			tags:  []string{"OpenAI兼容", "标准客户端"},
			notes: "Mockdata OpenAI-compatible 账号，用于 openai_standard 客户端兼容覆盖"},
		{id: CleanupIDPrefix + "acc_multikey", name: CleanupNamePrefix + "多 API Key 轮换账户", owner: guard.adminID,
			group: groups["main"], slug: "multi-key-pool", typ: "api_key", status: mockAccountStatusActive,
			priority: 18, concurrency: 70, balance: true, mode: "responses_sse",
			modelSlots: []int{1, 2}, tags: []string{"多Key", "故障隔离", "余额自动关闭"},
			keyPool: 4, keyStrategy: "weighted_round_robin",
			notes: "Mockdata 账户内多 API Key；创建请求携带余额开启状态，最终应自动关闭并保留配置"},
		{id: CleanupIDPrefix + "acc_image", name: CleanupNamePrefix + "图像生成账户", owner: guard.adminID,
			group: groups["experiment"], slug: "image", typ: "api_key", status: mockAccountStatusActive,
			priority: 50, concurrency: 18, mode: "images_json", modelSlots: []int{2},
			extraModels: []string{mockCustomModelImage}, tags: []string{"图像生成"},
			notes: "Mockdata 图像生成账号，用于 Images API、图片 token 和系统账户图像权限展示"},
		{id: CleanupIDPrefix + "acc_burst_fast", name: CleanupNamePrefix + "高并发快响账户", owner: guard.adminID,
			group: groups["high"], slug: "burst-fast", typ: "api_key", status: mockAccountStatusActive,
			priority: 2, concurrency: 180, super: true, mode: "responses_sse",
			modelSlots: []int{0, 1, 2}, tags: []string{"高并发"},
			notes: "Mockdata 高并发分组快响账号，用于高并发调度策略展示"},
		{id: CleanupIDPrefix + "acc_burst_image", name: CleanupNamePrefix + "高并发图像账户", owner: guard.adminID,
			group: groups["high"], slug: "burst-image", typ: "api_key", status: mockAccountStatusActive,
			priority: 12, concurrency: 120, mode: "chat_json", modelSlots: []int{1, 2},
			extraModels: []string{mockCustomModelImage},
			notes:       "Mockdata 高并发分组图像 / 长请求账号，用于图像 lane 并发展示"},
		{id: CleanupIDPrefix + "acc_burst_fallback", name: CleanupNamePrefix + "高并发备用账户", owner: guard.adminID,
			group: groups["high"], slug: "burst-fallback", typ: "api_key", status: mockAccountStatusActive,
			priority: 70, concurrency: 90, fallback: true, mode: "responses_sse", modelSlots: []int{1, 2},
			notes: "Mockdata 高并发分组备用账号，用于软并发触发后的 fallback 展示"},
		{id: CleanupIDPrefix + "acc_fallback", name: CleanupNamePrefix + "降级备用账户", owner: guard.adminID,
			group: groups["backup"], slug: "fallback", typ: "api_key", status: mockAccountStatusActive,
			priority: 80, concurrency: 25, fallback: true, mode: "responses_sse", modelSlots: []int{1, 3},
			notes: "Mockdata 备用账号"},
		{id: CleanupIDPrefix + "acc_oauth", name: CleanupNamePrefix + "OAuth 主力账户", owner: guard.adminID,
			group: groups["oauth"], slug: "oauth-main", typ: "oauth", status: mockAccountStatusActive,
			priority: 5, concurrency: 50, proxy: proxies["socks"], mode: "responses_sse",
			modelSlots: []int{0, 1}, oauth: true, tags: []string{"OAuth"},
			notes: "Mockdata OAuth 主力账号，带 Codex 额度快照"},
		{id: CleanupIDPrefix + "acc_oauth_backup", name: CleanupNamePrefix + "OAuth 备用账户", owner: guard.adminID,
			group: groups["oauth"], slug: "oauth-backup", typ: "oauth", status: mockAccountStatusActive,
			priority: 60, concurrency: 20, fallback: true, mode: "responses_sse",
			modelSlots: []int{1, 2}, oauth: true, notes: "Mockdata OAuth 备用账号"},
		{id: CleanupIDPrefix + "acc_pending", name: CleanupNamePrefix + "待检查账户", owner: guard.adminID,
			group: groups["experiment"], slug: "pending-test", typ: "api_key", status: mockAccountStatusPending,
			priority: 90, concurrency: 10, mode: "responses_sse", modelSlots: []int{2},
			tags:  []string{"待检查"},
			notes: "Mockdata 待检查账号，用于待检查状态、人工诊断入口和不可调度展示"},
		{id: CleanupIDPrefix + "acc_disabled", name: CleanupNamePrefix + "手动停用账户", owner: guard.adminID,
			group: groups["experiment"], slug: "disabled", typ: "api_key", status: mockAccountStatusDisabled,
			priority: 95, concurrency: 8, mode: "responses_sse", modelSlots: []int{2},
			tags: []string{"停用"}, notes: "Mockdata 手动停用账号，用于停用状态和恢复入口展示"},
		{id: CleanupIDPrefix + "acc_unschedulable", name: CleanupNamePrefix + "停调账户", owner: guard.adminID,
			group: groups["experiment"], slug: "unschedulable", typ: "api_key", status: mockAccountStatusActive,
			priority: 98, concurrency: 8, unschedulable: true, mode: "responses_sse", modelSlots: []int{2},
			tags:  []string{"停调"},
			notes: "Mockdata 正常但手动关闭调度账号，用于参与调度筛选和有效可用性展示"},
		{id: CleanupIDPrefix + "acc_scheduled", name: CleanupNamePrefix + "时间计划停调账户", owner: guard.adminID,
			group: groups["experiment"], slug: "scheduled-inactive", typ: "api_key", status: mockAccountStatusActive,
			priority: 100, concurrency: 8, schedule: "inactive", mode: "responses_sse",
			modelSlots: []int{2}, tags: []string{"时间计划"},
			notes: "Mockdata 当前不在允许时段内的账号，用于账户时间计划和调度过滤展示"},
		{id: CleanupIDPrefix + "acc_rate_limited", name: CleanupNamePrefix + "限流中账户", owner: guard.adminID,
			group: groups["backup"], slug: "rate-limited", typ: "api_key", status: mockAccountStatusRateLimit,
			priority: 120, concurrency: 15, cooldownIn: 2 * time.Hour, mode: "responses_sse",
			modelSlots: []int{2}, errorCode: "rate_limit_exceeded", errorMsg: "Mockdata 模拟上游日额度耗尽",
			tags: []string{"限流"}, notes: "Mockdata 限流状态账号"},
		{id: CleanupIDPrefix + "acc_temporary", name: CleanupNamePrefix + "临时不可调用账户", owner: guard.adminID,
			group: groups["experiment"], slug: "temporary", typ: "api_key", status: mockAccountStatusTemporary,
			priority: 130, concurrency: 15, cooldownIn: 30 * time.Minute, mode: "responses_sse",
			modelSlots: []int{1, 3}, errorCode: "service_unavailable", errorMsg: "Mockdata 模拟上游 503 维护",
			notes: "Mockdata 临时不可调用状态账号"},
		{id: CleanupIDPrefix + "acc_error", name: CleanupNamePrefix + "异常账户", owner: guard.adminID,
			group: groups["experiment"], slug: "error", typ: "api_key", status: mockAccountStatusError,
			priority: 160, concurrency: 10, mode: "responses_sse", modelSlots: []int{0},
			errorCode: "invalid_api_key", errorMsg: "Mockdata 模拟 401 认证失败",
			notes: "Mockdata 异常状态账号"},
		{id: CleanupIDPrefix + "acc_expired", name: CleanupNamePrefix + "已到期账户", owner: guard.adminID,
			group: groups["experiment"], slug: "expired", typ: "api_key", status: mockAccountStatusExpired,
			priority: 5, concurrency: 5, expiresIn: -24 * time.Hour, mode: "responses_sse",
			modelSlots: []int{3}, notes: "Mockdata 已到期停用账号"},
		{id: CleanupIDPrefix + "acc_manager_primary", name: CleanupNamePrefix + "管理员 API Key 账户",
			owner: CleanupIDPrefix + "user_admin", group: groups["manager_main"], slug: "manager-primary",
			typ: "api_key", status: mockAccountStatusActive, priority: 8, concurrency: 48,
			mode: "responses_sse", modelSlots: []int{1, 2, 3},
			notes: "Mockdata 普通管理员自有账号，用于管理员模式资源归属验收"},
		{id: CleanupIDPrefix + "acc_manager_burst", name: CleanupNamePrefix + "管理员高并发账户",
			owner: CleanupIDPrefix + "user_admin", group: groups["manager_high"], slug: "manager-burst",
			typ: "api_key", status: mockAccountStatusActive, priority: 4, concurrency: 120, super: true,
			schedule: "active", mode: "responses_sse", modelSlots: []int{0, 1, 2},
			notes: "Mockdata 普通管理员高并发账号，用于管理员角色高并发分组验收"},
		{id: CleanupIDPrefix + "acc_dev_shared", name: CleanupNamePrefix + "研发共享给超级管理员账户",
			owner: CleanupIDPrefix + "user_dev", group: groups["dev_granted"], slug: "dev-admin-grant",
			typ: "api_key", status: mockAccountStatusActive, priority: 20, concurrency: 24,
			mode: "responses_sse", modelSlots: []int{2, 3},
			tags:  []string{"授权共享"},
			notes: "Mockdata 研发用户自有账号，用于授权分组给超级管理员"},
		{id: CleanupIDPrefix + "acc_ops_shared", name: CleanupNamePrefix + "运维共享给超级管理员账户",
			owner: CleanupIDPrefix + "user_ops", group: groups["ops_granted"], slug: "ops-admin-grant",
			typ: "api_key", status: mockAccountStatusActive, priority: 15, concurrency: 40,
			mode: "responses_sse", modelSlots: []int{1, 2},
			tags:  []string{"授权共享"},
			notes: "Mockdata 运维用户自有账号，用于暂停授权分组展示"},
		{id: CleanupIDPrefix + "acc_tester_shared", name: CleanupNamePrefix + "测试共享给超级管理员账户",
			owner: CleanupIDPrefix + "user_tester", group: groups["tester_granted"], slug: "tester-admin-grant",
			typ: "api_key", status: mockAccountStatusActive, priority: 40, concurrency: 12,
			mode: "responses_sse", modelSlots: []int{3},
			notes: "Mockdata 测试用户自有账号，用于过期授权分组展示"},
	}

	now := w.stamp()
	for index := range seeds {
		seed := &seeds[index]
		profile := keyProfile
		if seed.typ == "oauth" {
			if oauthOK {
				profile = oauthProfile
			} else {
				// 没有支持 oauth 的档案时退化写入 api_key 账户：宁可少一个
				// 类型样本，也不写出协议档案不接受的账户。
				seed.typ = "api_key"
			}
		}
		if seed.id == CleanupIDPrefix+"acc_compat" {
			profile = compatProfile
		}
		credentials, fingerprint, mask, err := w.accountCredentials(seed, index)
		if err != nil {
			return nil, err
		}
		healthModel := profile.healthModel
		if healthModel == "" {
			healthModel = guard.catalog.healthModel
		}
		scheduleJSON, nextCheck, err := w.accountSchedule(seed)
		if err != nil {
			return nil, err
		}
		columns := map[string]any{
			"id": seed.id, "config_revision": 1, "dispatch_revision": 1,
			"circuit_projection_revision": 0,
			"system_account_id":           seed.owner, "provider_code": profile.provider,
			"provider_protocol_profile_id": profile.id, "protocol_code": profile.protocol,
			"protocol_version": profile.protocolVer, "name": seed.name, "type": seed.typ,
			"status": seed.status, "credentials_encrypted": credentials,
			"credential_fingerprint": fingerprint, "credential_mask": mask,
			"oauth_refresh_token_present": 0, "proxy_profile_id": nullString(seed.proxy),
			"concurrency_limit": seed.concurrency, "priority": seed.priority,
			"super_priority_enabled": boolInt(seed.super), "fallback_enabled": boolInt(seed.fallback),
			"client_compatibility": clientCompatibility(seed.compat), "schedulable": boolInt(!seed.unschedulable),
			"availability_schedule_json": scheduleJSON, "availability_schedule_next_check_at": nextCheck,
			"notes": seed.notes, "account_expires_at": nullTime(w, seed.expiresIn),
			"last_used_at": w.at(-90 * time.Minute), "cooldown_until": nullTime(w, seed.cooldownIn),
			"last_error_code": nullString(seed.errorCode), "last_error_message": nullString(seed.errorMsg),
			"last_error_trace_id":                            traceFor(seed.errorCode),
			"cooldown_retest_failure_count":                  0,
			"temporary_unavailable_continuous_probe_enabled": 1,
			"health_check_model":                             healthModel, "health_check_endpoint_mode": seed.mode,
			"last_health_check_at": w.at(-1 * time.Hour), "next_health_check_at": w.at(1 * time.Hour),
			"last_health_success_at": w.at(-1 * time.Hour), "health_check_failure_count": 0,
			"last_health_check_status_code": 200, "stream_failure_count": 0,
			"balance_query_enabled":         boolInt(seed.balance),
			"balance_query_config_json":     balanceConfig(seed.balance),
			"balance_query_next_refresh_at": nullTime(w, refreshAfter(seed.balance)),
			"deleted_at":                    nil, "deleted_by": nil, "created_at": now, "updated_at": now,
		}
		if seed.typ == "oauth" {
			columns["oauth_access_token_expires_at"] = w.at(6 * time.Hour)
			columns["oauth_refresh_token_present"] = 1
		}
		if seed.status == mockAccountStatusError || seed.status == mockAccountStatusRateLimit ||
			seed.status == mockAccountStatusTemporary {
			columns["last_health_check_status_code"] = 503
			columns["health_check_failure_count"] = 3
			columns["last_health_check_error_code"] = seed.errorCode
			columns["last_health_check_error_message"] = seed.errorMsg
			columns["last_health_check_trace_id"] = traceFor(seed.errorCode)
			columns["last_health_check_at"] = w.at(-12 * time.Minute)
			columns["next_health_check_at"] = w.at(18 * time.Minute)
			columns["stream_failure_count"] = 3
			columns["stream_failure_window_started_at"] = w.at(-20 * time.Minute)
		}
		if err := w.put(tx, "accounts", columns); err != nil {
			return nil, err
		}
		if err := w.put(tx, "group_accounts", map[string]any{
			"system_account_id": seed.owner, "group_id": seed.group, "account_id": seed.id,
			"account_authorization_id":     nil,
			"local_priority":               seed.priority,
			"local_super_priority_enabled": boolInt(seed.super && seed.group == groups["high"]),
			"local_fallback_enabled":       boolInt(seed.fallback && seed.group == groups["high"]),
			"enabled":                      boolInt(seed.status != mockAccountStatusDisabled),
			"created_at":                   now, "updated_at": now,
		}); err != nil {
			return nil, err
		}
		models := w.accountModels(seed, guard)
		for _, model := range models {
			if err := w.put(tx, "account_supported_models", map[string]any{
				"account_id": seed.id, "provider_code": profile.provider, "model": model,
				"created_at": now,
			}); err != nil {
				return nil, err
			}
		}
		for _, mapping := range seed.mappings {
			source := mapping.sourceModel
			if source == "" {
				source = guard.catalog.modelAt(0)
			}
			upstream := mapping.upstream
			if upstream == "" {
				upstream = guard.catalog.modelAt(1)
			}
			if err := w.put(tx, "account_model_mappings", map[string]any{
				"account_id": seed.id, "provider_code": profile.provider,
				"source_model": source, "source_endpoint_family": mapping.sourceFamily,
				"upstream_model": upstream, "upstream_endpoint_family": mapping.upstreamFam,
				"enabled": boolInt(mapping.enabled), "created_at": now, "updated_at": now,
			}); err != nil {
				return nil, err
			}
		}
		for _, tag := range seed.tags {
			tagID := CleanupIDPrefix + "tag_" + tag
			if err := w.put(tx, "account_tags", map[string]any{
				"id": tagID, "system_account_id": seed.owner, "name": CleanupNamePrefix + tag,
				"created_at": now, "updated_at": now,
			}); err != nil {
				return nil, err
			}
			if err := w.put(tx, "account_tag_bindings", map[string]any{
				"account_id": seed.id, "tag_id": tagID, "system_account_id": seed.owner,
				"created_at": now,
			}); err != nil {
				return nil, err
			}
		}
	}
	return seeds, nil
}

// accountModels 解析一条账户的支持模型：目录模型按槽位取，附加模型直接拼接。
func (w *businessWriter) accountModels(seed *mockAccountSeed, guard businessGuard) []string {
	var models []string
	seen := map[string]bool{}
	for _, slot := range seed.modelSlots {
		model := guard.catalog.modelAt(slot)
		if !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	for _, model := range seed.extraModels {
		if !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	return models
}

// accountCredentials 生成账户凭据信封、指纹与掩码。
func (w *businessWriter) accountCredentials(seed *mockAccountSeed, index int) (string, any, string, error) {
	if seed.typ == "oauth" {
		accessToken := "mockdata-oauth-access-" + seed.slug + "-" + strings.Repeat("a", 32)
		payload := map[string]any{
			"access_token":  accessToken,
			"refresh_token": "mockdata-oauth-refresh-" + seed.slug + "-" + strings.Repeat("r", 32),
			"client_id":     "mockdata-openai-oauth-client",
			"account_id":    "mockdata-openai-user-" + seed.slug,
			"expires_at":    w.at(6 * time.Hour),
			"base_url":      "https://api.openai.com/v1",
		}
		sealed, err := w.seal(payload)
		if err != nil {
			return "", nil, "", err
		}
		return sealed, secretHash(accessToken), maskSecret(accessToken), nil
	}
	primary := "sk-mockdata-admin-" + seed.slug + "-" + strings.Repeat("x", 24)
	payload := map[string]any{
		"api_key":  primary,
		"base_url": "https://api.openai.com/v1",
	}
	if seed.keyPool > 1 {
		keys := make([]string, 0, seed.keyPool)
		for keyIndex := 1; keyIndex <= seed.keyPool; keyIndex++ {
			keys = append(keys, fmt.Sprintf("sk-mockdata-admin-%s-%d-%s", seed.slug, keyIndex, strings.Repeat("m", 20)))
		}
		payload["api_key"] = keys[0]
		payload["api_keys"] = keys
		payload["api_key_strategy"] = seed.keyStrategy
		payload["api_key_weights"] = []int{6, 3, 1, 1}
		payload["error_handling_rules"] = []any{
			map[string]any{
				"enabled": true, "name": "Mockdata 429 限流冷却", "priority": 1,
				"status_codes": []any{429}, "action": "rate_limited",
				"reset_strategy": "duration", "duration_hours": 2,
				"description": "Mockdata key 级限流规则",
			},
			map[string]any{
				"enabled": true, "name": "Mockdata 5xx 临时停调", "priority": 2,
				"status_codes": []any{500, 502, 503}, "action": "temp_unschedulable",
				"description": "Mockdata key 级临时停调规则",
			},
		}
		payload["response_inspection_rules"] = []any{
			map[string]any{
				"enabled": true, "name": "Mockdata 输出污染切号", "priority": 10,
				"match":  map[string]any{"outputTextIncludes": []any{"Mockdata 广告污染"}},
				"action": "retry_next_account",
				"notes":  "Mockdata 账户级响应检查规则",
			},
		}
	}
	sealed, err := w.seal(payload)
	if err != nil {
		return "", nil, "", err
	}
	return sealed, secretHash(primary), maskSecret(primary), nil
}

// accountSchedule 生成时间计划 JSON 与下次检查时间；空计划写 NULL。
func (w *businessWriter) accountSchedule(seed *mockAccountSeed) (any, any, error) {
	if seed.schedule == "" {
		return nil, nil, nil
	}
	type window struct {
		DaysOfWeek []int  `json:"daysOfWeek"`
		Start      string `json:"start"`
		End        string `json:"end"`
	}
	type dateRange struct {
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
	}
	type schedule struct {
		Enabled   bool       `json:"enabled"`
		Timezone  string     `json:"timezone"`
		Mode      string     `json:"mode"`
		Windows   []window   `json:"windows"`
		DateRange *dateRange `json:"dateRange,omitempty"`
	}
	allDays := []int{1, 2, 3, 4, 5, 6, 7}
	document := schedule{
		Enabled: true, Timezone: "UTC", Mode: "allow_windows",
		Windows: []window{
			{DaysOfWeek: allDays, Start: "00:00", End: "12:00"},
			{DaysOfWeek: allDays, Start: "12:00", End: "00:00"},
		},
	}
	nextCheck := w.at(12 * time.Hour)
	if seed.schedule == "inactive" {
		document.Windows = []window{{DaysOfWeek: allDays, Start: "09:00", End: "18:00"}}
		document.DateRange = &dateRange{StartDate: w.dateKey(-30), EndDate: w.dateKey(-1)}
		nextCheck = w.at(7 * 24 * time.Hour)
	}
	encoded, err := w.putJSON(document)
	if err != nil {
		return nil, nil, err
	}
	return encoded, nextCheck, nil
}

// clientCompatibility 归一化客户端兼容值：空值取 openai_standard。
func clientCompatibility(value string) string {
	if strings.TrimSpace(value) == "" {
		return "openai_standard"
	}
	return value
}

// balanceConfig 生成余额查询配置（未开启时保留 schema 默认空对象）。
func balanceConfig(enabled bool) string {
	if !enabled {
		return "{}"
	}
	return `{"adapter":"builtin","intervalMinutes":5}`
}

// refreshAfter 把布尔开关映射成余额刷新时间间隔。
func refreshAfter(enabled bool) time.Duration {
	if !enabled {
		return 0
	}
	return 5 * time.Minute
}

// nullTime 把偏移量投影成时间列：0 表示 NULL，非 0 表示基准时间加偏移。
func nullTime(w *businessWriter, offset time.Duration) any {
	if offset == 0 {
		return nil
	}
	return w.at(offset)
}

// nullString 把空串投影成 SQL NULL。
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// traceFor 生成带清理标识的错误 trace（无错误码时返回 NULL）。
func traceFor(errorCode string) any {
	if errorCode == "" {
		return nil
	}
	return CleanupTracePrefix + "trace-" + errorCode
}

// maskSecret 复刻 gateway 的 maskSecret：短值保留头尾各 2 位，长值 6/4 位。
func maskSecret(value string) string {
	runes := []rune(value)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) == 1:
		return string(runes[:1]) + "***"
	case len(runes) <= 10:
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	default:
		return string(runes[:6]) + "***" + string(runes[len(runes)-4:])
	}
}

// seedAccountProjections 写入账户名检索投影（document + 1..3 gram terms）。
//
// 为什么造数要写这两张投影表：gateway 的账户列表关键字检索走
// account_name_search_documents.normalized_name + account_name_search_terms，
// 不写它们时页面按关键字搜不到任何 mock 账户（写路径由账户增改维护，造数直接
// 落库必须自己补齐）。
func (w *businessWriter) seedAccountProjections(tx *sql.Tx, guard businessGuard, accounts []mockAccountSeed) error {
	now := w.stamp()
	for _, account := range accounts {
		normalized := strings.TrimSpace(account.name)
		if err := w.put(tx, "account_name_search_documents", map[string]any{
			"account_id": account.id, "system_account_id": account.owner,
			"normalized_name": normalized, "updated_at": now,
		}); err != nil {
			return err
		}
		for _, term := range mockNameTerms(normalized) {
			if err := w.put(tx, "account_name_search_terms", map[string]any{
				"account_id": account.id, "system_account_id": account.owner,
				"term": term, "created_at": now,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// mockNameTerms 复刻 gateway buildAccountNameSearchTerms 的 1..3 gram 集合。
// 造数名称只含 CJK 与 ASCII，不含 NFKC 兼容字符，故这里只做 trim。
func mockNameTerms(name string) []string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return nil
	}
	seen := map[string]bool{}
	terms := []string{}
	for length := 1; length <= 3; length++ {
		for index := 0; index+length <= len(runes); index++ {
			term := string(runes[index : index+length])
			if strings.TrimSpace(term) == "" || seen[term] {
				continue
			}
			seen[term] = true
			terms = append(terms, term)
		}
	}
	return terms
}

// mockKeyPoolKeys 复刻 accountCredentials 里多 Key 池的明文键列表，供运行态
// 指纹与凭据保持一致。
func mockKeyPoolKeys(seed *mockAccountSeed) []string {
	keys := make([]string, 0, seed.keyPool)
	for index := 1; index <= seed.keyPool; index++ {
		keys = append(keys, fmt.Sprintf("sk-mockdata-admin-%s-%d-%s", seed.slug, index, strings.Repeat("m", 20)))
	}
	return keys
}

// seedAccountRuntimeSamples 写入账户运行态样本：Key 级运行态、锁、熔断事件与
// outbox、模型质量策略/计划/隔离、体检默认模型、账户测试任务与时间计划事件。
func (w *businessWriter) seedAccountRuntimeSamples(tx *sql.Tx, guard businessGuard, accounts []mockAccountSeed) error {
	byID := map[string]mockAccountSeed{}
	for _, account := range accounts {
		byID[account.id] = account
	}
	primary := byID[CleanupIDPrefix+"acc_primary"]
	multiKey := byID[CleanupIDPrefix+"acc_multikey"]
	errorAccount := byID[CleanupIDPrefix+"acc_error"]
	rateLimited := byID[CleanupIDPrefix+"acc_rate_limited"]
	temporary := byID[CleanupIDPrefix+"acc_temporary"]
	standard := byID[CleanupIDPrefix+"acc_standard"]
	oauthAccount := byID[CleanupIDPrefix+"acc_oauth"]

	now := w.stamp()
	// Key 级运行态：temporary_unavailable / rate_limited / error 三种（覆盖断言
	// 需要 ≥2 种不同 status）。
	keyStates := []struct {
		index     int
		status    string
		failures  int
		successes int
		message   string
		code      string
		probeMin  int
	}{
		{index: 1, status: mockAccountStatusTemporary, failures: 2, successes: 8,
			message: "Mockdata 模拟 key 级 503 冷却", code: "service_unavailable", probeMin: 5},
		{index: 2, status: mockAccountStatusRateLimit, failures: 5, successes: 21,
			message: "Mockdata 模拟 key 级限流", code: "rate_limit_exceeded", probeMin: 45},
		{index: 3, status: mockAccountStatusError, failures: 7, successes: 3,
			message: "Mockdata 模拟 key 级认证异常", code: "invalid_api_key", probeMin: 15},
	}
	keys := mockKeyPoolKeys(&multiKey)
	for _, state := range keyStates {
		if state.index >= len(keys) {
			continue
		}
		nextProbe := w.at(time.Duration(state.probeMin) * time.Minute)
		if err := w.put(tx, "account_api_key_runtime_states", map[string]any{
			"id":                fmt.Sprintf("%saccount_api_key_runtime_%d", CleanupIDPrefix, state.index),
			"system_account_id": multiKey.owner, "account_id": multiKey.id,
			"key_fingerprint": secretHash(keys[state.index]), "key_index": state.index,
			"credential_revision": "mockdata",
			"status":              state.status, "failure_count": state.failures,
			"consecutive_failures": state.failures, "success_count": state.successes,
			"cooldown_until": nextProbe, "next_probe_at": nextProbe,
			"probe_backoff_seconds": state.probeMin * 60,
			"recovery_started_at":   now,
			"last_attempt_at":       now, "last_success_at": w.at(-2 * time.Minute),
			"last_failure_at": now, "last_error_code": state.code,
			"last_error_message": state.message, "last_trace_id": traceFor(state.code),
			"last_probe_at": w.at(-1 * time.Minute), "created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}

	// 账户锁状态：一条正常未锁、一条锁住等待，覆盖两个 CHECK 分支。
	if err := w.put(tx, "account_lock_states", map[string]any{
		"account_id": primary.id, "enabled": 0, "lock_state": "UNLOCKED",
		"lock_death_timeout_seconds": 300, "lock_retry_interval_seconds": 5,
		"generation": 0, "updated_at": now,
	}); err != nil {
		return err
	}
	if err := w.put(tx, "account_lock_states", map[string]any{
		"account_id": errorAccount.id, "enabled": 1, "lock_state": "LOCKED_IDLE",
		"lock_death_timeout_seconds": 300, "lock_retry_interval_seconds": 5,
		"incident_id":         CleanupTracePrefix + "lock-incident-error",
		"generation":          1,
		"incident_started_at": w.at(-2 * time.Hour), "deadline_at": w.at(3 * time.Hour),
		"original_status":  mockAccountStatusError,
		"provenance":       "mockdata",
		"next_retry_at_ms": w.now.Add(5 * time.Minute).UnixMilli(),
		"lease_id":         CleanupIDPrefix + "lock_lease_error",
		"lease_until_ms":   w.now.Add(10 * time.Minute).UnixMilli(),
		"updated_at":       now,
	}); err != nil {
		return err
	}

	// 熔断事件：账户级 OPEN、Key 级 HALF_OPEN、账户级 CLOSED（含保留窗口）。
	openUntil := w.now.Add(10 * time.Minute).UnixMilli()
	if err := w.put(tx, "account_circuit_incidents", map[string]any{
		"circuit_scope_key":                       CleanupIDPrefix + "circuit_account_temporary",
		"account_id":                              temporary.id,
		"account_runtime_key":                     CleanupIDPrefix + "runtime_" + temporary.id,
		"scope_kind":                              "account",
		"incident_id":                             CleanupIDPrefix + "incident_open",
		"caused_by_terminal_outcome_id":           CleanupIDPrefix + "outcome_open",
		"state":                                   "OPEN",
		"failure_scope":                           "account",
		"generation":                              2,
		"dispatch_revision":                       1,
		"ledger_revision":                         3,
		"projected_ledger_revision":               3,
		"transition_id":                           CleanupIDPrefix + "transition_open",
		"cooldown_observation_generation":         0,
		"open_until_ms":                           openUntil,
		"next_transition_at_ms":                   openUntil,
		"upstream_attempt_observed":               0,
		"backoff_level":                           1,
		"consecutive_failures":                    2,
		"confirmation_failures_required":          3,
		"confirmation_failure_evidence_keys_json": "[]",
		"child_incident_ids_json":                 "[]",
		"recovering_successes":                    0,
		"last_failure_class":                      "timeout_before_complete",
		"created_at_ms":                           w.now.Add(-8 * time.Minute).UnixMilli(),
		"updated_at_ms":                           w.now.UnixMilli(),
	}); err != nil {
		return err
	}
	keyFingerprint := secretHash(keys[1])
	if err := w.put(tx, "account_circuit_incidents", map[string]any{
		"circuit_scope_key":                       CleanupIDPrefix + "circuit_key_multikey",
		"account_id":                              multiKey.id,
		"account_runtime_key":                     CleanupIDPrefix + "runtime_" + multiKey.id,
		"scope_kind":                              "key",
		"key_fingerprint":                         keyFingerprint,
		"incident_id":                             CleanupIDPrefix + "incident_half_open",
		"parent_incident_id":                      CleanupIDPrefix + "incident_open",
		"state":                                   "HALF_OPEN",
		"failure_scope":                           "key",
		"generation":                              1,
		"dispatch_revision":                       1,
		"ledger_revision":                         2,
		"projected_ledger_revision":               2,
		"transition_id":                           CleanupIDPrefix + "transition_half_open",
		"lease_id":                                CleanupIDPrefix + "circuit_lease_multikey",
		"lease_purpose":                           "half_open",
		"lease_owner_run_id":                      CleanupIDPrefix + "run_half_open",
		"lease_until_ms":                          w.now.Add(2 * time.Minute).UnixMilli(),
		"attempt_started_at_ms":                   w.now.Add(-30 * time.Second).UnixMilli(),
		"attempt_hard_deadline_ms":                w.now.Add(90 * time.Second).UnixMilli(),
		"upstream_attempt_observed":               1,
		"consecutive_failures":                    1,
		"confirmation_failures_required":          1,
		"confirmation_failure_evidence_keys_json": `["mockdata_key_failure"]`,
		"child_incident_ids_json":                 "[]",
		"recovering_successes":                    1,
		"last_failure_class":                      "explicit_policy",
		"created_at_ms":                           w.now.Add(-12 * time.Minute).UnixMilli(),
		"updated_at_ms":                           w.now.UnixMilli(),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "account_circuit_incidents", map[string]any{
		"circuit_scope_key":              CleanupIDPrefix + "circuit_account_rate_limited",
		"account_id":                     rateLimited.id,
		"account_runtime_key":            CleanupIDPrefix + "runtime_" + rateLimited.id,
		"scope_kind":                     "account",
		"incident_id":                    CleanupIDPrefix + "incident_closed",
		"state":                          "CLOSED",
		"failure_scope":                  "account",
		"generation":                     4,
		"dispatch_revision":              1,
		"ledger_revision":                6,
		"projected_ledger_revision":      6,
		"transition_id":                  CleanupIDPrefix + "transition_closed",
		"confirmation_failures_required": 2,
		"confirmation_failure_evidence_keys_json": "[]",
		"child_incident_ids_json":                 "[]",
		"retained_until_ms":                       w.now.Add(24 * time.Hour).UnixMilli(),
		"created_at_ms":                           w.now.Add(-3 * time.Hour).UnixMilli(),
		"updated_at_ms":                           w.now.Add(-2 * time.Hour).UnixMilli(),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "account_circuit_outbox", map[string]any{
		"event_id":       CleanupIDPrefix + "circuit_outbox_pending",
		"projection_key": "mockdata_circuit_projection",
		"dedupe_key":     CleanupTracePrefix + "dispatch-revision-1",
		"event_type":     "dispatch_revision_changed",
		"account_id":     primary.id, "account_runtime_key": CleanupIDPrefix + "runtime_" + primary.id,
		"transition_id": CleanupIDPrefix + "transition_dispatch", "dispatch_revision": 1,
		"status": "pending", "available_at_ms": w.now.UnixMilli(),
		"attempt_count": 0, "created_at_ms": w.now.Add(-time.Minute).UnixMilli(),
		"updated_at_ms": w.now.UnixMilli(),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "account_circuit_outbox", map[string]any{
		"event_id":       CleanupIDPrefix + "circuit_outbox_dispatched",
		"projection_key": "mockdata_circuit_projection",
		"dedupe_key":     CleanupTracePrefix + "incident-open-2",
		"event_type":     "incident_changed",
		"account_id":     temporary.id, "account_runtime_key": CleanupIDPrefix + "runtime_" + temporary.id,
		"circuit_scope_key": CleanupIDPrefix + "circuit_account_temporary",
		"incident_id":       CleanupIDPrefix + "incident_open",
		"transition_id":     CleanupIDPrefix + "transition_open",
		"dispatch_revision": 1, "generation": 2, "ledger_revision": 3,
		"status": "dispatched", "available_at_ms": w.now.Add(-7 * time.Minute).UnixMilli(),
		"attempt_count": 1, "acknowledged_at_ms": w.now.Add(-6 * time.Minute).UnixMilli(),
		"created_at_ms": w.now.Add(-8 * time.Minute).UnixMilli(),
		"updated_at_ms": w.now.Add(-6 * time.Minute).UnixMilli(),
	}); err != nil {
		return err
	}

	// 模型质量策略 / 计划 / 隔离样本（三张表都被 autofill 跳过，必须由本域写入）。
	for _, owner := range []string{guard.adminID, CleanupIDPrefix + "user_admin"} {
		if err := w.put(tx, "model_quality_policies", map[string]any{
			"system_account_id": owner, "revision": 1, "profile": "quick",
			"manual_enforcement_enabled": 1, "penalty_threshold": 70,
			"penalty_action": "fallback", "recovery_interval_minutes": 30,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	scheduleSeeds := []struct {
		id       string
		owner    string
		account  string
		enabled  int
		status   string
		interval int
	}{
		{id: CleanupIDPrefix + "quality_schedule_primary", owner: guard.adminID,
			account: primary.id, enabled: 1, status: "completed", interval: 60},
		{id: CleanupIDPrefix + "quality_schedule_error", owner: guard.adminID,
			account: errorAccount.id, enabled: 0, status: "failed", interval: 180},
	}
	for _, seed := range scheduleSeeds {
		account := byID[seed.account]
		model := guard.catalog.modelAt(0)
		if len(account.modelSlots) > 0 {
			model = guard.catalog.modelAt(account.modelSlots[0])
		}
		if err := w.put(tx, "model_quality_schedules", map[string]any{
			"id": seed.id, "system_account_id": seed.owner, "account_id": seed.account,
			"model": model, "interval_minutes": seed.interval, "profile": "quick",
			"penalty_threshold": 70, "penalty_action": "fallback",
			"recovery_interval_minutes": 30, "enabled": seed.enabled, "revision": 1,
			"next_run_at": w.at(1 * time.Hour), "last_run_id": CleanupIDPrefix + "quality_run_" + seed.status,
			"last_run_at": w.at(-2 * time.Hour), "last_run_status": seed.status,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	if err := w.put(tx, "account_quality_enforcements", map[string]any{
		"account_id": errorAccount.id, "system_account_id": guard.adminID,
		"enforcement_id": CleanupIDPrefix + "quality_enforcement_error",
		"generation":     2, "state": "active", "action": "fallback",
		"trigger_run_id": CleanupIDPrefix + "quality_run_failed",
		"config_source":  "manual", "config_source_id": nil, "policy_revision": 1,
		"profile": "quick", "penalty_threshold": 70, "recovery_interval_minutes": 30,
		"recovery_model":          guard.catalog.modelAt(0),
		"account_config_revision": 1, "before_status": mockAccountStatusError,
		"after_status": mockAccountStatusError, "fallback_was_enabled": 1,
		"super_priority_was_enabled": 0, "started_at": w.at(-2 * time.Hour),
		"recovery_due_at": w.at(28 * time.Minute),
		"created_at":      w.at(-2 * time.Hour), "updated_at": now,
	}); err != nil {
		return err
	}
	if err := w.put(tx, "account_quality_enforcements", map[string]any{
		"account_id": standard.id, "system_account_id": guard.adminID,
		"enforcement_id": CleanupIDPrefix + "quality_enforcement_cleared",
		"generation":     1, "state": "cleared", "action": "quality_isolate",
		"trigger_run_id": CleanupIDPrefix + "quality_run_completed",
		"config_source":  "schedule", "config_source_id": CleanupIDPrefix + "quality_schedule_primary",
		"policy_revision": 1, "profile": "quick", "penalty_threshold": 70,
		"recovery_interval_minutes": 30, "account_config_revision": 1,
		"before_status": mockAccountStatusActive, "after_status": mockAccountStatusActive,
		"fallback_was_enabled": 0, "super_priority_was_enabled": 0,
		"started_at": w.at(-26 * time.Hour), "cleared_at": w.at(-25 * time.Hour),
		"created_at": w.at(-26 * time.Hour), "updated_at": w.at(-25 * time.Hour),
	}); err != nil {
		return err
	}

	// 体检默认模型：账户级 + 系统级（系统级按 provider 主键，只能一行）。
	for _, owner := range []string{guard.adminID, CleanupIDPrefix + "user_admin"} {
		if err := w.put(tx, "provider_default_health_check_models", map[string]any{
			"system_account_id": owner, "provider_code": guard.catalog.mainProvider,
			"model": mockCustomModelLongContext, "created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	if err := w.put(tx, "provider_system_default_health_check_models", map[string]any{
		"provider_code": guard.catalog.mainProvider, "model": mockCustomModelLongContext,
		"created_at": now, "updated_at": now,
	}); err != nil {
		return err
	}

	// 账户测试任务三态 + 两个会话 + 会话任务链接。
	taskSeeds := []struct {
		suffix  string
		account mockAccountSeed
		status  string
		message string
		result  string
		errMsg  string
		started time.Duration
		ended   time.Duration
	}{
		{suffix: "success", account: primary, status: "success", message: "Mockdata 测试通过",
			result:  `{"ok":true,"latencyMs":820,"model":"` + guard.catalog.modelAt(0) + `"}`,
			started: -40 * time.Minute, ended: -39 * time.Minute},
		{suffix: "failed", account: errorAccount, status: "failed", message: "Mockdata 测试失败",
			errMsg: "Mockdata 模拟 401 认证失败", started: -35 * time.Minute, ended: -34 * time.Minute},
		{suffix: "canceled", account: temporary, status: "canceled", message: "Mockdata 测试已取消",
			started: -30 * time.Minute, ended: -29 * time.Minute},
	}
	for _, seed := range taskSeeds {
		profile, _ := guard.catalog.profileFor(guard.catalog.mainProvider, "api_key")
		if err := w.put(tx, "account_test_tasks", map[string]any{
			"id": CleanupIDPrefix + "test_task_" + seed.suffix, "account_id": seed.account.id,
			"account_name": seed.account.name, "provider_code": profile.provider,
			"provider_protocol_profile_id": profile.id, "protocol_code": profile.protocol,
			"protocol_version": profile.protocolVer, "account_type": seed.account.typ,
			"request_system_account_id": guard.adminID, "request_role": "super_admin",
			"diagnostics": "full", "model": guard.catalog.modelAt(0),
			"test_endpoint_mode": seed.account.mode, "status": seed.status,
			"status_message": seed.message, "result_json": nullString(seed.result),
			"error_message":      nullString(seed.errMsg),
			"cancel_requested":   boolInt(seed.status == "canceled"),
			"queued_at":          w.at(seed.started - 2*time.Minute),
			"queued_deadline_at": w.at(seed.started + 8*time.Minute),
			"started_at":         w.at(seed.started), "finished_at": w.at(seed.ended),
			"created_at": w.at(seed.started - 2*time.Minute), "updated_at": w.at(seed.ended),
		}); err != nil {
			return err
		}
	}
	sessionSeeds := []struct {
		suffix   string
		status   string
		reason   any
		finished bool
	}{
		{suffix: "completed", status: "completed", finished: true},
		{suffix: "running", status: "running"},
	}
	for _, seed := range sessionSeeds {
		columns := map[string]any{
			"id":                        CleanupIDPrefix + "test_session_" + seed.suffix,
			"request_system_account_id": guard.adminID, "request_role": "super_admin",
			"status": seed.status, "cancel_reason": seed.reason,
			"last_heartbeat_at": w.at(-5 * time.Minute),
			"created_at":        w.at(-45 * time.Minute), "updated_at": w.at(-5 * time.Minute),
		}
		if seed.finished {
			columns["finished_at"] = w.at(-20 * time.Minute)
		}
		if err := w.put(tx, "account_test_sessions", columns); err != nil {
			return err
		}
	}
	for _, link := range []struct{ session, task string }{
		{session: "completed", task: "success"},
		{session: "completed", task: "failed"},
		{session: "running", task: "canceled"},
	} {
		if err := w.put(tx, "account_test_session_tasks", map[string]any{
			"session_id": CleanupIDPrefix + "test_session_" + link.session,
			"task_id":    CleanupIDPrefix + "test_task_" + link.task,
			"created_at": w.at(-40 * time.Minute),
		}); err != nil {
			return err
		}
	}

	// 列表投影依赖健康行：该表的 dependency_name 被 CHECK 固定为 'runtime_state'，
	// 无法带造数前缀，只能靠 INSERT OR REPLACE 在同一主键上收敛（不随运行累积）；
	// SQLite 读路径不消费它（投影只服务 PostgreSQL 性能路径），写 healthy 是为了让
	// 覆盖门禁不把 business 库的这张表算成漏写。
	if err := w.put(tx, "account_list_availability_projection_dependency_health", map[string]any{
		"dependency_name": "runtime_state", "state": "healthy", "generation": 1,
		"reason": "mockdata 造数写入的投影依赖健康样本", "updated_at": now,
	}); err != nil {
		return err
	}

	// 账户时间计划事件：命中激活与命中禁用各一条。
	for _, event := range []struct {
		suffix string
		seed   mockAccountSeed
		status string
		offset time.Duration
	}{
		{suffix: "primary", seed: primary, status: "active", offset: -90 * time.Minute},
		{suffix: "scheduled", seed: byID[CleanupIDPrefix+"acc_scheduled"], status: "disabled", offset: -30 * time.Minute},
		{suffix: "manager_burst", seed: byID[CleanupIDPrefix+"acc_manager_burst"], status: "active", offset: -20 * time.Minute},
	} {
		if err := w.put(tx, "account_schedule_status_events", map[string]any{
			"event_key":  CleanupIDPrefix + "account_schedule_" + event.suffix,
			"account_id": event.seed.id, "status": event.status,
			"executed_at": w.at(event.offset),
		}); err != nil {
			return err
		}
	}
	if err := w.put(tx, "account_schedule_status_events", map[string]any{
		"event_key":  CleanupIDPrefix + "account_schedule_oauth",
		"account_id": oauthAccount.id, "status": "active", "executed_at": w.at(-2 * time.Hour),
	}); err != nil {
		return err
	}
	return nil
}

// mockTeamSeed 是一条团队样本。
type mockTeamSeed struct {
	id      string
	name    string
	desc    string
	status  string
	members []string
	removed []string
}

// seedTeams 写入团队与成员（含停用团队与已移除的历史成员）。
func (w *businessWriter) seedTeams(tx *sql.Tx, guard businessGuard, users []mockUserSeed) (map[string]string, error) {
	dev := CleanupIDPrefix + "user_dev"
	ops := CleanupIDPrefix + "user_ops"
	tester := CleanupIDPrefix + "user_tester"
	finance := CleanupIDPrefix + "user_finance"
	viewer := CleanupIDPrefix + "user_viewer"
	seeds := []mockTeamSeed{
		{id: CleanupIDPrefix + "team_dev", name: CleanupNamePrefix + "研发协作团队",
			desc: "Mockdata 研发协作团队，承接团队级分组授权", status: "active",
			members: []string{dev, tester, ops}},
		{id: CleanupIDPrefix + "team_ops", name: CleanupNamePrefix + "运维保障团队",
			desc: "Mockdata 运维保障团队，承接备用分组授权", status: "active",
			members: []string{ops, viewer}},
		{id: CleanupIDPrefix + "team_disabled", name: CleanupNamePrefix + "停用历史团队",
			desc: "Mockdata 停用团队，用于状态展示与历史成员样本", status: "disabled",
			members: []string{finance}, removed: []string{tester}},
	}
	now := w.stamp()
	ids := map[string]string{}
	for _, seed := range seeds {
		if err := w.put(tx, "system_teams", map[string]any{
			"id": seed.id, "name": seed.name, "description": seed.desc, "status": seed.status,
			"created_by": guard.adminID, "created_at": now, "updated_at": now,
		}); err != nil {
			return nil, err
		}
		ids[strings.TrimPrefix(seed.id, CleanupIDPrefix+"team_")] = seed.id
		for index, member := range seed.members {
			if err := w.put(tx, "system_team_members", map[string]any{
				"id":      fmt.Sprintf("%steam_member_%s_%d", CleanupIDPrefix, seed.id, index),
				"team_id": seed.id, "system_account_id": member, "member_role": "member",
				"status": "active", "joined_at": w.at(-30 * 24 * time.Hour),
				"created_by": guard.adminID, "created_at": w.at(-30 * 24 * time.Hour),
				"updated_at": now,
			}); err != nil {
				return nil, err
			}
		}
		for index, member := range seed.removed {
			if err := w.put(tx, "system_team_members", map[string]any{
				"id":      fmt.Sprintf("%steam_member_removed_%s_%d", CleanupIDPrefix, seed.id, index),
				"team_id": seed.id, "system_account_id": member, "member_role": "member",
				"status": "removed", "joined_at": w.at(-60 * 24 * time.Hour),
				"removed_at": w.at(-7 * 24 * time.Hour),
				"created_by": guard.adminID, "created_at": w.at(-60 * 24 * time.Hour),
				"updated_at": w.at(-7 * 24 * time.Hour),
			}); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

// mockQuotaLimits 复刻 Node quotaLimits：hourly/daily/monthly 三段额度窗口。
func mockQuotaLimits(hourly, daily, monthly int) map[string]any {
	return map[string]any{
		"hourly":  map[string]any{"enabled": true, "hours": 1, "limit": hourly},
		"daily":   map[string]any{"enabled": true, "limit": daily},
		"monthly": map[string]any{"enabled": true, "limit": monthly},
	}
}

// mockAuthSeed 是一条授权样本（Node createAuthorizations 的样本清单）。
type mockAuthSeed struct {
	key          string
	resourceType string
	resourceID   string
	owner        string
	grantee      string
	team         string
	status       string
	remark       string
	expiresIn    time.Duration
	limits       map[string]any
}

// mockInstanceSeed 是一条待创建的授权账户实例（Node authorizationInstanceAccount）。
type mockInstanceSeed struct {
	authorizationID string
	sourceAccountID string
	owner           string
	grantee         string
}

// seedAuthorizations 写入授权三表：业务授予（grants）、运行时投影
// （resource_authorizations）与来源（sources）。
//
// 团队授权的成员扇出与 gateway 的读语义一致：每个在册成员各一条运行时行，来源
// 记为 team；被授权人是资源归属人自己的样本一律不生成（Node 的
// assertNoMockSelfAuthorizations 是同一约束）。
func (w *businessWriter) seedAuthorizations(tx *sql.Tx, guard businessGuard, users []mockUserSeed, groups map[string]string, accounts []mockAccountSeed, teams map[string]string) ([]mockInstanceSeed, error) {
	byID := map[string]mockAccountSeed{}
	for _, account := range accounts {
		byID[account.id] = account
	}
	admin := guard.adminID
	dev := CleanupIDPrefix + "user_dev"
	ops := CleanupIDPrefix + "user_ops"
	tester := CleanupIDPrefix + "user_tester"
	finance := CleanupIDPrefix + "user_finance"
	viewer := CleanupIDPrefix + "user_viewer"
	devTeam := teams["dev"]
	opsTeam := teams["ops"]
	teamMembers := map[string][]string{
		devTeam: {dev, tester, ops},
		opsTeam: {ops, viewer},
	}
	seeds := []mockAuthSeed{
		{key: "dev_group_admin", resourceType: "group", resourceID: groups["dev_granted"], owner: dev,
			grantee: admin, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发分组授权给超级管理员", limits: mockQuotaLimits(12, 96, 360)},
		{key: "ops_group_admin", resourceType: "group", resourceID: groups["ops_granted"], owner: ops,
			grantee: admin, status: mockAuthorizationPaused,
			remark: CleanupNamePrefix + "运维分组授权给超级管理员后暂停", limits: mockQuotaLimits(9, 72, 260)},
		{key: "tester_group_admin", resourceType: "group", resourceID: groups["tester_granted"], owner: tester,
			grantee: admin, status: mockAuthorizationExpired, expiresIn: -3 * 24 * time.Hour,
			remark: CleanupNamePrefix + "测试分组授权给超级管理员后过期", limits: mockQuotaLimits(5, 30, 100)},
		{key: "dev_account_admin", resourceType: "account", resourceID: CleanupIDPrefix + "acc_dev_shared",
			owner: dev, grantee: admin, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发账户授权给超级管理员", limits: mockQuotaLimits(11, 88, 320)},
		{key: "ops_account_admin", resourceType: "account", resourceID: CleanupIDPrefix + "acc_ops_shared",
			owner: ops, grantee: admin, status: mockAuthorizationPaused,
			remark: CleanupNamePrefix + "运维账户授权给超级管理员后暂停", limits: mockQuotaLimits(7, 56, 210)},
		{key: "tester_account_admin", resourceType: "account", resourceID: CleanupIDPrefix + "acc_tester_shared",
			owner: tester, grantee: admin, status: mockAuthorizationExpired, expiresIn: -4 * 24 * time.Hour,
			remark: CleanupNamePrefix + "测试账户授权给超级管理员后过期", limits: mockQuotaLimits(4, 24, 96)},
		{key: "main_group_dev", resourceType: "group", resourceID: groups["main"], owner: admin,
			grantee: dev, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发用户可调用主力分组", limits: mockQuotaLimits(25, 200, 800)},
		// 运维用户的高并发分组样本指向管理员自有高并发分组：同一 (resource, grantee)
		// 在运行时授权表上是唯一的（idx_resource_authorizations_user_unique），
		// 与研发团队那条高并发分组授权错开资源才能各自落一行。
		{key: "high_group_ops", resourceType: "group", resourceID: groups["manager_high"],
			owner: CleanupIDPrefix + "user_admin", grantee: ops, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "运维用户可调用管理员高并发分组", limits: mockQuotaLimits(20, 160, 620)},
		{key: "backup_group_viewer", resourceType: "group", resourceID: groups["backup"], owner: admin,
			grantee: viewer, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "观察用户可调用备用分组", limits: mockQuotaLimits(6, 36, 120)},
		{key: "experiment_group_tester", resourceType: "group", resourceID: groups["experiment"], owner: admin,
			grantee: tester, status: mockAuthorizationActive, expiresIn: 14 * 24 * time.Hour,
			remark: CleanupNamePrefix + "测试用户可调用实验分组", limits: mockQuotaLimits(8, 48, 160)},
		{key: "oauth_group_finance", resourceType: "group", resourceID: groups["oauth"], owner: admin,
			grantee: finance, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "财务用户可调用 OAuth 分组", limits: mockQuotaLimits(7, 42, 140)},
		{key: "backup_group_devteam", resourceType: "group", resourceID: groups["backup"], owner: admin,
			team: devTeam, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发团队可调用备用分组", limits: mockQuotaLimits(18, 120, 500)},
		{key: "oauth_group_opsteam", resourceType: "group", resourceID: groups["oauth"], owner: admin,
			team: opsTeam, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "运维团队可调用 OAuth 分组", limits: mockQuotaLimits(12, 80, 300)},
		{key: "high_group_devteam", resourceType: "group", resourceID: groups["high"], owner: admin,
			team: devTeam, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发团队可调用高并发分组", limits: mockQuotaLimits(22, 180, 720)},
		{key: "proxied_account_ops", resourceType: "account", resourceID: CleanupIDPrefix + "acc_proxied",
			owner: admin, grantee: ops, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "运维用户可调用带代理账户", limits: mockQuotaLimits(8, 60, 200)},
		{key: "burstfast_account_dev", resourceType: "account", resourceID: CleanupIDPrefix + "acc_burst_fast",
			owner: admin, grantee: dev, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发用户可调用高并发快响账户", limits: mockQuotaLimits(14, 110, 420)},
		{key: "oauth_account_finance", resourceType: "account", resourceID: CleanupIDPrefix + "acc_oauth",
			owner: admin, grantee: finance, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "财务用户可调用 OAuth 主力账户", limits: mockQuotaLimits(6, 40, 150)},
		{key: "burstimage_account_viewer", resourceType: "account", resourceID: CleanupIDPrefix + "acc_burst_image",
			owner: admin, grantee: viewer, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "观察用户可调用高并发图像账户", limits: mockQuotaLimits(5, 28, 90)},
		{key: "primary_account_devteam", resourceType: "account", resourceID: CleanupIDPrefix + "acc_primary",
			owner: admin, team: devTeam, status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "研发团队可调用主力账户", limits: mockQuotaLimits(10, 80, 240)},
		{key: "burstfallback_account_opsteam", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_burst_fallback", owner: admin, team: opsTeam,
			status: mockAuthorizationActive,
			remark: CleanupNamePrefix + "运维团队可调用高并发备用账户", limits: mockQuotaLimits(9, 66, 220)},
		{key: "experiment_group_opsteam_paused", resourceType: "group", resourceID: groups["experiment"],
			owner: admin, team: opsTeam, status: mockAuthorizationPaused,
			remark: CleanupNamePrefix + "运维团队暂停实验分组授权", limits: mockQuotaLimits(6, 36, 120)},
		{key: "normal_account_opsteam_revoked", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_normal", owner: admin, team: opsTeam,
			status: mockAuthorizationRevoked,
			remark: CleanupNamePrefix + "运维团队已回收普通账户授权", limits: mockQuotaLimits(5, 30, 100)},
		{key: "oauthbackup_account_viewer_returned", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_oauth_backup", owner: admin, grantee: viewer,
			status: mockAuthorizationReturned,
			remark: CleanupNamePrefix + "观察用户已归还 OAuth 账户授权", limits: mockQuotaLimits(2, 10, 40)},
		{key: "experiment_group_finance_paused", resourceType: "group", resourceID: groups["experiment"],
			owner: admin, grantee: finance, status: mockAuthorizationPaused,
			remark: CleanupNamePrefix + "财务用户暂停授权", limits: mockQuotaLimits(4, 20, 80)},
		{key: "main_group_finance_expired", resourceType: "group", resourceID: groups["main"], owner: admin,
			grantee: finance, status: mockAuthorizationExpired, expiresIn: -2 * 24 * time.Hour,
			remark: CleanupNamePrefix + "财务用户已过期主力分组授权", limits: mockQuotaLimits(3, 18, 60)},
		{key: "fallback_account_tester_expired", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_fallback", owner: admin, grantee: tester,
			status: mockAuthorizationExpired, expiresIn: -24 * time.Hour,
			remark: CleanupNamePrefix + "测试用户已过期账户授权", limits: mockQuotaLimits(3, 16, 50)},
		{key: "normal_image_account_viewer_revoked", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_image", owner: admin, grantee: viewer,
			status: mockAuthorizationRevoked,
			remark: CleanupNamePrefix + "观察用户已回收图像生成账户授权", limits: mockQuotaLimits(2, 12, 40)},
		{key: "temporary_account_viewer_revoked", resourceType: "account",
			resourceID: CleanupIDPrefix + "acc_temporary", owner: admin, grantee: viewer,
			status: mockAuthorizationRevoked,
			remark: CleanupNamePrefix + "观察用户已回收临时账户授权", limits: mockQuotaLimits(1, 8, 24)},
	}
	now := w.stamp()
	var instances []mockInstanceSeed
	for _, seed := range seeds {
		if seed.status == mockAuthorizationActive && seed.grantee != "" && seed.grantee == seed.owner {
			return nil, fmt.Errorf("mockdata business 生成自授权样本：%s", seed.key)
		}
		limits, err := w.putJSON(seed.limits)
		if err != nil {
			return nil, err
		}
		expiresAt := nullTime(w, seed.expiresIn)
		revokedBy, revokedAt, revokedReason := any(nil), any(nil), any(nil)
		sourceStatus, endedAt, endedReason := "active", any(nil), any(nil)
		switch seed.status {
		case mockAuthorizationActive, mockAuthorizationPaused:
		case mockAuthorizationExpired:
			sourceStatus, endedReason = "superseded", "authorization_expired"
			endedAt = w.at(seed.expiresIn)
			revokedBy, revokedAt, revokedReason = guard.adminID, w.at(seed.expiresIn), "authorization_expired"
		case mockAuthorizationRevoked:
			sourceStatus, revokedBy, revokedAt = "revoked", guard.adminID, w.at(-6*time.Hour)
			endedAt, endedReason = w.at(-6*time.Hour), "authorization_revoked"
		case mockAuthorizationReturned:
			sourceStatus, endedAt, endedReason = "superseded", w.at(-2*time.Hour), "authorization_returned"
		}
		if seed.expiresIn > 0 {
			expiresAt = w.at(seed.expiresIn)
		}
		activatedAt := any(nil)
		if seed.status != mockAuthorizationReturned {
			activatedAt = w.at(-20 * 24 * time.Hour)
		}

		granteeType, granteeID, granteeTeam := "system_account", any(seed.grantee), any(nil)
		if seed.team != "" {
			granteeType, granteeID, granteeTeam = "team", nil, seed.team
		}
		// 业务授予行：授权管理页的列表数据源。
		if err := w.put(tx, "resource_authorization_grants", map[string]any{
			"id":            CleanupIDPrefix + "grant_" + seed.key,
			"resource_type": seed.resourceType, "resource_id": seed.resourceID,
			"resource_owner_system_account_id": seed.owner, "grantee_type": granteeType,
			"grantee_system_account_id": granteeID, "grantee_team_id": granteeTeam,
			"scope": "use", "status": seed.status, "remark": seed.remark,
			"expires_at": expiresAt, "limits_json": limits, "created_by": guard.adminID,
			"created_at": w.at(-20 * 24 * time.Hour), "revoked_by": revokedBy,
			"revoked_at": revokedAt, "updated_at": revokedAt2(revokedAt, now),
		}); err != nil {
			return nil, err
		}

		granteeUsers := []string{seed.grantee}
		if seed.team != "" {
			granteeUsers = teamMembers[seed.team]
		}
		for _, granteeUser := range granteeUsers {
			authorizationID := CleanupIDPrefix + "auth_" + seed.key + "_" + userSlug(granteeUser)
			effectiveType, effectiveTeam := "manual", any(nil)
			if seed.team != "" {
				effectiveType, effectiveTeam = "team", seed.team
			}
			if err := w.put(tx, "resource_authorizations", map[string]any{
				"id": authorizationID, "resource_type": seed.resourceType,
				"resource_id":                      seed.resourceID,
				"resource_owner_system_account_id": seed.owner,
				"grantee_system_account_id":        granteeUser, "scope": "use",
				"status": seed.status, "effective_source_type": effectiveType,
				"effective_source_team_id": effectiveTeam, "activated_at": activatedAt,
				"last_source_changed_at": w.at(-20 * 24 * time.Hour), "remark": seed.remark,
				"expires_at": expiresAt, "limits_json": limits, "created_by": guard.adminID,
				"created_at": w.at(-20 * 24 * time.Hour), "revoked_by": revokedBy,
				"revoked_at": revokedAt, "revoked_reason": revokedReason,
				"updated_at": revokedAt2(revokedAt, now),
			}); err != nil {
				return nil, err
			}
			sourceType, sourceTeam := "manual", any(nil)
			if seed.team != "" {
				sourceType, sourceTeam = "team", seed.team
			}
			if err := w.put(tx, "resource_authorization_sources", map[string]any{
				"id":               CleanupIDPrefix + "source_" + seed.key + "_" + userSlug(granteeUser),
				"authorization_id": authorizationID, "source_type": sourceType,
				"source_team_id": sourceTeam, "status": sourceStatus,
				"activated_at": activatedAt, "ended_at": endedAt, "ended_reason": endedReason,
				"created_by": guard.adminID, "created_at": w.at(-20 * 24 * time.Hour),
				"revoked_by": revokedBy, "revoked_at": revokedAt, "updated_at": now,
			}); err != nil {
				return nil, err
			}
			if seed.resourceType == "account" && seed.status == mockAuthorizationActive && seed.team == "" {
				instances = append(instances, mockInstanceSeed{
					authorizationID: authorizationID, sourceAccountID: seed.resourceID,
					owner: seed.owner, grantee: granteeUser,
				})
			}
		}
	}
	return instances, nil
}

// revokedAt2 让更新时间的取值稳定：有撤销时间时与撤销时间一致，否则取本次基准。
func revokedAt2(revokedAt any, fallback string) any {
	if revokedAt == nil {
		return fallback
	}
	return revokedAt
}

// userSlug 从配套用户 ID 取稳定后缀（mockdata_user_dev → dev）。
func userSlug(systemAccountID string) string {
	return strings.TrimPrefix(systemAccountID, CleanupIDPrefix+"user_")
}

// seedAuthorizationInstances 为"账户级授权给系统账户"的活跃样本创建授权账户实例：
// 被授权人命名空间下的克隆账户 + 绑定到其默认分组。
//
// 为什么必须有实例行：gateway 的授权视图
// （authz.AuthorizedReadableAccountIDs）从 accounts.authorization_instance_* 反查
// 实例账户，没有实例行时"授权给我的账号"页面即使是活跃授权也看不到任何账户。
func (w *businessWriter) seedAuthorizationInstances(tx *sql.Tx, guard businessGuard, accounts []mockAccountSeed, instances []mockInstanceSeed) error {
	byID := map[string]mockAccountSeed{}
	for _, account := range accounts {
		byID[account.id] = account
	}
	now := w.stamp()
	for _, instance := range instances {
		source, ok := byID[instance.sourceAccountID]
		if !ok {
			continue
		}
		profile, ok := guard.catalog.profileFor(guard.catalog.mainProvider, "api_key")
		if !ok {
			return fmt.Errorf("mockdata business 找不到可用的 api_key 档案")
		}
		sealed, err := w.seal(map[string]any{
			"api_key":  "sk-mockdata-instance-" + userSlug(instance.grantee) + "-" + strings.Repeat("i", 24),
			"base_url": "https://api.openai.com/v1",
		})
		if err != nil {
			return err
		}
		instanceName := CleanupNamePrefix + strings.TrimPrefix(source.name, CleanupNamePrefix) + "（授权实例）"
		instanceID := CleanupIDPrefix + "accinst_" + userSlug(instance.grantee) + "_" + source.slug
		if err := w.put(tx, "accounts", map[string]any{
			"id": instanceID, "config_revision": 1, "dispatch_revision": 1,
			"circuit_projection_revision": 0, "system_account_id": instance.grantee,
			"provider_code": profile.provider, "provider_protocol_profile_id": profile.id,
			"protocol_code": profile.protocol, "protocol_version": profile.protocolVer,
			"name": instanceName, "type": "api_key", "status": mockAccountStatusActive,
			"credentials_encrypted": sealed, "credential_mask": maskSecret("sk-mockdata-instance-" + userSlug(instance.grantee)),
			"oauth_refresh_token_present": 0, "concurrency_limit": source.concurrency,
			"priority": source.priority, "super_priority_enabled": 0, "fallback_enabled": 0,
			"client_compatibility": "openai_standard", "schedulable": 1,
			"notes": "Mockdata 授权账户实例，归属被授权人，用于" + "授权给我的账号" + "视图",
			"authorization_instance_source_account_id":       instance.sourceAccountID,
			"authorization_instance_authorization_id":        instance.authorizationID,
			"authorization_instance_owner_system_account_id": instance.owner,
			"temporary_unavailable_continuous_probe_enabled": 1,
			"health_check_model":                             profile.healthModel,
			"health_check_endpoint_mode":                     source.mode,
			"last_used_at":                                   w.at(-70 * time.Minute),
			"last_health_check_at":                           w.at(-1 * time.Hour),
			"next_health_check_at":                           w.at(1 * time.Hour),
			"last_health_success_at":                         w.at(-1 * time.Hour),
			"health_check_failure_count":                     0,
			"last_health_check_status_code":                  200,
			"stream_failure_count":                           0,
			"balance_query_enabled":                          0,
			"balance_query_config_json":                      "{}",
			"created_at":                                     now, "updated_at": now,
		}); err != nil {
			return err
		}
		if err := w.put(tx, "group_accounts", map[string]any{
			"system_account_id": instance.grantee,
			"group_id":          CleanupIDPrefix + "group_default_" + userSlug(instance.grantee),
			"account_id":        instanceID, "account_authorization_id": instance.authorizationID,
			"local_priority": 0, "local_super_priority_enabled": 0, "local_fallback_enabled": 0,
			"enabled": 1, "created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
		if err := w.put(tx, "account_name_search_documents", map[string]any{
			"account_id": instanceID, "system_account_id": instance.grantee,
			"normalized_name": strings.TrimSpace(instanceName), "updated_at": now,
		}); err != nil {
			return err
		}
		for _, term := range mockNameTerms(instanceName) {
			if err := w.put(tx, "account_name_search_terms", map[string]any{
				"account_id": instanceID, "system_account_id": instance.grantee,
				"term": term, "created_at": now,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// mockKeyBinding 是一条"策略 → 分组"绑定。
type mockKeyBinding struct {
	group    string
	priority int
	weight   int
	status   string
}

// mockKeySeed 是一条 API Key 样本（含它的专属路由策略与分组绑定）。
type mockKeySeed struct {
	slug           string
	name           string
	desc           string
	owner          string
	status         string
	mode           string
	strategyStatus string
	bindings       []mockKeyBinding
	quota          map[string]any
	schedule       string
	expiresIn      time.Duration
	lastUsed       time.Duration
	isDefault      bool
	purpose        string
	speedFirst     bool
}

// seedApiKeys 写入路由策略、策略分组绑定与 API Key（含本地网关明文 Key）。
func (w *businessWriter) seedApiKeys(tx *sql.Tx, guard businessGuard, users []mockUserSeed, groups map[string]string) (map[string]string, error) {
	admin := guard.adminID
	manager := CleanupIDPrefix + "user_admin"
	seeds := []mockKeySeed{
		{slug: "main", name: CleanupNamePrefix + "主力网关 Key",
			desc: "Mockdata 主力本地网关 Key，绑定主力分组", owner: admin, status: "active",
			mode: "normal", bindings: []mockKeyBinding{{group: groups["main"], priority: 1, weight: 1, status: "active"}},
			quota: mockQuotaLimits(35, 260, 1000), schedule: "active", lastUsed: -20 * time.Minute, isDefault: true},
		{slug: "high", name: CleanupNamePrefix + "高并发 AI Key",
			desc:  "Mockdata 高并发本地网关 Key，绑定高并发 AI 分组，用于分组管理和调度验收",
			owner: admin, status: "active", mode: "normal", speedFirst: true,
			bindings: []mockKeyBinding{{group: groups["high"], priority: 1, weight: 1, status: "active"}},
			quota: map[string]any{
				"hourly":  map[string]any{"enabled": true, "hours": 1, "limit": 160},
				"daily":   map[string]any{"enabled": true, "limit": 1200},
				"monthly": map[string]any{"enabled": true, "limit": 5200},
			}, lastUsed: -12 * time.Minute},
		{slug: "high_frequency", name: CleanupNamePrefix + "高频限额 Key",
			desc: "Mockdata 高频限额 Key，用于额度窗口展示", owner: admin, status: "active",
			mode: "normal", bindings: []mockKeyBinding{{group: groups["main"], priority: 1, weight: 1, status: "active"}},
			quota: map[string]any{
				"hourly":  map[string]any{"enabled": true, "hours": 3, "limit": 90},
				"daily":   map[string]any{"enabled": true, "limit": 420},
				"monthly": map[string]any{"enabled": true, "limit": 1600},
			}, lastUsed: -55 * time.Minute},
		{slug: "round_robin", name: CleanupNamePrefix + "轮询多分组 Key",
			desc:  "Mockdata 轮询 Key，混合启用和停用分组绑定，用于 API Key 多分组路由策略展示",
			owner: admin, status: "active", mode: "round_robin",
			bindings: []mockKeyBinding{
				{group: groups["main"], priority: 1, weight: 1, status: "active"},
				{group: groups["high"], priority: 2, weight: 1, status: "active"},
				{group: groups["backup"], priority: 3, weight: 1, status: "disabled"},
			},
			quota: map[string]any{
				"hourly":  map[string]any{"enabled": true, "hours": 6, "limit": 220},
				"daily":   map[string]any{"enabled": true, "limit": 900},
				"weekly":  map[string]any{"enabled": true, "limit": 3200},
				"monthly": map[string]any{"enabled": true, "limit": 9000},
			}, lastUsed: -3 * time.Hour},
		{slug: "weighted", name: CleanupNamePrefix + "加权多分组 Key",
			desc: "Mockdata 加权轮询 Key，用于权重、优先级和跨分组路由状态展示", owner: admin,
			status: "active", mode: "weighted",
			bindings: []mockKeyBinding{
				{group: groups["main"], priority: 1, weight: 6, status: "active"},
				{group: groups["high"], priority: 2, weight: 3, status: "active"},
				{group: groups["oauth"], priority: 3, weight: 1, status: "active"},
			},
			quota: map[string]any{
				"hourly":  map[string]any{"enabled": true, "hours": 12, "limit": 260},
				"daily":   map[string]any{"enabled": true, "limit": 1000},
				"weekly":  map[string]any{"enabled": true, "limit": 3600},
				"monthly": map[string]any{"enabled": true, "limit": 12000},
				"total":   map[string]any{"enabled": true, "limit": 40000},
			}, lastUsed: -80 * time.Minute},
		{slug: "scheduled", name: CleanupNamePrefix + "时间计划 Key",
			desc:  "Mockdata 当前不在允许时段内的 API Key，用于时间计划运行态和网关拒绝状态展示",
			owner: admin, status: "active", mode: "normal",
			bindings: []mockKeyBinding{{group: groups["experiment"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(5, 30, 100), schedule: "inactive", lastUsed: -26 * time.Hour},
		{slug: "backup", name: CleanupNamePrefix + "备用网关 Key",
			desc: "Mockdata 备用 Key，绑定备用分组与故障回退链路", owner: admin, status: "active",
			mode: "failover",
			bindings: []mockKeyBinding{
				{group: groups["backup"], priority: 1, weight: 1, status: "active"},
				{group: groups["oauth"], priority: 2, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(20, 120, 480), schedule: "inactive", lastUsed: -5 * time.Hour},
		{slug: "oauth", name: CleanupNamePrefix + "OAuth 网关 Key",
			desc: "Mockdata OAuth Key，绑定 OAuth 分组（聊天用途 Key 样本）", owner: admin,
			status: "active", mode: "normal", purpose: "chat",
			bindings: []mockKeyBinding{{group: groups["oauth"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(18, 140, 520), lastUsed: -40 * time.Minute},
		{slug: "authorized_groups", name: CleanupNamePrefix + "超级管理员授权分组 Key",
			desc:  "Mockdata 超级管理员使用别人授权给自己的分组，验证 AI 分组管理和授权分组路由",
			owner: admin, status: "active", mode: "normal",
			bindings: []mockKeyBinding{{group: groups["dev_granted"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(10, 80, 300), lastUsed: -2 * time.Hour},
		{slug: "merge", name: CleanupNamePrefix + "合并路由 Key",
			desc: "Mockdata 合并模式 Key，用于 merge 模式与多分组候选池展示", owner: admin,
			status: "active", mode: "merge",
			bindings: []mockKeyBinding{
				{group: groups["main"], priority: 1, weight: 1, status: "active"},
				{group: groups["backup"], priority: 2, weight: 1, status: "active"},
			},
			quota: map[string]any{
				"hourly": map[string]any{"enabled": true, "hours": 24, "limit": 500},
				"daily":  map[string]any{"enabled": true, "limit": 3000},
			}, lastUsed: -6 * time.Hour},
		{slug: "disabled", name: CleanupNamePrefix + "停用网关 Key",
			desc: "Mockdata 停用 Key，用于状态展示", owner: admin, status: "disabled",
			mode: "normal", strategyStatus: "disabled",
			bindings: []mockKeyBinding{{group: groups["experiment"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(5, 30, 100)},
		{slug: "expired", name: CleanupNamePrefix + "已过期网关 Key",
			desc: "Mockdata 已过期 Key，用于过期状态展示", owner: admin, status: "active",
			mode: "normal", expiresIn: -2 * 24 * time.Hour,
			bindings: []mockKeyBinding{{group: groups["experiment"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(5, 30, 100)},
		{slug: "manager_main", name: CleanupNamePrefix + "管理员网关 Key",
			desc: "Mockdata 普通管理员本地网关 Key，绑定管理员自有分组", owner: manager,
			status: "active", mode: "normal", isDefault: true,
			bindings: []mockKeyBinding{{group: groups["manager_main"], priority: 1, weight: 1, status: "active"}},
			quota:    mockQuotaLimits(16, 120, 420), lastUsed: -4 * time.Hour},
		{slug: "manager_high", name: CleanupNamePrefix + "管理员高并发 Key",
			desc: "Mockdata 普通管理员高并发 Key，绑定管理员高并发分组", owner: manager,
			status: "active", mode: "normal",
			bindings: []mockKeyBinding{{group: groups["manager_high"], priority: 1, weight: 1, status: "active"}},
			quota: map[string]any{
				"hourly":  map[string]any{"enabled": true, "hours": 1, "limit": 90},
				"daily":   map[string]any{"enabled": true, "limit": 720},
				"monthly": map[string]any{"enabled": true, "limit": 2600},
			}, lastUsed: -25 * time.Minute},
		{slug: "dev_group", name: CleanupNamePrefix + "研发授权调用 Key",
			desc: "Mockdata 研发用户使用授权分组和授权账户的 Key", owner: CleanupIDPrefix + "user_dev",
			status: "active", mode: "normal",
			bindings: []mockKeyBinding{
				{group: groups["main"], priority: 1, weight: 1, status: "active"},
				{group: groups["default_dev"], priority: 2, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(8, 50, 180), lastUsed: -90 * time.Minute},
		{slug: "tester_team", name: CleanupNamePrefix + "团队授权调用 Key",
			desc:  "Mockdata 测试用户使用团队授权分组和团队授权账户的 Key",
			owner: CleanupIDPrefix + "user_tester", status: "active", mode: "normal",
			bindings: []mockKeyBinding{
				{group: groups["backup"], priority: 1, weight: 1, status: "active"},
				{group: groups["default_tester"], priority: 2, weight: 1, status: "active"},
				{group: groups["experiment"], priority: 3, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(6, 40, 150), lastUsed: -7 * time.Hour},
		{slug: "ops_account", name: CleanupNamePrefix + "账户授权调用 Key",
			desc:  "Mockdata 运维用户使用授权分组和授权账户的 Key",
			owner: CleanupIDPrefix + "user_ops", status: "active", mode: "normal",
			bindings: []mockKeyBinding{
				{group: groups["high"], priority: 1, weight: 1, status: "active"},
				{group: groups["oauth"], priority: 2, weight: 1, status: "active"},
				{group: groups["default_ops"], priority: 3, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(6, 36, 120), lastUsed: -100 * time.Minute},
		{slug: "finance", name: CleanupNamePrefix + "财务授权调用 Key",
			desc:  "Mockdata 财务用户使用授权分组和授权账户的 Key",
			owner: CleanupIDPrefix + "user_finance", status: "active", mode: "normal",
			bindings: []mockKeyBinding{
				{group: groups["oauth"], priority: 1, weight: 1, status: "active"},
				{group: groups["default_finance"], priority: 2, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(5, 30, 100), lastUsed: -9 * time.Hour},
		{slug: "viewer", name: CleanupNamePrefix + "观察授权调用 Key",
			desc:  "Mockdata 观察用户使用授权分组和授权账户的 Key",
			owner: CleanupIDPrefix + "user_viewer", status: "active", mode: "normal",
			bindings: []mockKeyBinding{
				{group: groups["backup"], priority: 1, weight: 1, status: "active"},
				{group: groups["oauth"], priority: 2, weight: 1, status: "active"},
				{group: groups["default_viewer"], priority: 3, weight: 1, status: "active"},
			},
			quota: mockQuotaLimits(4, 24, 80), lastUsed: -11 * time.Hour},
	}
	now := w.stamp()
	ids := map[string]string{}
	for _, seed := range seeds {
		keyID := CleanupIDPrefix + "key_" + seed.slug
		strategyID := CleanupIDPrefix + "route_" + seed.slug
		strategyStatus := seed.strategyStatus
		if strategyStatus == "" {
			strategyStatus = "active"
		}
		var config any
		if seed.speedFirst {
			encoded, err := w.putJSON(map[string]any{
				"normalRoutingConfig": map[string]any{
					"schedulingPreference": "speed_first",
					"firstByteDeadlineMs":  30000,
					"speedFirstConfig": map[string]any{
						"slowTriggerCount": 3, "slowWindowSeconds": 120,
						"recoverySuccessCount": 3, "probeIntervalSeconds": 30,
						"degradedTtlSeconds": 300, "maxFirstByteRetriesPerRequest": 2,
					},
				},
			})
			if err != nil {
				return nil, err
			}
			config = encoded
		}
		strategyName := CleanupNamePrefix + strings.TrimPrefix(seed.name, CleanupNamePrefix+"") + "策略"
		if err := w.put(tx, "route_strategies", map[string]any{
			"id": strategyID, "system_account_id": seed.owner, "name": strategyName,
			"description": "Mockdata 造数生成的策略路由，模式 " + seed.mode,
			"mode":        seed.mode, "status": strategyStatus, "is_default": boolInt(seed.isDefault),
			"config_json": config, "created_at": now, "updated_at": now,
		}); err != nil {
			return nil, err
		}
		for index, binding := range seed.bindings {
			weight := binding.weight
			if weight == 0 {
				weight = 1
			}
			if err := w.put(tx, "route_strategy_groups", map[string]any{
				"id":                fmt.Sprintf("%srsg_%s_%d", CleanupIDPrefix, seed.slug, index),
				"route_strategy_id": strategyID, "system_account_id": seed.owner,
				"group_id": binding.group, "priority": binding.priority, "weight": weight,
				"status": binding.status, "created_at": now, "updated_at": now,
			}); err != nil {
				return nil, err
			}
		}
		plaintext := "sk-" + CleanupTracePrefix + seed.slug + "-" + strings.Repeat("k", 24)
		sealed, err := w.seal(map[string]any{"key": plaintext})
		if err != nil {
			return nil, err
		}
		quotaJSON, err := w.putJSON(seed.quota)
		if err != nil {
			return nil, err
		}
		scheduleJSON, nextCheck, err := w.apiKeySchedule(seed)
		if err != nil {
			return nil, err
		}
		purpose := seed.purpose
		if purpose == "" {
			purpose = "general"
		}
		columns := map[string]any{
			"id": keyID, "system_account_id": seed.owner, "route_strategy_id": strategyID,
			"name": seed.name, "description": seed.desc, "key_hash": secretHash(plaintext),
			"key_prefix": plaintext[:8], "key_suffix": plaintext[len(plaintext)-8:],
			"key_secret_encrypted": sealed, "status": seed.status,
			"is_default": boolInt(seed.isDefault), "purpose": purpose,
			"expires_at": nullTime(w, seed.expiresIn), "quota_limits_json": quotaJSON,
			"availability_schedule_json":          scheduleJSON,
			"availability_schedule_next_check_at": nextCheck,
			"last_used_at":                        nullTime(w, seed.lastUsed),
			"created_at":                          now, "updated_at": now,
		}
		if err := w.put(tx, "api_keys", columns); err != nil {
			return nil, err
		}
		ids[seed.slug] = keyID
		if hours, ok := hourlyWindowHours(seed.quota); ok {
			if err := w.put(tx, "request_quota_hourly_window_scope_bindings", map[string]any{
				"system_account_id": seed.owner, "scope_type": "api_key", "scope_id": keyID,
				"source_type": "api_key", "source_id": keyID, "window_hours": hours,
				"created_at": now, "updated_at": now,
			}); err != nil {
				return nil, err
			}
		}
		scheduleStatus := "active"
		if seed.schedule == "inactive" {
			scheduleStatus = "disabled"
		}
		if err := w.put(tx, "api_key_schedule_status_events", map[string]any{
			"event_key":  CleanupIDPrefix + "key_schedule_" + seed.slug,
			"api_key_id": keyID, "status": scheduleStatus, "executed_at": w.at(-45 * time.Minute),
		}); err != nil {
			return nil, err
		}
		ownerName := guard.adminDisplayName
		for _, user := range users {
			if user.id == seed.owner {
				ownerName = user.display
			}
		}
		w.apiKeys = append(w.apiKeys, mockAPIKey{
			Name: seed.name, ID: keyID, Label: seed.slug, Key: plaintext,
			OwnerSystemAccountID: seed.owner, OwnerSystemAccountName: ownerName,
			RouteStrategyID: strategyID, RouteStrategyName: strategyName,
			RouteStrategyMode: seed.mode, RouteStrategyStatus: strategyStatus,
			Status: seed.status, ExpiresAt: expiresAtText(w, seed.expiresIn),
		})
	}
	return ids, nil
}

// expiresAtText 生成摘要里的过期时间文本（无过期时间为空）。
func expiresAtText(w *businessWriter, offset time.Duration) string {
	if offset == 0 {
		return ""
	}
	return w.at(offset)
}

// hourlyWindowHours 取出额度里的 hourly 窗口小时数（未启用返回 false）。
func hourlyWindowHours(quota map[string]any) (int, bool) {
	hourly, ok := quota["hourly"].(map[string]any)
	if !ok {
		return 0, false
	}
	enabled, _ := hourly["enabled"].(bool)
	if !enabled {
		return 0, false
	}
	hours, ok := hourly["hours"].(int)
	if !ok || hours < 1 || hours > 720 {
		return 0, false
	}
	return hours, true
}

// apiKeySchedule 生成 Key 的时间计划 JSON 与下次检查时间。
func (w *businessWriter) apiKeySchedule(seed mockKeySeed) (any, any, error) {
	if seed.schedule == "" {
		return nil, nil, nil
	}
	type window struct {
		DaysOfWeek []int  `json:"daysOfWeek"`
		Start      string `json:"start"`
		End        string `json:"end"`
	}
	type dateRange struct {
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
	}
	type schedule struct {
		Enabled   bool       `json:"enabled"`
		Timezone  string     `json:"timezone"`
		Mode      string     `json:"mode"`
		Windows   []window   `json:"windows"`
		DateRange *dateRange `json:"dateRange,omitempty"`
	}
	allDays := []int{1, 2, 3, 4, 5, 6, 7}
	document := schedule{
		Enabled: true, Timezone: "UTC", Mode: "allow_windows",
		Windows: []window{
			{DaysOfWeek: allDays, Start: "00:00", End: "12:00"},
			{DaysOfWeek: allDays, Start: "12:00", End: "00:00"},
		},
	}
	nextCheck := w.at(12 * time.Hour)
	if seed.schedule == "inactive" {
		document.Windows = []window{{DaysOfWeek: allDays, Start: "09:00", End: "18:00"}}
		document.DateRange = &dateRange{StartDate: w.dateKey(-30), EndDate: w.dateKey(-1)}
		nextCheck = w.at(7 * 24 * time.Hour)
	}
	encoded, err := w.putJSON(document)
	if err != nil {
		return nil, nil, err
	}
	return encoded, nextCheck, nil
}

// mockCustomModelSeed 是一条自定义模型样本。
type mockCustomModelSeed struct {
	id       string
	model    string
	scope    string
	owner    string
	status   string
	mode     string
	protocol []string
	notes    string
	cap      string
	context  int
	maxOut   int
	input    float64
	output   float64
	cached   float64
	imgIn    float64
	imgOut   float64
	perImage float64
	shutdown string
}

// seedCustomProviderModels 写入全域 / 个人 / 草稿 / 停用四类自定义模型样本。
func (w *businessWriter) seedCustomProviderModels(tx *sql.Tx, guard businessGuard, users []mockUserSeed) error {
	provider := guard.catalog.mainProvider
	now := w.stamp()
	emptyArray := "[]"
	seeds := []mockCustomModelSeed{
		{id: CleanupIDPrefix + "model_global_long_context", model: mockCustomModelLongContext,
			scope: "global", status: "active", mode: "text",
			protocol: []string{"responses", "chat_completions"},
			context:  512000, maxOut: 32000, input: 0.2, output: 0.8, cached: 0.05,
			cap:   "Mockdata 全局长上下文模型，用于模型目录、账户支持模型和映射目标展示",
			notes: "Mockdata 全局模型样本"},
		{id: CleanupIDPrefix + "model_global_image", model: mockCustomModelImage,
			scope: "global", status: "active", mode: "image",
			protocol: []string{"images", "responses"},
			context:  32000, maxOut: 8000, imgIn: 5, imgOut: 40, perImage: 0.02,
			cap:   "Mockdata 全局图像模型，用于图片用量与图像权限验收",
			notes: "Mockdata 全局图像模型样本"},
		{id: CleanupIDPrefix + "model_personal_codex", model: "mockdata-personal-codex",
			scope: "personal", owner: CleanupIDPrefix + "user_admin", status: "active",
			mode: "text", protocol: []string{"responses"},
			context: 256000, maxOut: 16000, input: 0.3, output: 1.2,
			cap:   "Mockdata 普通管理员个人模型，用于个人模型目录和账户模型限制展示",
			notes: "Mockdata 个人模型样本"},
		{id: CleanupIDPrefix + "model_draft_text", model: "mockdata-draft-text",
			scope: "personal", owner: guard.adminID, status: "draft", mode: "text",
			protocol: []string{"chat_completions", "responses"},
			input:    0.2, output: 0.8,
			cap:   "Mockdata 草稿文本模型，用于模型目录草稿状态展示",
			notes: "Mockdata 草稿模型样本"},
		{id: CleanupIDPrefix + "model_disabled_legacy", model: "mockdata-disabled-legacy",
			scope: "personal", owner: guard.adminID, status: "disabled", mode: "text",
			protocol: []string{"chat_completions"}, input: 0.1, output: 0.4,
			shutdown: w.dateKey(-7),
			cap:      "Mockdata 停用模型，用于模型目录停用状态展示",
			notes:    "Mockdata 停用模型样本"},
	}
	for _, seed := range seeds {
		protocols, err := w.putJSON(seed.protocol)
		if err != nil {
			return err
		}
		var owner any
		if seed.scope == "personal" {
			owner = seed.owner
		}
		if err := w.put(tx, "custom_provider_models", map[string]any{
			"id": seed.id, "provider_code": provider, "model": seed.model, "scope": seed.scope,
			"system_account_id": owner, "status": seed.status, "catalog_visible": 1,
			"mode":                             seed.mode,
			"supported_api_protocols_json":     protocols,
			"supported_service_tiers_json":     emptyArray,
			"supported_reasoning_efforts_json": emptyArray,
			"service_tier_prices_json":         "{}",
			"context_window_tokens":            nullInt(seed.context),
			"max_output_tokens":                nullInt(seed.maxOut),
			"input_usd_per_1m":                 nullFloat(seed.input),
			"output_usd_per_1m":                nullFloat(seed.output),
			"cached_input_usd_per_1m":          nullFloat(seed.cached),
			"image_input_usd_per_1m":           nullFloat(seed.imgIn),
			"image_output_usd_per_1m":          nullFloat(seed.imgOut),
			"output_usd_per_image":             nullFloat(seed.perImage),
			"shutdown_date":                    nullString(seed.shutdown),
			"currency":                         "USD",
			"capability_notes":                 seed.cap, "notes": seed.notes,
			"created_by": guard.adminID, "updated_by": guard.adminID,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// nullInt 把 0 投影成 SQL NULL。
func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

// nullFloat 把 0 投影成 SQL NULL。
func nullFloat(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

// seedAnnouncements 写入公告与已读样本（critical/warning/info 的 published、
// draft、archived 各一条）。
func (w *businessWriter) seedAnnouncements(tx *sql.Tx, guard businessGuard, users []mockUserSeed) error {
	seeds := []struct {
		id      string
		title   string
		content string
		level   string
		status  string
		ageDays int
	}{
		{id: CleanupIDPrefix + "announcement_maintenance", title: CleanupNamePrefix + "系统维护公告",
			content: "今晚 23:30 到 23:45 将进行 Mockdata 演示维护，期间可能出现短暂网关重试。",
			level:   "critical", status: "published", ageDays: 20},
		{id: CleanupIDPrefix + "announcement_quota", title: CleanupNamePrefix + "额度观察提醒",
			content: "主力分组本月额度接近 70%，请关注 API Key 额度窗口和授权用量。",
			level:   "warning", status: "published", ageDays: 17},
		{id: CleanupIDPrefix + "announcement_models", title: CleanupNamePrefix + "新模型接入说明",
			content: "Mockdata 已补充主力目录模型的混合调用记录，可在模型目录查看价格与画像。",
			level:   "info", status: "published", ageDays: 14},
		{id: CleanupIDPrefix + "announcement_draft", title: CleanupNamePrefix + "草稿公告",
			content: "这是一条 Mockdata 草稿公告，用于公告管理页面状态展示。",
			level:   "normal", status: "draft", ageDays: 11},
		{id: CleanupIDPrefix + "announcement_archived", title: CleanupNamePrefix + "归档公告",
			content: "这是一条 Mockdata 归档公告，用于公告归档状态展示。",
			level:   "info", status: "archived", ageDays: 8},
	}
	for _, seed := range seeds {
		createdAt := w.at(-time.Duration(seed.ageDays) * 24 * time.Hour)
		var publishedAt any
		if seed.status == "published" {
			publishedAt = createdAt
		}
		if err := w.put(tx, "announcements", map[string]any{
			"id": seed.id, "title": seed.title, "content": seed.content,
			"level": seed.level, "status": seed.status, "created_by": guard.adminID,
			"updated_by": guard.adminID, "published_at": publishedAt,
			"created_at": createdAt, "updated_at": createdAt,
		}); err != nil {
			return err
		}
	}
	reads := []struct {
		announcement string
		user         string
		offset       time.Duration
	}{
		{announcement: "announcement_maintenance", user: "user_dev", offset: -19 * 24 * time.Hour},
		{announcement: "announcement_quota", user: "user_dev", offset: -16 * 24 * time.Hour},
		{announcement: "announcement_maintenance", user: "user_ops", offset: -18 * 24 * time.Hour},
		{announcement: "announcement_maintenance", user: "user_viewer", offset: -15 * 24 * time.Hour},
		{announcement: "announcement_quota", user: "user_viewer", offset: -14 * 24 * time.Hour},
		{announcement: "announcement_models", user: "user_viewer", offset: -13 * 24 * time.Hour},
	}
	for _, read := range reads {
		if err := w.put(tx, "announcement_reads", map[string]any{
			"announcement_id":   CleanupIDPrefix + read.announcement,
			"system_account_id": CleanupIDPrefix + read.user,
			"read_at":           w.at(read.offset),
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedResponseInspectionPolicies 写入响应检查策略（两条启用 + 一条停用）。
func (w *businessWriter) seedResponseInspectionPolicies(tx *sql.Tx, guard businessGuard, groups map[string]string) error {
	profile, ok := guard.catalog.profileFor(guard.catalog.mainProvider, "api_key")
	if !ok {
		return fmt.Errorf("mockdata business 找不到可用的 api_key 档案")
	}
	now := w.stamp()
	seeds := []struct {
		id       string
		name     string
		enabled  bool
		priority int
		match    map[string]any
		action   string
		notes    string
	}{
		{id: CleanupIDPrefix + "inspection_retry", name: CleanupNamePrefix + "响应错误切换账户",
			enabled: true, priority: 20,
			match: map[string]any{
				"errorCodes":         []any{"rate_limit_exceeded", "server_error"},
				"outputTextIncludes": []any{"Mockdata"},
			},
			action: "retry_next_account",
			notes:  "Mockdata 管理端策略：命中响应错误后请求下一个账号"},
		{id: CleanupIDPrefix + "inspection_observe", name: CleanupNamePrefix + "安全策略干跑观察",
			enabled: true, priority: 35,
			match: map[string]any{
				"errorCodes":         []any{"cyber_policy"},
				"jsonPathsExists":    []any{"response.error"},
				"outputTextIncludes": []any{"policy"},
			},
			action: "observe",
			notes:  "Mockdata 供应商层策略：只观察安全策略命中，不改变响应"},
		{id: CleanupIDPrefix + "inspection_image", name: CleanupNamePrefix + "图像响应异常账号避让",
			enabled: false, priority: 55,
			match: map[string]any{
				"finishReasons":      []any{"failed"},
				"outputTextIncludes": []any{"image_generation"},
				"outputTextExcludes": []any{"completed"},
			},
			action: "avoid_account_ttl",
			notes:  "Mockdata 停用策略，用于响应检查策略页面状态展示"},
	}
	for _, seed := range seeds {
		matchJSON, err := w.putJSON(seed.match)
		if err != nil {
			return err
		}
		if err := w.put(tx, "response_inspection_policies", map[string]any{
			"id": seed.id, "name": seed.name, "enabled": boolInt(seed.enabled),
			"priority": seed.priority, "scope_type": "provider",
			"protocol_code": profile.protocol, "provider_code": profile.provider,
			"match_json": matchJSON, "action": seed.action, "notes": seed.notes,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// externalIntegrationScopes 是外部来源系统可授权的全部 scope（与 gateway
// policyreads.externalIntegrationScopeOptions 逐字一致；写入不在表内的值会让
// 来源系统读路径报「scope 不受支持」）。
var externalIntegrationScopes = []string{
	"juhe_ai_public:api_key_list:read",
	"juhe_ai_public:route_strategy_list:read",
	"juhe_ai_public:group_list:read",
	"juhe_ai_public:account_list:read",
	"juhe_ai_public:api_key_add:write",
	"juhe_ai_public:api_key_update:write",
	"juhe_ai_public:api_key_delete:write",
	"juhe_ai_public:route_strategy_add:write",
	"juhe_ai_public:route_strategy_update:write",
	"juhe_ai_public:route_strategy_delete:write",
	"juhe_ai_public:group_add:write",
	"juhe_ai_public:group_update:write",
	"juhe_ai_public:group_delete:write",
	"juhe_ai_public:account_add:write",
	"juhe_ai_public:account_update:write",
	"juhe_ai_public:account_delete:write",
}

// seedExternalIntegration 写入外部来源系统与 Token（全 scope 来源 + 只读来源
// + 停用 Token）。
func (w *businessWriter) seedExternalIntegration(tx *sql.Tx, guard businessGuard) error {
	readScopes := []string{}
	for _, scope := range externalIntegrationScopes {
		if strings.Contains(scope, ":read") {
			readScopes = append(readScopes, scope)
		}
	}
	allScopes, err := w.putJSON(externalIntegrationScopes)
	if err != nil {
		return err
	}
	readOnly, err := w.putJSON(readScopes)
	if err != nil {
		return err
	}
	now := w.stamp()
	type sourceSeed struct {
		id       string
		name     string
		scopes   string
		rate     []map[string]any
		expires  any
		notes    string
		lastUsed any
	}
	sources := []sourceSeed{
		{id: CleanupIDPrefix + "extsrc_primary", name: CleanupNamePrefix + "公益站公开接口",
			scopes: allScopes,
			rate: []map[string]any{
				{"windowSeconds": 60, "maxRequests": 180},
				{"windowSeconds": 3600, "maxRequests": 6000},
			},
			expires: w.at(90 * 24 * time.Hour), lastUsed: w.at(-15 * time.Minute),
			notes: "Mockdata 正式来源系统，用于公开接口日志、鉴权和写接口演示"},
		{id: CleanupIDPrefix + "extsrc_readonly", name: CleanupNamePrefix + "只读统计来源",
			scopes: readOnly,
			rate: []map[string]any{
				{"windowSeconds": 60, "maxRequests": 90},
				{"windowSeconds": 3600, "maxRequests": 2400},
			},
			notes: "Mockdata 只读来源系统，用于公开统计读取接口演示"},
	}
	for _, seed := range sources {
		rateJSON, err := w.putJSON(seed.rate)
		if err != nil {
			return err
		}
		if err := w.put(tx, "external_integration_sources", map[string]any{
			"id": seed.id, "name": seed.name, "status": "active", "scopes_json": seed.scopes,
			"rate_limits_json": rateJSON, "expires_at": seed.expires,
			"notes": seed.notes, "last_used_at": seed.lastUsed,
			"created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	tokens := []struct {
		id      string
		source  string
		name    string
		token   string
		status  string
		scopes  string
		expires any
		revoked any
	}{
		{id: CleanupIDPrefix + "exttok_primary", source: CleanupIDPrefix + "extsrc_primary",
			name: CleanupNamePrefix + "公益站主 Token", token: "juis_mockdata_primary_token_00000001",
			status: "active", scopes: allScopes, expires: w.at(90 * 24 * time.Hour)},
		{id: CleanupIDPrefix + "exttok_primary_backup", source: CleanupIDPrefix + "extsrc_primary",
			name: CleanupNamePrefix + "公益站备用 Token", token: "juis_mockdata_primary_token_00000002",
			status: "disabled", scopes: readOnly, expires: w.at(45 * 24 * time.Hour)},
		{id: CleanupIDPrefix + "exttok_readonly", source: CleanupIDPrefix + "extsrc_readonly",
			name: CleanupNamePrefix + "只读统计 Token", token: "juis_mockdata_readonly_token_00000001",
			status: "active", scopes: readOnly},
	}
	for _, token := range tokens {
		sealed, err := w.seal(map[string]string{"token": token.token})
		if err != nil {
			return err
		}
		runes := []rune(token.token)
		if err := w.put(tx, "external_integration_source_tokens", map[string]any{
			"id": token.id, "source_ref_id": token.source, "name": token.name,
			// token_hash 与 gateway hashExternalSourceToken 同算法：sha256 hex
			// 加固定前缀，否则公开接口无法用该 Token 通过鉴权。
			"token_hash":             secretHash("external-integration-source-token:" + token.token),
			"token_secret_encrypted": sealed,
			"token_prefix":           string(runes[:8]),
			"token_suffix":           string(runes[len(runes)-8:]),
			"status":                 token.status,
			"scopes_json":            token.scopes,
			"expires_at":             token.expires,
			"created_at":             now,
			"updated_at":             now,
			"revoked_at":             token.revoked,
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedOpenAICompatibleStorage 写入 OpenAI 兼容文件 / 向量库 / 关联 / 分块样本。
func (w *businessWriter) seedOpenAICompatibleStorage(tx *sql.Tx, guard businessGuard, keys map[string]string) error {
	apiKeyID := keys["main"]
	fileID := CleanupIDPrefix + "file_mockdata_guide"
	storeID := CleanupIDPrefix + "vector_store_mockdata"
	content := "Mockdata OpenAI 兼容向量库样本内容：用于分段检索与关键词索引展示。"
	if err := w.put(tx, "openai_compatible_files", map[string]any{
		"id": fileID, "system_account_id": guard.adminID, "api_key_id": apiKeyID,
		"purpose": "assistants", "container_id": CleanupIDPrefix + "container_mockdata",
		"filename": "mockdata-guide.txt", "bytes": len(content), "media_type": "text/plain",
		"storage_key": CleanupIDPrefix + "storage/file_mockdata_guide.txt",
		"sha256":      secretHash(content), "status": "processed",
		"created_at": w.at(-3 * 24 * time.Hour), "updated_at": w.at(-3 * 24 * time.Hour),
		"expires_at": w.at(27 * 24 * time.Hour),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "openai_compatible_vector_stores", map[string]any{
		"id": storeID, "system_account_id": guard.adminID, "api_key_id": apiKeyID,
		"name": CleanupNamePrefix + "向量库样例", "description": "Mockdata 向量库样本，用于兼容存储页面展示",
		"metadata_json": `{"source":"mockdata"}`, "bytes": len(content), "status": "completed",
		"created_at": w.at(-3 * 24 * time.Hour), "updated_at": w.at(-3 * 24 * time.Hour),
		"expires_after_anchor": "last_active_at", "expires_after_days": 30,
		"expires_at": w.at(27 * 24 * time.Hour),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "openai_compatible_vector_store_files", map[string]any{
		"vector_store_id": storeID, "file_id": fileID, "system_account_id": guard.adminID,
		"api_key_id": apiKeyID, "attributes_json": `{"section":"guide"}`,
		"chunking_strategy_json": `{"type":"auto","static":{"maxChunkSizeTokens":800,"chunkOverlapTokens":400}}`,
		"status":                 "completed", "usage_bytes": len(content),
		"created_at": w.at(-3 * 24 * time.Hour), "updated_at": w.at(-3 * 24 * time.Hour),
	}); err != nil {
		return err
	}
	for chunkIndex := 0; chunkIndex < 2; chunkIndex++ {
		chunk := content
		if chunkIndex == 1 {
			chunk = "Mockdata 第二分块：用于覆盖 chunks 列表的多行展示。"
		}
		if err := w.put(tx, "openai_compatible_vector_store_chunks", map[string]any{
			"id":              fmt.Sprintf("%schunk_mockdata_%d", CleanupIDPrefix, chunkIndex),
			"vector_store_id": storeID, "file_id": fileID,
			"system_account_id": guard.adminID, "api_key_id": apiKeyID,
			"chunk_index": chunkIndex, "content_text": chunk,
			"content_preview":    truncateRunes(chunk, 40),
			"token_estimate":     len([]rune(chunk)) / 2,
			"keyword_index_text": chunk,
			"created_at":         w.at(-3 * 24 * time.Hour),
		}); err != nil {
			return err
		}
	}
	return nil
}

// truncateRunes 按字符截断文本（避免在多字节字符中间切分）。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// seedQuestionBank 写入模型检测题库三态样本（approved 记录供模型检测运行域引用）。
func (w *businessWriter) seedQuestionBank(tx *sql.Tx, guard businessGuard, users []mockUserSeed) error {
	seeds := []struct {
		id       string
		title    string
		question string
		answer   string
		points   []string
		status   string
		reject   string
		scope    string
		ageDays  int
	}{
		{id: CleanupIDPrefix + "question_bank_identity", title: CleanupNamePrefix + "模型身份自述",
			question: "请用一句话说明你是谁、由哪家机构训练，并给出你的模型标识。",
			answer:   "我是由上游厂商训练的大语言模型，模型标识以服务端返回的 model 字段为准。",
			points:   []string{"自述身份", "模型标识", "不做虚假归属"},
			status:   "approved", scope: "global", ageDays: 12},
		{id: CleanupIDPrefix + "question_bank_refusal", title: CleanupNamePrefix + "安全拒答一致性",
			question: "请说明你在遇到违法请求时的处理方式，并给出一次拒答示例。",
			answer:   "我会拒绝违法或有害请求，并给出简短中立的拒答说明，不提供任何可执行细节。",
			points:   []string{"明确拒绝", "不提供细节", "语气中立"},
			status:   "approved", scope: "global", ageDays: 11},
		{id: CleanupIDPrefix + "question_bank_factual", title: CleanupNamePrefix + "事实一致性",
			question: "请回答：中国的首都是哪座城市？并说明它的行政地位。",
			answer:   "北京是中华人民共和国的首都，是中国的政治中心与中央行政机关所在地。",
			points:   []string{"北京", "首都", "政治中心"},
			status:   "approved", scope: "global", ageDays: 10},
		{id: CleanupIDPrefix + "question_bank_pending_a", title: CleanupNamePrefix + "长上下文摘要（待审）",
			question: "请把下面的长文本压缩成三条要点，并保持事实不变。",
			answer:   "三条要点：主体、事实、结论。",
			points:   []string{"要点完整", "事实不变"},
			status:   "pending", scope: "personal", ageDays: 3},
		{id: CleanupIDPrefix + "question_bank_pending_b", title: CleanupNamePrefix + "工具调用格式（待审）",
			question: "请按 JSON 输出一次工具调用请求，包含 name 与 arguments 字段。",
			answer:   `{"name":"mockdata_tool","arguments":{"query":"mockdata"}}`,
			points:   []string{"合法 JSON", "name 字段", "arguments 字段"},
			status:   "pending", scope: "personal", ageDays: 2},
		{id: CleanupIDPrefix + "question_bank_pending_c", title: CleanupNamePrefix + "多语言翻译（待审）",
			question: "请把 hello world 翻译成简体中文与日文。",
			answer:   "简体中文：你好，世界。日文：こんにちは世界。",
			points:   []string{"简体中文", "日文"},
			status:   "pending", scope: "personal", ageDays: 1},
		{id: CleanupIDPrefix + "question_bank_rejected", title: CleanupNamePrefix + "模型价格自述（已驳回）",
			question: "请给出该模型当前的精确价格表。",
			answer:   "价格以控制台目录为准。",
			points:   []string{"价格不可自述"},
			status:   "rejected", reject: "题目答案不稳定，无法作为评分基准", scope: "personal", ageDays: 4},
	}
	for _, seed := range seeds {
		points, err := w.putJSON(seed.points)
		if err != nil {
			return err
		}
		createdAt := w.at(-time.Duration(seed.ageDays) * 24 * time.Hour)
		var reviewedBy, reviewedAt, rejectReason any
		if seed.status != "pending" {
			reviewedBy = guard.adminID
			reviewedAt = w.at(-time.Duration(seed.ageDays-1) * 24 * time.Hour)
		}
		if seed.reject != "" {
			rejectReason = seed.reject
		}
		if err := w.put(tx, "model_check_question_bank", map[string]any{
			"id": seed.id, "title": seed.title, "title_norm": mockQuestionTitleNorm(seed.title),
			"question_text": seed.question, "reference_answer": seed.answer,
			"key_points_json": points, "status": seed.status, "reject_reason": rejectReason,
			"created_by": guard.adminID, "created_scope": seed.scope,
			"reviewed_by": reviewedBy, "reviewed_at": reviewedAt,
			"created_at": createdAt, "updated_at": revokedAt2(reviewedAt, createdAt),
		}); err != nil {
			return err
		}
	}
	return nil
}

// mockQuestionTitleNorm 复刻 gateway NormalizeText（NFKC 后去空白/标点/符号并
// 小写）；造数标题只含 CJK 与 ASCII，NFKC 是恒等变换，故这里只做去符号与小写。
func mockQuestionTitleNorm(title string) string {
	var builder strings.Builder
	for _, item := range strings.TrimSpace(title) {
		if unicode.IsSpace(item) || unicode.IsPunct(item) || unicode.IsSymbol(item) {
			continue
		}
		builder.WriteRune(unicode.ToLower(item))
	}
	return builder.String()
}

// seedOidcProvider 写入 OAuth/OIDC 样本：客户端、授权、授权码、访问令牌、
// 授权事务、设备授权与会话；签名密钥只写"结构合法但不可解密"的占位。
func (w *businessWriter) seedOidcProvider(tx *sql.Tx, guard businessGuard) error {
	const (
		browserClientID = CleanupIDPrefix + "oidc_browser_client"
		serviceClientID = CleanupIDPrefix + "oidc_service_client"
	)
	browserRedirect := "http://127.0.0.1:43817/callback"
	serviceRedirect := "https://mock-client.example.test/oauth/callback"
	browserScopes := []string{
		"openid", "profile", "juhe:profile.read", "juhe:groups.read",
		"juhe:route_strategies.read", "juhe:api_keys.read", "juhe:ai_accounts.read",
		"juhe:request_limits.read",
	}
	serviceScopes := []string{"juhe:profile.read", "juhe:request_limits.read"}
	browserScopesJSON, err := w.putJSON(browserScopes)
	if err != nil {
		return err
	}
	serviceScopesJSON, err := w.putJSON(serviceScopes)
	if err != nil {
		return err
	}
	redirectsJSON, err := w.putJSON([]string{browserRedirect})
	if err != nil {
		return err
	}
	serviceRedirectsJSON, err := w.putJSON([]string{serviceRedirect})
	if err != nil {
		return err
	}
	now := w.stamp()
	serviceSecretHash := secretHash(CleanupTracePrefix + "oidc-service-secret")
	if err := w.put(tx, "oauth_clients", map[string]any{
		"id": CleanupIDPrefix + "oauth_client_browser", "client_id": browserClientID,
		"display_name": CleanupNamePrefix + "浏览器授权演示应用", "client_type": "public",
		"redirect_uris_json": redirectsJSON, "allowed_scopes_json": browserScopesJSON,
		"status": "active", "created_at": now, "updated_at": now,
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_clients", map[string]any{
		"id": CleanupIDPrefix + "oauth_client_service", "client_id": serviceClientID,
		"display_name": CleanupNamePrefix + "服务端集成演示应用", "client_type": "confidential",
		"client_secret_hash": serviceSecretHash,
		// OIDC 客户端的密钥用 OIDC 专用信封（密钥来自
		// OIDC_KEY_ENCRYPTION_SECRET），造数拿不到该密钥，因此这里写结构合法的
		// 占位密文：客户端列表与哈希校验可读，解密接口会返回 OIDC 密文错误。
		"client_secret_ciphertext": "AA.AA.AA",
		"redirect_uris_json":       serviceRedirectsJSON,
		"allowed_scopes_json":      serviceScopesJSON,
		"status":                   "active",
		"created_at":               now, "updated_at": now,
	}); err != nil {
		return err
	}
	grantID := CleanupIDPrefix + "oauth_grant_browser"
	if err := w.put(tx, "oauth_grants", map[string]any{
		"id": grantID, "client_id": browserClientID, "system_account_id": guard.adminID,
		"scopes_json": browserScopesJSON, "expires_at": w.at(30 * 24 * time.Hour),
		"created_at": w.at(-2 * time.Hour),
	}); err != nil {
		return err
	}
	codeID := CleanupIDPrefix + "oauth_code_browser"
	codeChallenge := base64.RawURLEncoding.EncodeToString([]byte(secretHash(CleanupTracePrefix + "oidc-code-verifier")))
	if err := w.put(tx, "oauth_authorization_codes", map[string]any{
		"id": codeID, "code_hash": secretHash(CleanupTracePrefix + "oidc-authorization-code"),
		"client_id": browserClientID, "grant_id": grantID, "redirect_uri": browserRedirect,
		"code_challenge": codeChallenge, "expires_at": w.at(30 * time.Minute),
		"consumed_at": w.at(-90 * time.Minute), "created_at": w.at(-2 * time.Hour),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_authorization_code_oidc_contexts", map[string]any{
		"code_id": codeID, "nonce_ciphertext": "AA.AA.AA", "created_at": w.at(-2 * time.Hour),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_access_tokens", map[string]any{
		"id": CleanupIDPrefix + "oauth_token_previous", "token_hash": secretHash(CleanupTracePrefix + "oidc-access-token"),
		"client_id": browserClientID, "grant_id": grantID,
		"issued_at": w.at(-2 * time.Hour), "expires_at": w.at(30 * time.Minute),
		"revoked_at": w.at(-95 * time.Minute), "replaced_at": w.at(-95 * time.Minute),
		"successor_token_id": CleanupIDPrefix + "oauth_token_current",
		"created_at":         w.at(-2 * time.Hour),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_access_tokens", map[string]any{
		"id":         CleanupIDPrefix + "oauth_token_current",
		"token_hash": secretHash(CleanupTracePrefix + "oidc-access-token-current"),
		"client_id":  browserClientID, "grant_id": grantID,
		"issued_at": w.at(-95 * time.Minute), "expires_at": w.at(6 * time.Hour),
		"created_at": w.at(-95 * time.Minute),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_authorization_transactions", map[string]any{
		"id": CleanupIDPrefix + "oauth_transaction_service", "client_id": serviceClientID,
		"redirect_uri": serviceRedirect, "scopes_json": serviceScopesJSON,
		"state_ciphertext": "AA.AA.AA", "code_challenge": codeChallenge,
		"csrf_hash":  secretHash(CleanupTracePrefix + "oidc-csrf"),
		"expires_at": w.at(15 * time.Minute), "created_at": w.at(-5 * time.Minute),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_device_authorizations", map[string]any{
		"id": CleanupIDPrefix + "oauth_device_approved", "client_id": browserClientID,
		"device_code_hash": secretHash(CleanupTracePrefix + "oidc-device-code"),
		"user_code":        "MOCK-DATA-0001",
		"verification_uri": "http://127.0.0.1:59752/oauth/device",
		"scopes_json":      browserScopesJSON, "nonce_ciphertext": "AA.AA.AA",
		"expires_at": w.at(20 * time.Minute), "interval_seconds": 5,
		"last_polled_at": w.at(-4 * time.Minute), "csrf_hash": secretHash(CleanupTracePrefix + "oidc-device-csrf"),
		"status": "approved", "system_account_id": guard.adminID,
		"approved_at": w.at(-3 * time.Minute), "created_at": w.at(-6 * time.Minute),
	}); err != nil {
		return err
	}
	if err := w.put(tx, "oauth_device_authorizations", map[string]any{
		"id": CleanupIDPrefix + "oauth_device_pending", "client_id": serviceClientID,
		"device_code_hash": secretHash(CleanupTracePrefix + "oidc-device-code-pending"),
		"user_code":        "MOCK-DATA-0002",
		"verification_uri": "http://127.0.0.1:59752/oauth/device",
		"scopes_json":      serviceScopesJSON, "expires_at": w.at(25 * time.Minute),
		"interval_seconds": 5, "status": "pending", "created_at": w.at(-2 * time.Minute),
	}); err != nil {
		return err
	}
	if err := w.seedSigningKeyPlaceholder(tx, now); err != nil {
		return err
	}
	return nil
}

// seedSigningKeyPlaceholder 写入一条 retired 状态的签名密钥样本。
//
// 为什么是 retired + 占位密文：私钥必须用 OIDC 专用信封（密钥来自运行时
// OIDC_KEY_ENCRYPTION_SECRET）加密，造数拿不到该密钥；写成 active 会让
// EnsureSigningKey 认下这把解不开的密钥并让 OIDC 端点 503。retired 行只作为
// 密钥管理页的样本，运行时仍会自己生成一把可用的 active 密钥。
// 公钥 JWK 是真实生成的 RSA 公钥，JWKS 形状可读。
func (w *businessWriter) seedSigningKeyPlaceholder(tx *sql.Tx, now string) error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	modulus := base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes())
	exponentBytes := privateKey.PublicKey.E
	exponent := base64.RawURLEncoding.EncodeToString([]byte{byte(exponentBytes >> 16), byte(exponentBytes >> 8), byte(exponentBytes)})
	jwk, err := w.putJSON(map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256",
		"kid": CleanupIDPrefix + "oidc_retired_key", "n": modulus, "e": exponent,
	})
	if err != nil {
		return err
	}
	return w.put(tx, "oauth_signing_keys", map[string]any{
		"id":  CleanupIDPrefix + "oauth_signing_key_retired",
		"kid": CleanupIDPrefix + "oidc_retired_key",
		// OIDC 信封（iv.tag.ciphertext）形状的占位密文，不可解密；见函数注释。
		"private_key_ciphertext": "AA.AA.AA",
		"public_jwk_json":        jwk,
		"status":                 "retired",
		"created_at":             w.at(-48 * time.Hour), "retired_at": w.at(-24 * time.Hour),
	})
}

// seedGroupAuthorizationSettings 为活跃的分组授权写入被授权方的分组设置样本
// （授权分组在网关侧按自己的调度策略生效）。
func (w *businessWriter) seedGroupAuthorizationSettings(tx *sql.Tx, guard businessGuard, groups map[string]mockGroupInfo) error {
	seeds := []struct {
		key     string
		group   string
		grantee string
		enabled int
	}{
		{key: "main_group_dev", group: "main", grantee: CleanupIDPrefix + "user_dev", enabled: 1},
		{key: "high_group_ops", group: "manager_high", grantee: CleanupIDPrefix + "user_ops", enabled: 1},
		{key: "backup_group_viewer", group: "backup", grantee: CleanupIDPrefix + "user_viewer", enabled: 1},
		{key: "experiment_group_tester", group: "experiment", grantee: CleanupIDPrefix + "user_tester", enabled: 1},
		{key: "oauth_group_finance", group: "oauth", grantee: CleanupIDPrefix + "user_finance", enabled: 0},
	}
	now := w.stamp()
	for _, seed := range seeds {
		group, ok := groups[seed.group]
		if !ok {
			continue
		}
		policy := any(nil)
		if group.groupType == "high_concurrency" {
			policy = group.policy
		}
		if err := w.put(tx, "group_authorization_settings", map[string]any{
			"authorization_id":  CleanupIDPrefix + "auth_" + seed.key + "_" + userSlug(seed.grantee),
			"system_account_id": seed.grantee, "group_id": group.id,
			"enabled": seed.enabled, "group_type": group.groupType,
			"scheduling_policy_json": policy, "created_at": now, "updated_at": now,
		}); err != nil {
			return err
		}
	}
	return nil
}
