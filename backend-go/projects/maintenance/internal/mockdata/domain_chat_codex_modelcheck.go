package mockdata

// seedChatCodexModelCheck 是 chat / codex context / J3b 模型检测域：chat 会话与
// 消息、图片资产、上下文检查点，codex context state 分片（会话 / 响应状态 /
// 压缩摘要），以及 J3b 模型检测专库（运行、检查项、受控 observation、可信度
// 投影、Token 基线、调度任务、小时健康，以及持久化输入 / 认领 / 产出）。
//
// 覆盖点（docs/functions/Mockdata造数设计.md「验证点」）：
//   - chat：置顶会话、达轮次上限会话、reasoning / 工具调用 / 图片输出消息、
//     图片生成与编辑、上下文检查点与条目；
//   - codex context state：分片按运行时哈希落盘，每个分片库自带会话 / 响应 /
//     摘要三表；除自然分布样本外另按目标分片挑选 ID（分片对齐组），保证
//     Paths.CodexContextShardCount 的每一个分片各有一整套会话 / 响应 / 摘要；
//   - 模型检测：快速 / 深度 × 运行中 / 已完成 / 失败 / 已取消，受控 observation
//     只绑定深度样本，并通过与真实游标投影同形的行生成账户最新可信结果、
//     回执游标与 Token 拦截基线。
//
// 数据来源与边界（为什么这样写）：
//   - 会话与检测运行必须绑定 business 域真实存在的 mock 资源（系统账户、API
//     Key、AI 账户、分组）与题库中 status='approved' 的题目：运行时读 business
//     库取真实 ID，查不到就跳过并记日志，绝不猜 ID。
//   - 列定义来自 internal/schema 的 chat / codex-context DDL（gateway 运行时
//     同样的 DDL）；J3b 专库的 DDL 由 internal/j3bmodelcheck 的既有 ensure 入口
//     创建，本域不另写 DDL。
//   - 资产图片必须真的落在 Paths.ChatAssetsRoot 下：前端 <img> 按 storage_key
//     取文件，只有数据库行没有文件就是 404。
//   - 幂等：每次先按清理标识删除本域上一批行再重建。不用 INSERT OR REPLACE：
//     REPLACE 会先删父行并（在开启外键的连接上）级联删掉子行，显式 DELETE +
//     INSERT 的顺序可控。
//   - 已知缺口（供主代理决策，不在本文件权限内）：
//   - codex context 的 payload 段文件（storage_key 指向的
//     sessions/<session>/segments/<hour>.json.gz）不落盘：Paths 没有该存储根的
//     解析字段（gateway 用 JUHE_AI_CODEX_CONTEXT_ROOT），凭分片根去猜父目录会
//     在用户覆盖分片根时写到错误位置。sha256 / 压缩与原始字节数按真实写入语义
//     （gzip 后取摘要与大小）计算，只有文件缺席。
//   - J3b 专库在 Paths.ModelCheck，不存在时本域建库建表；owner 租约、live 认领
//     与 pending 调度任务一律不写（伪造持有者会让本机后端误判）。
//   - 可信度投影表是「与真实游标聚合器同形」的行，不是调用聚合器产生：本模块
//     不得 import gateway（Go 三项目基线），因此按 aggregator 的写入形状与
//     observation 事实自洽地落库，并在注释里标注每个计数的推导来源。

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// 词汇表：与 gateway / 前端 model-checks 的取值域逐字对齐（改这里必须同步
// frontend/src/types/domain/model-checks.ts 与 gateway modelcheckowner 的校验）。
const (
	chatCodexImageModelGPT     = "gpt-image-2"
	chatCodexImageSize         = "1024x1024"
	chatCodexImageQuality      = "high"
	chatCodexQuickProbeSet     = "multi-provider-model-check-quick-v2-light-suite"
	chatCodexFullProbeSet      = "multi-provider-model-check-v4-gpt56-preview"
	chatCodexTokenizerVersion  = "js-tiktoken@1.0.21:o200k_base"
	chatCodexFeatureVersion    = "identity-features-v2-seven-categories"
	chatCodexIdentityProbe     = "generated-canary-v2-seven-categories"
	chatCodexTokenProbeVersion = "token-integrity-v1"
	chatCodexTrustScopeKey     = "model-trust-observation-aggregation"
	chatCodexTrustOwnerID      = "mockdata_owner_local"
	// chatCodexQuizQuestionLimit 是一次运行绑定的题目上限：检查项、resultSummary
	// 的 customQuiz 与 policy_snapshot.customQuestionIds 都用它，保证三处一致。
	chatCodexQuizQuestionLimit   = 3
	chatCodexFallbackModel       = "deepseek-v4.1-flash"
	chatCodexFallbackProvider    = "openai"
	chatCodexFallbackProfile     = "profile_openai_openai_v1"
	chatCodexEndpointFamily      = "openai_responses"
	chatCodexTurnLimit           = 50
	chatCodexRetentionDays       = 30
	chatCodexCodexContextTTLDays = 7
	// chatCodexAssetRetentionDays 与 gateway chat 资产保留期同量级：只要让
	// 样本在本地长期可读，过期时间不参与任何断言。
	chatCodexAssetRetentionDays = 30
)

// chatCodexChatTables 是 chat 段要写的表：任何一张缺失都说明 chat 库还没
// bootstrap（--ensure-schema），此时跳过整段而不是自行建表。
var chatCodexChatTables = []string{
	"chat_conversations",
	"chat_messages",
	"chat_message_idempotency",
	"chat_user_storage_windows",
	"chat_user_asset_usage",
	"chat_context_checkpoints",
	"chat_context_entries",
	"chat_assets",
	"chat_asset_references",
	"chat_image_generations",
}

// seedChatCodexModelCheck 是域入口：资源解析 → chat → codex context → J3b。
//
// 三段各自独立判存、独立跳过：某段的存储或前置资源缺失只让该段留空并记日志，
// 不影响其他段。整体跳过（resources.skip 非空）时 Counts 为空，覆盖报告据此
// 把本域断言判成 not-covered 而不是「接线了但一行没写」。
func seedChatCodexModelCheck(ctx context.Context, e *env) (DomainResult, error) {
	resources, err := loadChatCodexResources(ctx, e)
	if err != nil {
		return DomainResult{}, err
	}
	if resources.skip != "" {
		e.logger.Info("mockdata chat/codex/模型检测域跳过", "reason", resources.skip)
		return DomainResult{Name: DomainChatCodexModelCheck}, nil
	}
	counts := map[string]int{}
	now := e.options.clock().UTC()
	writer := &chatCodexWriter{ctx: ctx, e: e, now: now, counts: counts}
	steps := []struct {
		name string
		run  func(*chatCodexWriter, chatCodexResources) error
	}{
		{"chat 会话与消息", seedChatSection},
		{"codex context 分片", seedCodexContextSection},
		{"J3b 模型检测", seedModelCheckSection},
	}
	for _, step := range steps {
		if err := step.run(writer, resources); err != nil {
			return DomainResult{Name: DomainChatCodexModelCheck, Counts: counts}, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return DomainResult{Name: DomainChatCodexModelCheck, Counts: counts}, nil
}

// ---------------------------------------------------------------------------
// business 域资源
// ---------------------------------------------------------------------------

// chatCodexUser 是 business 库的一条 mock 系统账户。
type chatCodexUser struct {
	ID       string
	Username string
}

// chatCodexAPIKey 是 business 库的一条 mock API Key。
type chatCodexAPIKey struct {
	ID    string
	Name  string
	Owner string
}

// chatCodexAccount 是 business 库的一条 mock AI 账户。
type chatCodexAccount struct {
	ID           string
	Name         string
	Owner        string
	Provider     string
	Profile      string
	GroupID      string
	Model        string
	Protocol     string
	EndpointMode string
}

// chatCodexQuestion 是题库里 status='approved' 的题目。
type chatCodexQuestion struct {
	ID    string
	Title string
}

// chatCodexResources 是 chat / codex / 模型检测三段要引用的真实外键集合。
type chatCodexResources struct {
	skip      string
	users     []chatCodexUser
	apiKeys   []chatCodexAPIKey
	accounts  []chatCodexAccount
	groups    []string
	models    []string
	questions []chatCodexQuestion
}

// loadChatCodexResources 只读解析 business 库的 mock 资源。缺库 / 缺表 / 缺
// mock 行时返回 skip 原因而不是错误：造数必须能在空数据根上跑完。
func loadChatCodexResources(ctx context.Context, e *env) (chatCodexResources, error) {
	resources := chatCodexResources{}
	for _, table := range []string{"system_accounts", "api_keys", "accounts", "groups", "model_check_question_bank"} {
		exists, err := e.existsTable(ctx, StoreBusiness, table)
		if err != nil {
			return resources, err
		}
		if !exists {
			resources.skip = "business 库缺少表 " + table + "（请先执行 --ensure-schema --seed 与 business 域造数）"
			return resources, nil
		}
	}
	db, err := e.openExisting(StoreBusiness)
	if err != nil {
		return resources, err
	}
	if db == nil {
		resources.skip = "business 库文件不存在"
		return resources, nil
	}

	userRows, err := db.QueryContext(ctx, `SELECT id, username FROM system_accounts WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 系统账户: %w", err)
	}
	for userRows.Next() {
		var row chatCodexUser
		if err := userRows.Scan(&row.ID, &row.Username); err != nil {
			userRows.Close()
			return resources, err
		}
		resources.users = append(resources.users, row)
	}
	if err := userRows.Err(); err != nil {
		userRows.Close()
		return resources, err
	}
	userRows.Close()

	keyRows, err := db.QueryContext(ctx, `SELECT id, name, system_account_id FROM api_keys WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock API Key: %w", err)
	}
	for keyRows.Next() {
		var row chatCodexAPIKey
		if err := keyRows.Scan(&row.ID, &row.Name, &row.Owner); err != nil {
			keyRows.Close()
			return resources, err
		}
		resources.apiKeys = append(resources.apiKeys, row)
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return resources, err
	}
	keyRows.Close()

	accountRows, err := db.QueryContext(ctx, `SELECT id, COALESCE(name,''), system_account_id, COALESCE(provider_code,''),
		COALESCE(provider_protocol_profile_id,''), COALESCE(protocol_code,''), COALESCE(health_check_endpoint_mode,''),
		COALESCE(health_check_model,'')
		FROM accounts WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock AI 账户: %w", err)
	}
	type rawAccount struct {
		row    chatCodexAccount
		health string
	}
	var rawAccounts []rawAccount
	for accountRows.Next() {
		var item rawAccount
		if err := accountRows.Scan(&item.row.ID, &item.row.Name, &item.row.Owner, &item.row.Provider,
			&item.row.Profile, &item.row.Protocol, &item.row.EndpointMode, &item.health); err != nil {
			accountRows.Close()
			return resources, err
		}
		rawAccounts = append(rawAccounts, item)
	}
	if err := accountRows.Err(); err != nil {
		accountRows.Close()
		return resources, err
	}
	accountRows.Close()

	groupRows, err := db.QueryContext(ctx, `SELECT id FROM groups WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 分组: %w", err)
	}
	for groupRows.Next() {
		var id string
		if err := groupRows.Scan(&id); err != nil {
			groupRows.Close()
			return resources, err
		}
		resources.groups = append(resources.groups, id)
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return resources, err
	}
	groupRows.Close()

	// 账户检测模型：账户支持模型优先（运行记录里的 model 就是账户支持的模型），
	// 其次是体检模型，最后回落到目录模型。
	modelByAccount := map[string]string{}
	modelRows, err := db.QueryContext(ctx, `SELECT account_id, model FROM account_supported_models ORDER BY account_id, model`)
	if err != nil {
		return resources, fmt.Errorf("读取账户支持模型: %w", err)
	}
	for modelRows.Next() {
		var accountID, model string
		if err := modelRows.Scan(&accountID, &model); err != nil {
			modelRows.Close()
			return resources, err
		}
		if _, ok := modelByAccount[accountID]; !ok {
			modelByAccount[accountID] = model
		}
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return resources, err
	}
	modelRows.Close()

	catalogRows, err := db.QueryContext(ctx, `SELECT model FROM provider_model_catalog WHERE status = 'active' AND COALESCE(mode,'') <> 'image' ORDER BY catalog_order, model`)
	if err != nil {
		return resources, fmt.Errorf("读取模型目录: %w", err)
	}
	for catalogRows.Next() {
		var model string
		if err := catalogRows.Scan(&model); err != nil {
			catalogRows.Close()
			return resources, err
		}
		resources.models = append(resources.models, model)
	}
	if err := catalogRows.Err(); err != nil {
		catalogRows.Close()
		return resources, err
	}
	catalogRows.Close()

	for index, item := range rawAccounts {
		model := modelByAccount[item.row.ID]
		if model == "" {
			model = item.health
		}
		if model == "" && len(resources.models) > 0 {
			model = resources.models[index%len(resources.models)]
		}
		if model == "" {
			model = chatCodexFallbackModel
		}
		item.row.Model = model
		if len(resources.groups) > 0 {
			item.row.GroupID = resources.groups[index%len(resources.groups)]
		}
		if item.row.Provider == "" {
			item.row.Provider = chatCodexFallbackProvider
		}
		if item.row.Profile == "" {
			item.row.Profile = chatCodexFallbackProfile
		}
		if item.row.Protocol == "" {
			item.row.Protocol = "openai_responses"
		}
		if item.row.EndpointMode == "" {
			item.row.EndpointMode = "responses_sse"
		}
		resources.accounts = append(resources.accounts, item.row)
	}

	questionRows, err := db.QueryContext(ctx, `SELECT id, title FROM model_check_question_bank WHERE status = 'approved' ORDER BY id`)
	if err != nil {
		return resources, fmt.Errorf("读取题库已审核题目: %w", err)
	}
	for questionRows.Next() {
		var row chatCodexQuestion
		if err := questionRows.Scan(&row.ID, &row.Title); err != nil {
			questionRows.Close()
			return resources, err
		}
		resources.questions = append(resources.questions, row)
	}
	if err := questionRows.Err(); err != nil {
		questionRows.Close()
		return resources, err
	}
	questionRows.Close()

	if len(resources.users) == 0 {
		resources.skip = "business 库没有 mock 系统账户（请先执行 business 域造数）"
		return resources, nil
	}
	if len(resources.apiKeys) == 0 {
		resources.skip = "business 库没有 mock API Key（请先执行 business 域造数）"
		return resources, nil
	}
	return resources, nil
}

// userAt / accountAt / modelAt 越界回落，保证样本构造不越界也不 panic。
func (r chatCodexResources) userAt(index int) chatCodexUser {
	if index < 0 || index >= len(r.users) {
		index = 0
	}
	return r.users[index]
}

func (r chatCodexResources) accountAt(index int) (chatCodexAccount, bool) {
	if len(r.accounts) == 0 {
		return chatCodexAccount{}, false
	}
	if index < 0 {
		return r.accounts[0], true
	}
	return r.accounts[index%len(r.accounts)], true
}

// keyForOwner 取该用户名下的一条 Key；没有就返回空（调用方写 NULL）。
func (r chatCodexResources) keyForOwner(owner string) (chatCodexAPIKey, bool) {
	for _, key := range r.apiKeys {
		if key.Owner == owner {
			return key, true
		}
	}
	return chatCodexAPIKey{}, false
}

func (r chatCodexResources) groupAt(index int) string {
	if len(r.groups) == 0 {
		return ""
	}
	if index < 0 {
		index = 0
	}
	return r.groups[index%len(r.groups)]
}

// ---------------------------------------------------------------------------
// 写入器
// ---------------------------------------------------------------------------

// chatCodexWriter 承载一次本域写入的时钟与按表行数（DomainResult.Counts 直接取它）。
type chatCodexWriter struct {
	ctx    context.Context
	e      *env
	now    time.Time
	counts map[string]int
}

// chatCodexExecer 是 *sql.DB / *sql.Tx 的最小公共面（分片写入要在一个事务里
// 写完三张表）。
type chatCodexExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// chatCodexInsert 按列名排序拼 INSERT：同一批数据每次生成逐字相同的语句，
// 便于用 SQL 文本或 fuzz 断言回放。
func chatCodexInsert(ctx context.Context, execer chatCodexExecer, table string, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("造数写入 %s 需要至少一列", table)
	}
	names := make([]string, 0, len(columns))
	for column := range columns {
		names = append(names, column)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	values := make([]any, len(names))
	for index, column := range names {
		placeholders[index] = "?"
		values[index] = columns[column]
	}
	query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	_, err := execer.ExecContext(ctx, query, values...)
	return err
}

// put 写入一行并记账。
func (w *chatCodexWriter) put(store, table string, columns map[string]any) error {
	db, err := w.e.open(store)
	if err != nil {
		return err
	}
	if err := chatCodexInsert(w.ctx, db, table, columns); err != nil {
		return fmt.Errorf("写入 %s.%s: %w", store, table, err)
	}
	w.counts[table]++
	return nil
}

// exec 执行一条语句（清理用）。
func (w *chatCodexWriter) exec(store, query string, args ...any) error {
	if _, err := w.e.exec(w.ctx, store, query, args...); err != nil {
		return fmt.Errorf("%s: %w", store, err)
	}
	return nil
}

// clear 按条件删除本域上一批行。表不存在不是错误（本域刚创建过或该段会跳过），
// 但语句本身出错必须冒泡。
func (w *chatCodexWriter) clear(store, table, condition string, args ...any) error {
	exists, err := w.e.existsTable(w.ctx, store, table)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return w.exec(store, "DELETE FROM "+table+" WHERE "+condition, args...)
}

// clearByMarker 删除「标识列命中清理标识」的行：本域每个表至少有一个这样的列，
// 这是清理之外的第二道幂等保证（第一次运行删 0 行）。
func (w *chatCodexWriter) clearByMarker(store, table string, columns ...string) error {
	conditions := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		conditions = append(conditions, column+" LIKE ?")
		args = append(args, CleanupIDPrefix+"%")
	}
	return w.clear(store, table, strings.Join(conditions, " OR "), args...)
}

// clearMarkerAnywhere 删除「标识列在任意位置含清理标识」的行。
//
// 用于标识由多段拼接而成的列（例如 J3b 的 identity_key =
// <账户所有者>:<账户>:<模型>:<profile>:actor:<所有者>）：当账户所有者是 business
// 的既有管理员而不是 mock 用户时，前缀匹配不成立，只有包含匹配能收敛上一批行。
func (w *chatCodexWriter) clearMarkerAnywhere(store, table string, columns ...string) error {
	conditions := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		conditions = append(conditions, column+" LIKE ?")
		args = append(args, "%"+CleanupIDPrefix+"%")
	}
	return w.clear(store, table, strings.Join(conditions, " OR "), args...)
}

// ---------------------------------------------------------------------------
// chat 段
// ---------------------------------------------------------------------------

// seedChatSection 写 chat 库。库或表不存在时跳过：chat 库由 --ensure-schema
// 建表，本域不自行造 chat schema（避免「数据根配错却悄悄建了一个空库」）。
func seedChatSection(w *chatCodexWriter, resources chatCodexResources) error {
	existing, err := w.e.openExisting(StoreChat)
	if err != nil {
		return err
	}
	if existing == nil {
		chatCodexSkip(w, "chat 库文件不存在（请先执行 --ensure-schema）", StoreChat, "")
		return nil
	}
	for _, table := range chatCodexChatTables {
		ready, err := w.e.existsTable(w.ctx, StoreChat, table)
		if err != nil {
			return err
		}
		if !ready {
			chatCodexSkip(w, "chat 库缺少表（请先执行 --ensure-schema）", StoreChat, table)
			return nil
		}
	}
	return writeChatSection(w, resources)
}

// chatCodexSkip 记录一次跳过：跳过不是失败，但必须可查，否则「某张表为什么是
// 空的」无从追溯。
func chatCodexSkip(w *chatCodexWriter, reason, store, table string) {
	w.e.logger.Warn("mockdata chat/codex/模型检测域跳过", "reason", reason, "store", store, "table", table)
}

// chatCodexMessage 是一条待写消息样本。
type chatCodexMessage struct {
	ID              string
	Sequence        int
	Role            string
	Status          string
	Text            string
	Blocks          []map[string]any
	Model           string
	TurnID          string
	ClientMessageID string
	Trace           string
	FinishReason    string
	ErrorCode       string
	ErrorMessage    string
	CreatedAt       time.Time
	CompletedAt     time.Time
}

// chatCodexAsset 是一条待写资产样本（文件字节与存储键由写文件步骤生成）。
type chatCodexAsset struct {
	ID           string
	SourceKind   string
	Filename     string
	Conversation string
	Owner        string
	TurnID       string
	MessageID    string
	Bytes        []byte
	MimeType     string
	Width        int
	Height       int
	WithPreview  bool
	PreviewBytes []byte
	QuotaBytes   int
	CommittedAt  time.Time
	CreatedAt    time.Time
}

// writeChatSection 是 chat 段的实际写入：先清后插，顺序为子表 → 父表。
func writeChatSection(w *chatCodexWriter, resources chatCodexResources) error {
	user := resources.userAt(0)
	key, hasKey := resources.keyForOwner(user.ID)
	if !hasKey {
		key = resources.apiKeys[0]
	}
	model := chatCodexFallbackModel
	if len(resources.models) > 0 {
		model = resources.models[0]
	}
	now := w.now
	stamp := func(value time.Time) string { return value.UTC().Format(isoMillisLayout) }

	if err := clearChatSection(w, user.ID); err != nil {
		return err
	}

	// —— 会话样本：置顶（带检查点）、普通交替、达轮次上限。
	conversations := []struct {
		id            string
		title         string
		pinned        int
		nextSequence  int
		userTurns     int
		messageRev    int
		checkpointID  string
		compacted     int
		contextRev    int
		activeTokens  any
		limitTokens   any
		usageEstimate int
		createdAt     time.Time
		lastMessageAt time.Time
		activeCheckpt bool
	}{
		{
			id: "mockdata_chat_conversation_pinned", title: CleanupNamePrefix + "置顶图片会话", pinned: 1,
			nextSequence: 7, userTurns: 3, messageRev: 3, checkpointID: "mockdata_chat_checkpoint_pinned",
			compacted: 4, contextRev: 1, activeTokens: 1536, limitTokens: 400000, usageEstimate: 0,
			createdAt: now.Add(-72 * time.Hour), lastMessageAt: now.Add(-2 * time.Hour), activeCheckpt: true,
		},
		{
			id: "mockdata_chat_conversation_default", title: CleanupNamePrefix + "默认会话", pinned: 0,
			nextSequence: 3, userTurns: 1, messageRev: 1,
			createdAt: now.Add(-6 * time.Hour), lastMessageAt: now.Add(-5 * time.Hour),
		},
		{
			// 达轮次上限：gateway 默认 ChatMaxTurnsPerConversation=50，这里写到
			// 第 50 轮（next_sequence_no=101，最后一轮消息在 99/100），页面应能
			// 展示「已达轮次上限」的阻断态。
			id: "mockdata_chat_conversation_turn_limit", title: CleanupNamePrefix + "达轮次上限会话", pinned: 0,
			nextSequence: 101, userTurns: chatCodexTurnLimit, messageRev: chatCodexTurnLimit,
			createdAt: now.Add(-30 * time.Hour), lastMessageAt: now.Add(-27 * time.Hour),
		},
	}
	for _, conversation := range conversations {
		columns := map[string]any{
			"id": conversation.id, "system_account_id": user.ID, "api_key_id": key.ID,
			"api_key_name_snapshot": key.Name, "title": conversation.title, "title_source_message_id": nil,
			"is_pinned": conversation.pinned, "last_model": model,
			"default_image_model": chatCodexImageModelGPT, "next_sequence_no": conversation.nextSequence,
			"user_turn_count": conversation.userTurns, "message_revision": conversation.messageRev,
			"active_turn_id": nil, "active_started_at": nil, "context_revision": conversation.contextRev,
			"active_checkpoint_id": nil, "compacted_through_sequence": 0, "context_state": "ready",
			"active_context_tokens": nil, "effective_context_limit_tokens": nil,
			"context_usage_estimated": 1, "context_claim_id": nil, "context_claim_revision": nil,
			"context_claim_through_sequence": nil, "context_claimed_at": nil, "context_retry_at": nil,
			"context_attempt_count": 0, "context_error_code": nil, "context_progress_sequence": 0,
			"context_progress_earliest_expires_at": nil, "last_message_at": stamp(conversation.lastMessageAt),
			"created_at": stamp(conversation.createdAt), "updated_at": stamp(conversation.lastMessageAt),
		}
		if conversation.activeCheckpt {
			// 检查点把 compacted_through_sequence 推到 4，并把 active 检查点挂上；
			// 其余会话两个字段必须同时为空/为零（DDL 的 CHECK 组合）。
			columns["active_checkpoint_id"] = conversation.checkpointID
			columns["compacted_through_sequence"] = conversation.compacted
			columns["active_context_tokens"] = conversation.activeTokens
			columns["effective_context_limit_tokens"] = conversation.limitTokens
			columns["context_usage_estimated"] = conversation.usageEstimate
			columns["context_attempt_count"] = 1
		}
		if err := w.put(StoreChat, "chat_conversations", columns); err != nil {
			return err
		}
	}

	// —— 消息样本：置顶会话含 reasoning / 工具调用 / 图片输出，普通会话一对
	// 交替消息，上限会话含失败与图片轮次。
	toolCall := map[string]any{
		"type": "tool_call", "blockId": "mockdata_chat_block_tool_1", "order": 2,
		"callId": "mockdata_chat_call_1", "toolType": "web_search", "status": "completed",
		"item": map[string]any{"type": "web_search_call", "status": "completed", "query": "造数-本地联调"},
	}
	assistantBlocks := func(text, reasoning string, outputAssetID string) []map[string]any {
		blocks := []map[string]any{}
		if reasoning != "" {
			blocks = append(blocks, map[string]any{
				"type": "reasoning", "blockId": "mockdata_chat_block_reasoning", "order": 1,
				"text": reasoning, "status": "completed",
			})
		}
		blocks = append(blocks, toolCall)
		blocks = append(blocks, map[string]any{
			"type": "text", "blockId": "mockdata_chat_block_text", "order": 3, "text": text, "status": "completed",
		})
		if outputAssetID != "" {
			blocks = append(blocks, map[string]any{
				"type": "output_image", "blockId": "mockdata_chat_block_image", "order": 4,
				"assetId": outputAssetID, "mimeType": "image/png", "status": "completed",
				"width": 1, "height": 1, "revisedPrompt": CleanupNamePrefix + "图像输出样本",
			})
		}
		return blocks
	}
	userBlocks := func(text, assetID string) []map[string]any {
		blocks := []map[string]any{}
		if assetID != "" {
			blocks = append(blocks, map[string]any{
				"type": "input_image", "blockId": "mockdata_chat_block_input_image", "order": 0, "assetId": assetID,
			})
		}
		blocks = append(blocks, map[string]any{
			"type": "text", "blockId": "mockdata_chat_block_user_text", "order": 1, "text": text,
		})
		return blocks
	}

	const (
		assetUploadID   = "mockdata_chat_asset_upload"
		assetGenerateID = "mockdata_chat_asset_generated"
		assetEditID     = "mockdata_chat_asset_edited"
	)
	pinnedConv := conversations[0].id
	defaultConv := conversations[1].id
	limitConv := conversations[2].id
	messages := []chatCodexMessage{
		{
			ID: "mockdata_chat_message_pinned_1", Sequence: 1, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "帮我看这张图并生成一张同风格的新图", Blocks: userBlocks("帮我看这张图并生成一张同风格的新图", assetUploadID),
			Model: model, TurnID: "mockdata_chat_turn_pinned_1", ClientMessageID: "mockdata_chat_client_pinned_1",
			CreatedAt: now.Add(-26 * time.Hour), CompletedAt: now.Add(-26 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_pinned_2", Sequence: 2, Role: "assistant", Status: "completed",
			Text:   CleanupNamePrefix + "已生成图片并整理要点",
			Blocks: assistantBlocks("已生成图片并整理要点", CleanupNamePrefix+"先拆解图片构图，再生成同风格新图", assetGenerateID),
			Model:  model, TurnID: "mockdata_chat_turn_pinned_1", Trace: CleanupTracePrefix + "chat-pinned-1",
			FinishReason: "stop", CreatedAt: now.Add(-26*time.Hour + time.Minute), CompletedAt: now.Add(-25*time.Hour + 58*time.Minute),
		},
		{
			ID: "mockdata_chat_message_pinned_3", Sequence: 3, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "把背景换成纯白，尺寸不变", Blocks: userBlocks("把背景换成纯白，尺寸不变", ""),
			Model: model, TurnID: "mockdata_chat_turn_pinned_2", ClientMessageID: "mockdata_chat_client_pinned_2",
			CreatedAt: now.Add(-24 * time.Hour), CompletedAt: now.Add(-24 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_pinned_4", Sequence: 4, Role: "assistant", Status: "completed",
			Text:   CleanupNamePrefix + "已按原图改成纯白背景",
			Blocks: assistantBlocks("已按原图改成纯白背景", CleanupNamePrefix+"先读取原图，再按尺寸约束重绘背景", assetEditID),
			Model:  model, TurnID: "mockdata_chat_turn_pinned_2", Trace: CleanupTracePrefix + "chat-pinned-2",
			FinishReason: "stop", CreatedAt: now.Add(-24*time.Hour + time.Minute), CompletedAt: now.Add(-23*time.Hour + 59*time.Minute),
		},
		{
			ID: "mockdata_chat_message_pinned_5", Sequence: 5, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "再补充一版配色说明", Blocks: userBlocks("再补充一版配色说明", ""),
			Model: model, TurnID: "mockdata_chat_turn_pinned_3", ClientMessageID: "mockdata_chat_client_pinned_3",
			CreatedAt: now.Add(-3 * time.Hour), CompletedAt: now.Add(-3 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_pinned_6", Sequence: 6, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "配色说明：主色 #1F6FEB，辅助色 #F2F4F7",
			Blocks: []map[string]any{{"type": "text", "blockId": "mockdata_chat_block_text", "order": 1,
				"text": CleanupNamePrefix + "配色说明：主色 #1F6FEB，辅助色 #F2F4F7", "status": "completed"}},
			Model: model, TurnID: "mockdata_chat_turn_pinned_3", Trace: CleanupTracePrefix + "chat-pinned-3",
			FinishReason: "stop", CreatedAt: now.Add(-2 * time.Hour), CompletedAt: now.Add(-2*time.Hour + time.Minute),
		},
		{
			ID: "mockdata_chat_message_default_1", Sequence: 1, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "帮我写一段本地联调说明", Blocks: userBlocks("帮我写一段本地联调说明", ""),
			Model: model, TurnID: "mockdata_chat_turn_default_1", ClientMessageID: "mockdata_chat_client_default_1",
			CreatedAt: now.Add(-6 * time.Hour), CompletedAt: now.Add(-6 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_default_2", Sequence: 2, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "本地联调说明：先跑 ensure-schema 再跑 mockdata",
			Blocks: []map[string]any{{"type": "text", "blockId": "mockdata_chat_block_text", "order": 1,
				"text": CleanupNamePrefix + "本地联调说明：先跑 ensure-schema 再跑 mockdata", "status": "completed"}},
			Model: model, TurnID: "mockdata_chat_turn_default_1", Trace: CleanupTracePrefix + "chat-default-1",
			FinishReason: "stop", CreatedAt: now.Add(-6*time.Hour + time.Minute), CompletedAt: now.Add(-5*time.Hour + 58*time.Minute),
		},
		{
			ID: "mockdata_chat_message_limit_97", Sequence: 97, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "第 49 轮：再核对一次缓存命中率", Blocks: userBlocks("第 49 轮：再核对一次缓存命中率", ""),
			Model: model, TurnID: "mockdata_chat_turn_limit_49", ClientMessageID: "mockdata_chat_client_limit_49",
			CreatedAt: now.Add(-28 * time.Hour), CompletedAt: now.Add(-28 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_limit_98", Sequence: 98, Role: "assistant", Status: "failed",
			Text: "", Blocks: []map[string]any{},
			Model: model, TurnID: "mockdata_chat_turn_limit_49", Trace: CleanupTracePrefix + "chat-limit-49",
			ErrorCode: "upstream_error", ErrorMessage: CleanupNamePrefix + "上游返回 429，本轮失败留档",
			CreatedAt: now.Add(-28*time.Hour + time.Minute), CompletedAt: now.Add(-28*time.Hour + 2*time.Minute),
		},
		{
			ID: "mockdata_chat_message_limit_99", Sequence: 99, Role: "user", Status: "completed",
			Text: CleanupNamePrefix + "第 50 轮：给出最终结论", Blocks: userBlocks("第 50 轮：给出最终结论", ""),
			Model: model, TurnID: "mockdata_chat_turn_limit_50", ClientMessageID: "mockdata_chat_client_limit_50",
			CreatedAt: now.Add(-27 * time.Hour), CompletedAt: now.Add(-27 * time.Hour),
		},
		{
			ID: "mockdata_chat_message_limit_100", Sequence: 100, Role: "assistant", Status: "completed",
			Text: CleanupNamePrefix + "最终结论：缓存命中率 92%，无需调整",
			Blocks: []map[string]any{{"type": "text", "blockId": "mockdata_chat_block_text", "order": 1,
				"text": CleanupNamePrefix + "最终结论：缓存命中率 92%，无需调整", "status": "completed"}},
			Model: model, TurnID: "mockdata_chat_turn_limit_50", Trace: CleanupTracePrefix + "chat-limit-50",
			FinishReason: "stop", CreatedAt: now.Add(-27*time.Hour + time.Minute), CompletedAt: now.Add(-26*time.Hour - 50*time.Minute),
		},
	}
	conversationOf := map[string]string{
		"mockdata_chat_message_pinned_1":  pinnedConv,
		"mockdata_chat_message_pinned_2":  pinnedConv,
		"mockdata_chat_message_pinned_3":  pinnedConv,
		"mockdata_chat_message_pinned_4":  pinnedConv,
		"mockdata_chat_message_pinned_5":  pinnedConv,
		"mockdata_chat_message_pinned_6":  pinnedConv,
		"mockdata_chat_message_default_1": defaultConv,
		"mockdata_chat_message_default_2": defaultConv,
		"mockdata_chat_message_limit_97":  limitConv,
		"mockdata_chat_message_limit_98":  limitConv,
		"mockdata_chat_message_limit_99":  limitConv,
		"mockdata_chat_message_limit_100": limitConv,
	}
	// 存储窗口按消息的真实创建日期分桶：窗口行必须与消息字节数自洽，否则
	// 「用户存储用量」页面会与消息列表矛盾。
	windowBytes := map[string]int{}
	windowOrder := []string{}
	for _, message := range messages {
		conversationID := conversationOf[message.ID]
		blocks, err := json.Marshal(message.Blocks)
		if err != nil {
			return err
		}
		contentBytes := len(message.Text) + len(blocks)
		completedAt := any(nil)
		if !message.CompletedAt.IsZero() {
			completedAt = stamp(message.CompletedAt)
		}
		columns := map[string]any{
			"id": message.ID, "conversation_id": conversationID, "system_account_id": user.ID,
			"turn_id": message.TurnID, "sequence_no": message.Sequence, "client_message_id": nil,
			"role": message.Role, "status": message.Status, "content_text": message.Text,
			"content_blocks_json": string(blocks), "content_bytes": contentBytes, "storage_reserved_bytes": 0,
			"model": message.Model, "trace_id": nil, "finish_reason": nil, "error_code": nil, "error_message": nil,
			"created_at": stamp(message.CreatedAt), "completed_at": completedAt,
			"expires_at": stamp(message.CreatedAt.Add(chatCodexRetentionDays * 24 * time.Hour)),
		}
		if message.ClientMessageID != "" {
			columns["client_message_id"] = message.ClientMessageID
		}
		if message.Trace != "" {
			columns["trace_id"] = message.Trace
		}
		if message.FinishReason != "" {
			columns["finish_reason"] = message.FinishReason
		}
		if message.ErrorCode != "" {
			columns["error_code"] = message.ErrorCode
			columns["error_message"] = message.ErrorMessage
		}
		if err := w.put(StoreChat, "chat_messages", columns); err != nil {
			return err
		}
		bucket := message.CreatedAt.UTC().Format("2006-01-02")
		if _, seen := windowBytes[bucket]; !seen {
			windowOrder = append(windowOrder, bucket)
		}
		windowBytes[bucket] += contentBytes
	}
	sort.Strings(windowOrder)
	for _, bucket := range windowOrder {
		if err := w.put(StoreChat, "chat_user_storage_windows", map[string]any{
			"system_account_id": user.ID, "bucket_date": bucket, "content_bytes": windowBytes[bucket],
			"reserved_bytes": 0, "updated_at": stamp(now),
		}); err != nil {
			return err
		}
	}

	// —— 幂等键：与 default 会话第 1 轮的 user/assistant 消息一一对应
	//（替换轮次的读路径要求 (conversation, turn) 恰好一行且 id 一致）。
	if err := w.put(StoreChat, "chat_message_idempotency", map[string]any{
		"conversation_id": defaultConv, "client_message_id": "mockdata_chat_client_default_1",
		"system_account_id": user.ID, "turn_id": "mockdata_chat_turn_default_1",
		"user_message_id": "mockdata_chat_message_default_1", "assistant_message_id": "mockdata_chat_message_default_2",
		"created_at": stamp(now.Add(-6 * time.Hour)),
		"expires_at": stamp(now.Add(-6*time.Hour + 24*time.Hour)),
	}); err != nil {
		return err
	}

	// —— 上下文检查点与条目：只服务置顶会话（active 检查点每个会话唯一）。
	payloadDigest := chatCodexDigest([]byte("mockdata-chat-checkpoint-payload"))
	checkpointID := "mockdata_chat_checkpoint_pinned"
	if err := w.put(StoreChat, "chat_context_checkpoints", map[string]any{
		"id": checkpointID, "conversation_id": pinnedConv, "system_account_id": user.ID,
		"version": 1, "source_revision": 1, "source_from_sequence": 1, "source_through_sequence": 4,
		"recent_tail_from_sequence": 5, "entry_from_sequence": 1, "entry_through_sequence": 3,
		"payload_digest": payloadDigest, "estimated_input_tokens": 1536, "upstream_input_tokens": 1500,
		"request_body_bytes": 8192, "model_id": model, "provider_code": resources.accounts[0].Provider,
		"provider_profile_id": resources.accounts[0].Profile, "endpoint_family": chatCodexEndpointFamily,
		"compact_compatibility_hash": payloadDigest, "prompt_version": "chat-context-v1",
		"status": "active", "quality_status": "passed",
		"created_at": stamp(now.Add(-2 * time.Hour)), "expires_at": stamp(now.Add(20 * 24 * time.Hour)),
	}); err != nil {
		return err
	}
	entries := []struct {
		sequence int
		sourceID string
		kind     string
		content  string
		bytes    int
		source   string
		trust    string
		tokens   int
	}{
		{1, "mockdata_chat_message_pinned_1", "verbatim", `{"role":"user","text":"帮我看这张图并生成一张同风格的新图"}`, 60, "user", "untrusted", 28},
		{2, "mockdata_chat_message_pinned_2", "task_state", `{"task":"生成同风格新图","state":"done"}`, 44, "assistant", "assistant_derived", 18},
		{3, "mockdata_chat_message_pinned_3", "tool_result", `{"tool":"web_search","resultCount":3}`, 40, "tool", "untrusted", 15},
	}
	for _, entry := range entries {
		if err := w.put(StoreChat, "chat_context_entries", map[string]any{
			"conversation_id": pinnedConv, "checkpoint_id": checkpointID, "sequence": entry.sequence,
			"source_message_id": entry.sourceID, "kind": entry.kind, "content_json": entry.content,
			"content_bytes": entry.bytes, "provenance": entry.source, "trust_level": entry.trust,
			"token_count": entry.tokens, "created_at": stamp(now.Add(-2 * time.Hour)),
			"expires_at": stamp(now.Add(20 * 24 * time.Hour)),
		}); err != nil {
			return err
		}
	}

	// —— 资产与图片：数据库行 + ChatAssetsRoot 下的真实文件（前端按 storage_key
	// 直接取字节，只写行就是 404）。
	uploadPNG := chatCodexPNG(1, 1)
	generatedPNG := chatCodexPNG(1, 1)
	editedJPEG := chatCodexJPEG(1, 1)
	previewWebP := chatCodexWebP()
	assets := []chatCodexAsset{
		{
			ID: assetUploadID, SourceKind: "user_upload", Filename: CleanupNamePrefix + "输入图.png",
			Conversation: pinnedConv, Owner: user.ID, TurnID: "mockdata_chat_turn_pinned_1",
			MessageID: "mockdata_chat_message_pinned_1", Bytes: uploadPNG, MimeType: "image/png",
			Width: 1, Height: 1, QuotaBytes: len(uploadPNG),
			CommittedAt: now.Add(-26 * time.Hour), CreatedAt: now.Add(-26 * time.Hour),
		},
		{
			ID: assetGenerateID, SourceKind: "assistant_generated", Filename: CleanupNamePrefix + "生成图.png",
			Conversation: pinnedConv, Owner: user.ID, TurnID: "mockdata_chat_turn_pinned_1",
			MessageID: "mockdata_chat_message_pinned_2", Bytes: generatedPNG, MimeType: "image/png",
			Width: 1, Height: 1, WithPreview: true, PreviewBytes: previewWebP, QuotaBytes: len(generatedPNG),
			CommittedAt: now.Add(-25*time.Hour - 58*time.Minute), CreatedAt: now.Add(-26 * time.Hour),
		},
		{
			ID: assetEditID, SourceKind: "assistant_generated", Filename: CleanupNamePrefix + "编辑图.jpg",
			Conversation: pinnedConv, Owner: user.ID, TurnID: "mockdata_chat_turn_pinned_2",
			MessageID: "mockdata_chat_message_pinned_4", Bytes: editedJPEG, MimeType: "image/jpeg",
			Width: 1, Height: 1, WithPreview: true, PreviewBytes: previewWebP, QuotaBytes: len(editedJPEG),
			CommittedAt: now.Add(-23*time.Hour - 59*time.Minute), CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	assetBytesTotal := 0
	for _, asset := range assets {
		digest := chatCodexDigest(asset.Bytes)
		storageKey := chatCodexStorageKey(asset.ID, digest, asset.MimeType, "")
		if err := chatCodexWriteAssetFile(w.e.options.Paths.ChatAssetsRoot, storageKey, asset.Bytes); err != nil {
			return err
		}
		columns := map[string]any{
			"id": asset.ID, "system_account_id": asset.Owner, "conversation_id": asset.Conversation,
			"source_kind": asset.SourceKind, "original_filename": asset.Filename,
			"original_mime_type": asset.MimeType, "original_width": asset.Width, "original_height": asset.Height,
			"original_bytes": len(asset.Bytes), "original_sha256": digest,
			"processed_mime_type": asset.MimeType, "processed_width": asset.Width, "processed_height": asset.Height,
			"processed_bytes": len(asset.Bytes), "processed_sha256": digest, "storage_key": storageKey,
			"preview_mime_type": nil, "preview_width": nil, "preview_height": nil, "preview_bytes": nil,
			"preview_sha256": nil, "preview_storage_key": nil,
			"processing_status": "ready", "processing_error_code": nil,
			"observation_status": "not_requested", "observation_json": nil, "observation_revision": 0,
			"observation_claim_id": nil, "observation_claimed_at": nil, "quota_bytes": asset.QuotaBytes,
			"turn_id": asset.TurnID, "message_id": asset.MessageID, "committed_at": stamp(asset.CommittedAt),
			"cleanup_status": "active", "cleanup_claim_id": nil, "cleanup_attempt_count": 0,
			"cleanup_claimed_at": nil, "cleanup_retry_at": nil, "cleanup_error_code": nil,
			"created_at": stamp(asset.CreatedAt), "updated_at": stamp(asset.CommittedAt),
			"expires_at": stamp(asset.CreatedAt.Add(chatCodexAssetRetentionDays * 24 * time.Hour)),
		}
		if asset.WithPreview {
			previewDigest := chatCodexDigest(asset.PreviewBytes)
			previewKey := chatCodexStorageKey(asset.ID, previewDigest, "image/webp", "preview")
			if err := chatCodexWriteAssetFile(w.e.options.Paths.ChatAssetsRoot, previewKey, asset.PreviewBytes); err != nil {
				return err
			}
			// assistant_generated 必须有 preview_storage_key（DDL CHECK），预览
			// 组字段也要么全空要么全非空。
			columns["preview_mime_type"] = "image/webp"
			columns["preview_width"] = asset.Width
			columns["preview_height"] = asset.Height
			columns["preview_bytes"] = len(asset.PreviewBytes)
			columns["preview_sha256"] = previewDigest
			columns["preview_storage_key"] = previewKey
		}
		if err := w.put(StoreChat, "chat_assets", columns); err != nil {
			return err
		}
		assetBytesTotal += asset.QuotaBytes
	}
	if err := w.put(StoreChat, "chat_user_asset_usage", map[string]any{
		"system_account_id": user.ID, "asset_bytes": assetBytesTotal, "asset_count": len(assets),
		"updated_at": stamp(now),
	}); err != nil {
		return err
	}

	references := []struct {
		assetID   string
		messageID string
		kind      string
		order     int
	}{
		{assetUploadID, "mockdata_chat_message_pinned_1", "user_input", 0},
		{assetGenerateID, "mockdata_chat_message_pinned_2", "assistant_output", 0},
		{assetEditID, "mockdata_chat_message_pinned_4", "assistant_output", 0},
	}
	for index, reference := range references {
		if err := w.put(StoreChat, "chat_asset_references", map[string]any{
			"asset_id": reference.assetID, "conversation_id": pinnedConv, "turn_id": assets[index].TurnID,
			"message_id": reference.messageID, "reference_kind": reference.kind, "content_order": reference.order,
			"created_at": stamp(assets[index].CreatedAt),
			"expires_at": stamp(assets[index].CreatedAt.Add(chatCodexAssetRetentionDays * 24 * time.Hour)),
		}); err != nil {
			return err
		}
	}

	generations := []struct {
		assetID     string
		operation   string
		prompt      string
		sourceIDs   string
		rootAssetID string
		outputFmt   string
		createdAt   time.Time
	}{
		{
			assetID: assetGenerateID, operation: "generate",
			prompt: CleanupNamePrefix + "生成一张同风格极小图", sourceIDs: "[]", rootAssetID: assetGenerateID,
			outputFmt: "png", createdAt: now.Add(-26 * time.Hour),
		},
		{
			assetID: assetEditID, operation: "edit",
			prompt: CleanupNamePrefix + "把背景换成纯白，尺寸不变", sourceIDs: `["` + assetUploadID + `"]`,
			rootAssetID: assetUploadID, outputFmt: "jpeg", createdAt: now.Add(-24 * time.Hour),
		},
	}
	for _, generation := range generations {
		if err := w.put(StoreChat, "chat_image_generations", map[string]any{
			"asset_id": generation.assetID, "conversation_id": pinnedConv, "system_account_id": user.ID,
			"operation": generation.operation, "model": chatCodexImageModelGPT, "prompt": generation.prompt,
			"source_asset_ids_json": generation.sourceIDs, "root_asset_id": generation.rootAssetID,
			"size": chatCodexImageSize, "quality": chatCodexImageQuality, "output_format": generation.outputFmt,
			"created_at": stamp(generation.createdAt),
			"expires_at": stamp(generation.createdAt.Add(chatCodexAssetRetentionDays * 24 * time.Hour)),
		}); err != nil {
			return err
		}
	}
	return nil
}

// clearChatSection 按清理标识删除 chat 库上一批行，顺序为子表 → 父表。
func clearChatSection(w *chatCodexWriter, ownerID string) error {
	steps := []struct {
		table   string
		columns []string
	}{
		{"chat_image_generations", []string{"asset_id", "conversation_id", "root_asset_id"}},
		{"chat_asset_references", []string{"asset_id", "conversation_id", "message_id"}},
		{"chat_context_entries", []string{"checkpoint_id", "conversation_id"}},
		{"chat_context_checkpoints", []string{"id", "conversation_id"}},
		{"chat_assets", []string{"id", "conversation_id"}},
		{"chat_message_idempotency", []string{"conversation_id", "client_message_id"}},
		{"chat_messages", []string{"id", "conversation_id", "turn_id"}},
		{"chat_user_storage_windows", []string{"system_account_id"}},
		{"chat_user_asset_usage", []string{"system_account_id"}},
		{"chat_conversations", []string{"id"}},
	}
	for _, step := range steps {
		if err := w.clearByMarker(StoreChat, step.table, step.columns...); err != nil {
			return err
		}
	}
	// 存储窗口按 (user, bucket_date) 建行：日期会随时间漂移，标识列删除足够，
	// 但同一用户的历史行必须收敛，这里再按 owner 兜底一次。
	if err := w.clear(StoreChat, "chat_user_storage_windows", "system_account_id = ?", ownerID); err != nil {
		return err
	}
	// 用户资产用量只以 system_account_id 为主键，行内没有别的标识列：当账户
	// 所有者不是 mock 用户时前缀匹配不成立，必须显式按 owner 删。
	return w.clear(StoreChat, "chat_user_asset_usage", "system_account_id = ?", ownerID)
}

// ---------------------------------------------------------------------------
// 图片字节与存储键
// ---------------------------------------------------------------------------

// chatCodexPNG 生成极小合法 PNG（标准库编码器保证 magic 与 CRC 正确）。
func chatCodexPNG(width, height int) []byte {
	buffer := &bytes.Buffer{}
	// 1x1 全透明 RGBA 足够：前端只需要能解码出图片并走完 <img> 渲染。
	if err := png.Encode(buffer, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		// 编码到内存缓冲区不会失败（标准库对 RGBA 恒可编码）；真失败说明环境
		// 异常，用 panic 暴露而不是写出半张图。
		panic(err)
	}
	return buffer.Bytes()
}

// chatCodexJPEG 生成极小合法 JPEG（覆盖 JPEG magic 的资产样本）。
func chatCodexJPEG(width, height int) []byte {
	buffer := &bytes.Buffer{}
	if err := jpeg.Encode(buffer, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

// chatCodexWebP 返回最小的 1x1 无损 WebP（34 字节）。
//
// 为什么手写字节：chat_assets 的 preview_mime_type 只允许 image/webp，而标准库
// 没有 WebP 编码器。容器由本函数按规范拼出（RIFF 头 + VP8L 块 + 奇数块填充），
// VP8L payload 是公开的最小无损位图样本（1x1、无 alpha、版本 0）：浏览器能正常
// 解码，预览不会变成破图。
func chatCodexWebP() []byte {
	payload := []byte{0x2f, 0x00, 0x00, 0x00, 0x10, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07}
	// RIFF size = 文件长度 - 8 = 4（"WEBP"）+ 8（VP8L 块头）+ payload + 1 字节填充。
	file := []byte{'R', 'I', 'F', 'F'}
	file = appendLittleEndian(file, uint32(4+8+len(payload)+1))
	file = append(file, 'W', 'E', 'B', 'P', 'V', 'P', '8', 'L')
	file = appendLittleEndian(file, uint32(len(payload)))
	file = append(file, payload...)
	return append(file, 0x00)
}

// appendLittleEndian 追加一个小端 uint32（RIFF/VP8L 块头都是小端长度字段）。
func appendLittleEndian(target []byte, value uint32) []byte {
	return append(target, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
}

// chatCodexStorageKey 复刻 gateway 的 storageKeyForChatAsset：存储键是
// <摘要前两位>/<次两位>/<资产 ID>[-preview]-<摘要前十六位><扩展名>。
func chatCodexStorageKey(assetID, digestHex, mimeType, variant string) string {
	digest := strings.ToLower(strings.TrimSpace(digestHex))
	if len(digest) != 64 {
		digest = strings.Repeat("0", 64)
	}
	suffix := ""
	if variant != "" {
		suffix = "-" + variant
	}
	return fmt.Sprintf("%s/%s/%s%s-%s%s", digest[:2], digest[2:4], assetID, suffix, digest[:16], chatCodexImageExtension(mimeType))
}

// chatCodexImageExtension 与 chatAssetObjectExtension 对齐。
func chatCodexImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

// chatCodexWriteAssetFile 把资产字节写到资产根下。
//
// 只写库内衍生出的键（十六进制摘要 + 受控 ID，不含 ".."），因此路径永远落在
// 资产根内；仍然显式做一次包含性检查，避免将来键格式改动时写到数据根之外。
func chatCodexWriteAssetFile(root, storageKey string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("聊天资产 %s 字节为空", storageKey)
	}
	target := filepath.Join(root, filepath.FromSlash(filepath.Clean(storageKey)))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "" || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return fmt.Errorf("聊天资产存储键越出受限目录: %s", storageKey)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建聊天资产目录 %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return fmt.Errorf("写入聊天资产 %s: %w", target, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// codex context state 分片
// ---------------------------------------------------------------------------

// chatCodexCodexRow 是一条待写分片行（响应 / 摘要共用）。
type chatCodexCodexRow struct {
	id         string
	sessionID  string
	previousID string
	model      string
	payload    map[string]any
	createdAt  time.Time
	lastUsedAt time.Time
}

// chatCodexSessionSeed 是一组 codex 会话样本（会话 + 响应 + 摘要）。
type chatCodexSessionSeed struct {
	id           string
	groupID      string
	responses    []chatCodexCodexRow
	compacts     []chatCodexCodexRow
	sourceRespID string
	createdAt    time.Time
}

// chatCodexNaturalSessions 返回固定 ID 的两组样本：它们的行落在哈希算出的分片上，
// 与运行时真实写入的分布一致（同一会话的响应与摘要也是各按自己的键分片）。
func chatCodexNaturalSessions(account chatCodexAccount, resources chatCodexResources, now time.Time) []chatCodexSessionSeed {
	return []chatCodexSessionSeed{
		{
			id: "mockdata_codex_session_responses", groupID: resources.groupAt(0),
			createdAt: now.Add(-20 * time.Hour),
			responses: []chatCodexCodexRow{
				{
					id: "mockdata_codex_response_1", sessionID: "mockdata_codex_session_responses",
					model: account.Model, createdAt: now.Add(-20 * time.Hour), lastUsedAt: now.Add(-18 * time.Hour),
					payload: map[string]any{"responseId": "mockdata_codex_response_1", "model": account.Model, "output": "造数-首次响应"},
				},
				{
					id: "mockdata_codex_response_2", sessionID: "mockdata_codex_session_responses",
					previousID: "mockdata_codex_response_1", model: account.Model,
					createdAt: now.Add(-18 * time.Hour), lastUsedAt: now.Add(-17 * time.Hour),
					payload: map[string]any{"responseId": "mockdata_codex_response_2", "model": account.Model, "output": "造数-续写响应"},
				},
			},
			compacts: []chatCodexCodexRow{
				{
					id: "mockdata_codex_compact_1", sessionID: "mockdata_codex_session_responses",
					previousID: "mockdata_codex_response_2", model: account.Model,
					createdAt: now.Add(-17 * time.Hour), lastUsedAt: now.Add(-16 * time.Hour),
					payload: map[string]any{"compactId": "mockdata_codex_compact_1", "summary": "造数-上下文压缩摘要"},
				},
			},
		},
		{
			id: "mockdata_codex_session_compacted", groupID: resources.groupAt(1),
			createdAt: now.Add(-6 * time.Hour), sourceRespID: "mockdata_codex_response_3",
			responses: []chatCodexCodexRow{
				{
					id: "mockdata_codex_response_3", sessionID: "mockdata_codex_session_compacted",
					model: account.Model, createdAt: now.Add(-6 * time.Hour), lastUsedAt: now.Add(-5 * time.Hour),
					payload: map[string]any{"responseId": "mockdata_codex_response_3", "model": account.Model, "output": "造数-新会话响应"},
				},
			},
			compacts: []chatCodexCodexRow{
				{
					id: "mockdata_codex_compact_2", sessionID: "mockdata_codex_session_compacted",
					previousID: "mockdata_codex_response_3", model: account.Model,
					createdAt: now.Add(-5 * time.Hour), lastUsedAt: now.Add(-4 * time.Hour),
					payload: map[string]any{"compactId": "mockdata_codex_compact_2", "summary": "造数-新会话压缩摘要"},
				},
			},
		},
	}
}

// chatCodexAlignedSessions 为每一个分片（0..shardCount-1）各生成一组会话 / 响应 /
// 摘要，三种 ID 都选成哈希命中该分片：覆盖校验要求全部 codex 分片非空，而自然
// 分布样本只落在哈希算出的少数分片里，靠哈希散布无法保证。
func chatCodexAlignedSessions(account chatCodexAccount, resources chatCodexResources, now time.Time, shardCount int) []chatCodexSessionSeed {
	groups := shardCount
	if groups < 1 {
		groups = 1
	}
	seeds := make([]chatCodexSessionSeed, 0, groups)
	for target := 0; target < groups; target++ {
		sessionID := chatCodexAlignedID(fmt.Sprintf("mockdata_codex_session_shard%d", target), target, shardCount)
		responseID := chatCodexAlignedID(fmt.Sprintf("mockdata_codex_response_shard%d", target), target, shardCount)
		compactID := chatCodexAlignedID(fmt.Sprintf("mockdata_codex_compact_shard%d", target), target, shardCount)
		createdAt := now.Add(-time.Duration(9+target) * time.Hour)
		seeds = append(seeds, chatCodexSessionSeed{
			id: sessionID, groupID: resources.groupAt(target), createdAt: createdAt,
			responses: []chatCodexCodexRow{
				{
					id: responseID, sessionID: sessionID, model: account.Model,
					createdAt: createdAt, lastUsedAt: createdAt.Add(time.Hour),
					payload: map[string]any{"responseId": responseID, "model": account.Model, "shard": target, "output": "造数-分片对齐响应"},
				},
			},
			compacts: []chatCodexCodexRow{
				{
					id: compactID, sessionID: sessionID, previousID: responseID, model: account.Model,
					createdAt: createdAt.Add(time.Hour), lastUsedAt: createdAt.Add(2 * time.Hour),
					payload: map[string]any{"compactId": compactID, "shard": target, "summary": "造数-分片对齐压缩摘要"},
				},
			},
		})
	}
	return seeds
}

// chatCodexAlignedID 在固定候选序列里挑一个哈希落在目标分片的 ID。
//
// 分片位置由运行时的哈希决定，造数不能把行直接塞进某个分片；要让指定分片拿到
// 一整套样本，只能挑 ID。候选序列固定，因此每次运行选出的 ID 相同（可复现）。
func chatCodexAlignedID(prefix string, target, shardCount int) string {
	for index := 0; index < 4096; index++ {
		candidate := fmt.Sprintf("%s_%d", prefix, index)
		if chatCodexShardIndex(candidate, shardCount) == target {
			return candidate
		}
	}
	// 分片数 1 时唯一分片就是 0，搜索必然命中；其余情况 4096 个候选里没命中
	// 的概率可忽略，这里返回首个候选只是为了让函数有返回值。
	return prefix + "_0"
}

// seedCodexContextSection 写 codex context state 分片。
//
// 分片位置不是自选的：运行时的 databaseForKey 用 FNV-1a（UTF-16 码元）取模
// 分片数决定行落在哪个 state-NNN.sqlite3，本域复刻同一函数，让造数行落在运行时
// 真正会读取的分片里。缺分组 / 缺账户时跳过并记日志。
func seedCodexContextSection(w *chatCodexWriter, resources chatCodexResources) error {
	account, ok := resources.accountAt(0)
	if !ok {
		chatCodexSkip(w, "business 库没有 mock AI 账户，codex context 分片跳过", StoreCodexContextShardPrefix, "")
		return nil
	}
	if len(resources.groups) == 0 {
		chatCodexSkip(w, "business 库没有 mock 分组（codex_context_sessions.group_id 非空），codex context 分片跳过", StoreCodexContextShardPrefix, "")
		return nil
	}
	owner := resources.userAt(0)
	key, hasKey := resources.keyForOwner(owner.ID)
	shardCount := w.e.options.Paths.CodexContextShardCount
	now := w.now

	// 样本分两类：
	//   - 自然分布组：ID 直接写好，落在哈希算出的分片上（运行时真实写入形态）；
	//   - 分片对齐组：为了让「每个分片各有一整套会话 / 响应 / 摘要」（设计
	//     文档验证点），按目标分片挑选哈希命中的 ID——分片位置仍然由运行时的
	//     哈希决定，造数只是选 ID，不绕过写入语义。
	sessions := chatCodexNaturalSessions(account, resources, now)
	sessions = append(sessions, chatCodexAlignedSessions(account, resources, now, shardCount)...)

	// 清理与建 schema 覆盖全部分片：分片位置随分片数配置变化，上一批行可能留在
	// 任意一个已存在的分片里（openExisting 不会创建缺失的分片文件）。
	for shard := 0; shard < shardCount; shard++ {
		storeName := codexContextShardName(shard)
		db, err := w.e.openExisting(storeName)
		if err != nil {
			return err
		}
		if db == nil {
			continue
		}
		if _, err := schema.EnsureSQLiteCodexContext(w.ctx, db); err != nil {
			return fmt.Errorf("建 codex context 分片 %d schema: %w", shard, err)
		}
		for _, step := range []struct {
			table   string
			columns []string
		}{
			{"codex_context_compacts", []string{"compact_id", "session_id", "source_response_id", "storage_key"}},
			{"codex_context_responses", []string{"response_id", "session_id", "previous_response_id", "storage_key"}},
			{"codex_context_sessions", []string{"id", "source_response_id", "latest_response_id", "latest_compact_id"}},
		} {
			if err := w.clearByMarker(storeName, step.table, step.columns...); err != nil {
				return err
			}
		}
	}

	rows := map[int][]struct {
		table   string
		columns map[string]any
	}{}
	appendRow := func(shard int, table string, columns map[string]any) {
		rows[shard] = append(rows[shard], struct {
			table   string
			columns map[string]any
		}{table: table, columns: columns})
	}

	latestResponse := map[string]string{}
	latestCompact := map[string]string{}
	// 段文件偏移：同一 (session, 小时) 段内的多行按写入顺序追加，与运行时
	// appendSegmentBytes 的语义一致。
	segmentOffsets := map[string]int{}
	for _, session := range sessions {
		for _, response := range session.responses {
			compressed, digest, err := chatCodexCompressPayload(response.payload)
			if err != nil {
				return err
			}
			responseShard := chatCodexShardIndex(response.id, shardCount)
			storageKey := chatCodexSegmentKey(response.sessionID, response.createdAt)
			offset := segmentOffsets[storageKey]
			segmentOffsets[storageKey] += len(compressed)
			appendRow(responseShard, "codex_context_responses", map[string]any{
				"response_id": response.id, "session_id": response.sessionID,
				"previous_response_id": chatCodexNullable(response.previousID),
				"system_account_id":    owner.ID, "api_key_id": chatCodexNullable(keyOrEmpty(key, hasKey)),
				"group_id": session.groupID, "provider_code": account.Provider,
				"upstream_account_id": account.ID, "model": response.model, "upstream_model": response.model,
				"storage_key": storageKey, "storage_offset_bytes": offset, "sha256": digest,
				"raw_size_bytes": len(chatCodexMustJSON(response.payload)), "compressed_size_bytes": len(compressed),
				"compression": "gzip", "schema_version": 1,
				"created_at": chatCodexStamp(response.createdAt), "updated_at": chatCodexStamp(response.lastUsedAt),
				"last_used_at": chatCodexStamp(response.lastUsedAt),
				"expires_at":   chatCodexStamp(response.createdAt.Add(chatCodexCodexContextTTLDays * 24 * time.Hour)),
			})
			latestResponse[session.id] = response.id
		}
		for _, compact := range session.compacts {
			compressed, digest, err := chatCodexCompressPayload(compact.payload)
			if err != nil {
				return err
			}
			compactShard := chatCodexShardIndex(compact.id, shardCount)
			storageKey := chatCodexSegmentKey(compact.sessionID, compact.createdAt)
			offset := segmentOffsets[storageKey]
			segmentOffsets[storageKey] += len(compressed)
			appendRow(compactShard, "codex_context_compacts", map[string]any{
				"compact_id": compact.id, "session_id": compact.sessionID,
				"source_response_id": chatCodexNullable(compact.previousID),
				"summary_digest":     chatCodexDigest([]byte(compact.id + "-summary")),
				"system_account_id":  owner.ID, "api_key_id": chatCodexNullable(keyOrEmpty(key, hasKey)),
				"group_id": session.groupID, "provider_code": account.Provider,
				"upstream_account_id": account.ID, "model": compact.model, "upstream_model": compact.model,
				"storage_key": storageKey, "storage_offset_bytes": offset, "sha256": digest,
				"raw_size_bytes": len(chatCodexMustJSON(compact.payload)), "compressed_size_bytes": len(compressed),
				"compression": "gzip", "schema_version": 1,
				"created_at": chatCodexStamp(compact.createdAt), "updated_at": chatCodexStamp(compact.lastUsedAt),
				"last_used_at": chatCodexStamp(compact.lastUsedAt),
				"expires_at":   chatCodexStamp(compact.createdAt.Add(chatCodexCodexContextTTLDays * 24 * time.Hour)),
			})
			latestCompact[session.id] = compact.id
		}
		sessionShard := chatCodexShardIndex(session.id, shardCount)
		appendRow(sessionShard, "codex_context_sessions", map[string]any{
			"id": session.id, "system_account_id": owner.ID, "api_key_id": chatCodexNullable(keyOrEmpty(key, hasKey)),
			"group_id": session.groupID, "provider_code": account.Provider,
			"source_response_id": chatCodexNullable(session.sourceRespID),
			"latest_response_id": chatCodexNullable(latestResponse[session.id]),
			"latest_compact_id":  chatCodexNullable(latestCompact[session.id]),
			"created_at":         chatCodexStamp(session.createdAt),
			"updated_at":         chatCodexStamp(now.Add(-time.Hour)),
			"last_used_at":       chatCodexStamp(now.Add(-time.Hour)),
			"expires_at":         chatCodexStamp(session.createdAt.Add(chatCodexCodexContextTTLDays * 24 * time.Hour)),
		})
	}

	// 写入：只打开真正要写的分片（缺失时创建）。
	shards := make([]int, 0, len(rows))
	for shard := range rows {
		shards = append(shards, shard)
	}
	sort.Ints(shards)
	for _, shard := range shards {
		storeName := codexContextShardName(shard)
		db, err := w.e.open(storeName)
		if err != nil {
			return err
		}
		if _, err := schema.EnsureSQLiteCodexContext(w.ctx, db); err != nil {
			return fmt.Errorf("建 codex context 分片 %d schema: %w", shard, err)
		}
		shardRows := rows[shard]
		err = w.e.tx(w.ctx, storeName, func(tx *sql.Tx) error {
			for _, row := range shardRows {
				if err := chatCodexInsert(w.ctx, tx, row.table, row.columns); err != nil {
					return fmt.Errorf("写入 %s.%s: %w", storeName, row.table, err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, row := range shardRows {
			w.counts[row.table]++
		}
	}
	return nil
}

// chatCodexShardIndex 复刻 gateway 的 CodexContextStateShardIndexForKey：
// FNV-1a over UTF-16 码元，模分片数。
func chatCodexShardIndex(key string, shardCount int) int {
	if shardCount < 1 {
		shardCount = 1
	}
	if shardCount > 256 {
		shardCount = 256
	}
	hash := uint32(2166136261)
	for _, unit := range utf16.Encode([]rune(key)) {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	return int(hash % uint32(shardCount))
}

// chatCodexSegmentKey 复刻 SegmentStorageKey：sessions/<segment>/segments/<YYYYMMDDHH>.json.gz。
// 段文件按小时分段，同一段内的多行按写入顺序追加，偏移由调用方累加。
func chatCodexSegmentKey(sessionID string, at time.Time) string {
	hourKey := strings.NewReplacer("-", "", "T", "", ":", "").Replace(at.UTC().Format("2006-01-02T15"))
	return fmt.Sprintf("sessions/%s/segments/%s.json.gz", chatCodexSafePathSegment(sessionID), hourKey)
}

// chatCodexSafePathSegment 复刻 safePathSegment：可读前缀 + 24 位十六进制摘要。
func chatCodexSafePathSegment(input string) string {
	normalized := strings.TrimSpace(input)
	var readable strings.Builder
	for _, char := range normalized {
		switch {
		case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z', char >= '0' && char <= '9',
			char == '_', char == '.', char == '-':
			readable.WriteRune(char)
		default:
			readable.WriteRune('_')
		}
	}
	prefix := readable.String()
	if len(prefix) > 96 {
		prefix = prefix[:96]
	}
	if prefix == "" {
		prefix = "session"
	}
	return prefix + "-" + chatCodexDigest([]byte(normalized))[:24]
}

// chatCodexCompressPayload 按运行时语义计算 payload 引用：compact JSON → gzip →
// sha256（对压缩字节）。
func chatCodexCompressPayload(payload map[string]any) ([]byte, string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	buffer := &bytes.Buffer{}
	writer := gzip.NewWriter(buffer)
	if _, err := writer.Write(raw); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	compressed := buffer.Bytes()
	return compressed, chatCodexDigest(compressed), nil
}

// chatCodexMustJSON 只在样本构造（编译期常量级数据）里使用：编码失败说明样本
// 里塞了不可序列化的值，属于编码错误。
func chatCodexMustJSON(payload map[string]any) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

// chatCodexNullable 空字符串写 NULL（codex 的可空外键列）。
func chatCodexNullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func keyOrEmpty(key chatCodexAPIKey, ok bool) string {
	if !ok {
		return ""
	}
	return key.ID
}

func chatCodexStamp(value time.Time) string {
	return value.UTC().Format(isoMillisLayout)
}

func chatCodexDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// J3b 模型检测
// ---------------------------------------------------------------------------

// chatCodexRunSeed 是一条模型检测运行样本。
type chatCodexRunSeed struct {
	key          string
	profile      string
	status       string
	level        string
	score        int
	message      string
	errorCode    string
	errorMsg     string
	trusted      bool
	ageHours     int
	accountIdx   int
	items        []chatCodexItemSeed
	observations []chatCodexObservationSeed
	quiz         bool
}

// chatCodexItemSeed 是一条检查项样本（item_type 用运行时 real 家族的 kind）。
type chatCodexItemSeed struct {
	key       string
	typ       string
	status    string
	score     int
	maxScore  int
	duration  int
	evidence  map[string]any
	errorCode string
	errorMsg  string
}

// chatCodexObservationSeed 是一条受控 observation（只出现在深度样本）。
type chatCodexObservationSeed struct {
	family        string
	roundIndex    int
	paddingTokens int
	localTokens   int
	reportedTok   *int
	cachedTok     *int
	status        string
	complete      bool
	bucket        string
	cohort        string
	population    string
	probe         string
	fingerprint   string
	features      [8]float64
}

// chatCodexObservationSample 是深度样本的受控 observation 矩阵：身份来源、
// Token 轮次与配对窗口各一条，覆盖 observation 表里与可信度相关的全部列族。
var chatCodexObservationSamples = []chatCodexObservationSeed{
	{
		family: "identity_observation", roundIndex: 1, paddingTokens: 0, localTokens: 1024,
		status: "complete", complete: true,
		bucket: "mockdata-bucket-primary", cohort: "mockdata-cohort-gpt56", population: "mockdata-population-gpt56",
		probe: "mockdata-probe-identity", fingerprint: "mockdata-fingerprint-upstream",
		features: [8]float64{0.312, 0.207, 0.118, 0.064, 0.041, 0.022, 0.014, 0.009},
	},
	{
		family: "token_integrity", roundIndex: 2, paddingTokens: 512, localTokens: 1536,
		reportedTok: chatCodexIntPtr(1586), cachedTok: chatCodexIntPtr(0),
		status: "complete", complete: true,
		bucket: "mockdata-bucket-primary", cohort: "mockdata-cohort-gpt56", population: "mockdata-population-gpt56",
		probe: "mockdata-probe-token", fingerprint: "mockdata-fingerprint-upstream",
		features: [8]float64{0.301, 0.211, 0.121, 0.061, 0.043, 0.024, 0.013, 0.010},
	},
	{
		family: "distribution", roundIndex: 3, paddingTokens: 2048, localTokens: 3072,
		reportedTok: chatCodexIntPtr(3122), cachedTok: chatCodexIntPtr(2048),
		status: "complete", complete: true,
		bucket: "mockdata-bucket-paired", cohort: "mockdata-cohort-gpt56", population: "mockdata-population-gpt56",
		probe: "mockdata-probe-paired", fingerprint: "mockdata-fingerprint-upstream",
		features: [8]float64{0.298, 0.209, 0.119, 0.066, 0.044, 0.021, 0.015, 0.008},
	},
}

// chatCodexRunMatrix 是「快速 / 深度 × 运行中 / 已完成 / 失败 / 已取消」矩阵。
// ageHours 让运行落在最近两天里（小时健康样本因此跨天），并让同一账户的
// (account_id, stat_hour) 主键不冲突。
var chatCodexRunMatrix = []chatCodexRunSeed{
	{
		key: "quick_completed", profile: "quick", status: "completed", level: "high_confidence", score: 92,
		message: "快速检测完成：协议与结构化输出全部通过", ageHours: 7, accountIdx: 0,
		items: []chatCodexItemSeed{
			{key: "protocol_basic", typ: "protocol_basic", status: "passed", score: 10, maxScore: 10, duration: 820,
				evidence: map[string]any{"success": true, "responseModel": chatCodexFallbackModel}},
			{key: "structured_output", typ: "structured_output", status: "passed", score: 15, maxScore: 15, duration: 910,
				evidence: map[string]any{"success": true, "jsonValid": true}},
			{key: "tool_calling", typ: "tool_calling", status: "passed", score: 15, maxScore: 15, duration: 1180,
				evidence: map[string]any{"success": true, "toolCallCount": 1}},
			{key: "usage_shape", typ: "usage_shape", status: "passed", score: 10, maxScore: 10, duration: 640,
				evidence: map[string]any{"success": true}},
		},
	},
	{
		key: "quick_running", profile: "quick", status: "running", level: "unavailable", score: 0,
		message: "快速检测执行中", ageHours: 14, accountIdx: 1,
	},
	{
		key: "quick_failed", profile: "quick", status: "failed", level: "unavailable", score: 0,
		message: "快速检测失败：上游返回 429", errorCode: "model_check_execution_failed",
		errorMsg: CleanupNamePrefix + "上游限流导致检测失败", ageHours: 21, accountIdx: 0,
		items: []chatCodexItemSeed{
			{key: "target.execution", typ: "execution", status: "failed", score: 0, maxScore: 100, duration: 320,
				evidence:  map[string]any{"message": CleanupNamePrefix + "上游限流导致检测失败"},
				errorCode: "model_check_execution_failed", errorMsg: CleanupNamePrefix + "上游限流导致检测失败"},
		},
	},
	{
		key: "quick_canceled", profile: "quick", status: "canceled", level: "unavailable", score: 0,
		message: "快速检测被取消", ageHours: 28, accountIdx: 1,
		items: []chatCodexItemSeed{
			{key: "protocol_basic", typ: "protocol_basic", status: "skipped", score: 0, maxScore: 10, duration: 0,
				evidence: map[string]any{"cancelled": true}},
		},
	},
	{
		key: "full_completed", profile: "full", status: "completed", level: "likely", score: 84,
		message: "深度检测完成：受控 observation 与可信对比样本已生成", ageHours: 35, accountIdx: 0,
		trusted: true, quiz: true,
		items: []chatCodexItemSeed{
			{key: "protocol_basic", typ: "protocol_basic", status: "passed", score: 10, maxScore: 10, duration: 830,
				evidence: map[string]any{"success": true, "responseModel": chatCodexFallbackModel}},
			{key: "structured_output", typ: "structured_output", status: "passed", score: 15, maxScore: 15, duration: 950,
				evidence: map[string]any{"success": true, "jsonValid": true}},
			{key: "tool_calling", typ: "tool_calling", status: "warning", score: 11, maxScore: 15, duration: 1320,
				evidence: map[string]any{"success": true, "toolCallCount": 1, "partial": true}},
			{key: "usage_shape", typ: "usage_shape", status: "passed", score: 10, maxScore: 10, duration: 700,
				evidence: map[string]any{"success": true}},
			{key: "juice", typ: "juice", status: "passed", score: 15, maxScore: 15, duration: 1420,
				evidence: map[string]any{"success": true, "hardAnomaly": false}},
			{key: "long_context", typ: "long_context", status: "passed", score: 15, maxScore: 15, duration: 2100,
				evidence: map[string]any{"success": true, "needleRate": 1.0}},
			{key: "identity_observation", typ: "identity_observation", status: "passed", score: 10, maxScore: 10, duration: 1260,
				evidence: map[string]any{"success": true, "featureVersion": chatCodexFeatureVersion,
					"probeVersion": chatCodexIdentityProbe, "observationCount": 3}},
			{key: "token_integrity", typ: "token_integrity", status: "passed", score: 10, maxScore: 10, duration: 2480,
				evidence: map[string]any{"success": true, "tokenizerVersion": chatCodexTokenizerVersion,
					"probeVersion": chatCodexTokenProbeVersion, "slope": 1.001, "intercept": 50.0, "roundCount": 3}},
			{key: "distribution", typ: "distribution", status: "passed", score: 15, maxScore: 15, duration: 1980,
				evidence: map[string]any{"success": true, "pairedProbeCount": 2}},
		},
		observations: chatCodexObservationSamples,
	},
	{
		key: "full_running", profile: "full", status: "running", level: "unavailable", score: 0,
		message: "深度检测执行中", ageHours: 42, accountIdx: 1,
		// 运行中样本同样会 append observation（投影尚未聚合），因此这里给出
		// 一条 aggregation_completed_at 为空的 observation，与脏账户行自洽。
		observations: []chatCodexObservationSeed{
			{
				family: "identity_observation", roundIndex: 1, paddingTokens: 0, localTokens: 1024,
				status: "partial", complete: false,
				bucket: "mockdata-bucket-secondary", cohort: "mockdata-cohort-gpt56", population: "mockdata-population-gpt56",
				probe: "mockdata-probe-identity", fingerprint: "mockdata-fingerprint-upstream",
				features: [8]float64{0.287, 0.201, 0.126, 0.070, 0.047, 0.026, 0.017, 0.011},
			},
		},
	},
	{
		key: "full_failed", profile: "full", status: "failed", level: "unavailable", score: 0,
		message: "深度检测失败：上游连接中断", errorCode: "model_check_execution_failed",
		errorMsg: CleanupNamePrefix + "上游连接中断导致检测失败", ageHours: 49, accountIdx: 0,
		items: []chatCodexItemSeed{
			{key: "target.execution", typ: "execution", status: "failed", score: 0, maxScore: 100, duration: 410,
				evidence:  map[string]any{"message": CleanupNamePrefix + "上游连接中断导致检测失败"},
				errorCode: "model_check_execution_failed", errorMsg: CleanupNamePrefix + "上游连接中断导致检测失败"},
		},
	},
	{
		key: "full_canceled", profile: "full", status: "canceled", level: "unavailable", score: 0,
		message: "深度检测被取消", ageHours: 56, accountIdx: 1,
	},
}

func chatCodexIntPtr(value int) *int { return &value }

// seedModelCheckSection 写 J3b 模型检测专库。
//
// 深度的已完成样本承载受控 observation 与可信度投影；运行中的深度样本给出
// 尚未聚合的 observation 与对应脏账户行。缺 mock AI 账户时跳过整段并记日志
// （运行必须绑定真实账户，不能猜 ID）。
func seedModelCheckSection(w *chatCodexWriter, resources chatCodexResources) error {
	_, ok := resources.accountAt(0)
	if !ok {
		chatCodexSkip(w, "business 库没有 mock AI 账户，模型检测段跳过", StoreModelCheck, "")
		return nil
	}
	db, err := w.e.open(StoreModelCheck)
	if err != nil {
		return err
	}
	// J3b schema 由既有 ensure 入口创建（本域不另写 DDL）：不 Ready 时 apply，
	// 已 Ready 时是纯读取校验。
	if _, err := j3bmodelcheck.RunSQLite(w.ctx, db, true); err != nil {
		return fmt.Errorf("确保 J3b SQLite schema: %w", err)
	}
	return writeModelCheckSection(w, resources)
}

// chatCodexModelCheckTables 是 J3b 段要清空的表与它们的标识列。
var chatCodexModelCheckTables = []struct {
	table   string
	columns []string
}{
	{"model_check_items", []string{"id", "run_id"}},
	{"model_check_observations", []string{"id", "run_id"}},
	{"model_check_outcomes", []string{"outcome_id", "input_id"}},
	{"model_check_execution_claims", []string{"input_id", "outcome_id"}},
	{"model_check_inputs", []string{"input_id", "identity_key", "target_id"}},
	{"model_check_input_versions", []string{"identity_key"}},
	{"model_check_runs", []string{"id", "target_id", "account_id"}},
	{"account_quality_health_hourly", []string{"account_id", "model_check_run_id", "system_account_id"}},
	{"model_check_scheduler_tasks", []string{"id"}},
	{"model_trust_observation_receipts", []string{"observation_id"}},
	{"model_account_trust_results", []string{"system_account_id", "account_id", "last_observed_id"}},
	{"model_trust_latest_dirty_accounts", []string{"system_account_id", "account_id"}},
}

// writeModelCheckSection 按矩阵写运行、检查项、observation、可信度投影、
// 基线、调度任务、小时健康与持久化输入 / 认领 / 产出。
func writeModelCheckSection(w *chatCodexWriter, resources chatCodexResources) error {
	if err := clearModelCheckSection(w); err != nil {
		return err
	}
	now := w.now
	// 深度完成样本承担 observation 与可信度投影；深度运行中样本给出尚未聚合的
	// observation 与脏账户行。
	if err := writeModelCheckRuns(w, resources); err != nil {
		return err
	}
	trustedRunID := chatCodexRunID("full_completed")
	if err := writeModelCheckItems(w, resources); err != nil {
		return err
	}
	if err := writeModelCheckObservations(w, resources); err != nil {
		return err
	}
	if err := writeModelCheckTrust(w, resources, trustedRunID, now); err != nil {
		return err
	}
	if err := writeModelCheckSchedulerTasks(w, resources, now); err != nil {
		return err
	}
	if err := writeAccountQualityHealthHourly(w, resources); err != nil {
		return err
	}
	return writeModelCheckDurableInputs(w, resources, trustedRunID)
}

func chatCodexRunID(key string) string { return CleanupIDPrefix + "model_check_run_" + key }

func chatCodexItemID(runKey string, index int) string {
	return fmt.Sprintf("%s-item-%04d", chatCodexRunID(runKey), index+1)
}

func chatCodexObservationID(runID string, index int) string {
	return fmt.Sprintf("%s-observation-family-%04d", runID, index+1)
}

// clearModelCheckSection 按标识列清空本域在 J3b 专库写出的行。
func clearModelCheckSection(w *chatCodexWriter) error {
	for _, step := range chatCodexModelCheckTables {
		if err := w.clearByMarker(StoreModelCheck, step.table, step.columns...); err != nil {
			return err
		}
	}
	// identity_key 是「账户所有者:账户:模型:profile:actor:所有者」的拼接值：账户
	// 所有者可能是 business 既有管理员（非 mock 标识），只有包含匹配能收敛。
	if err := w.clearMarkerAnywhere(StoreModelCheck, "model_check_input_versions", "identity_key"); err != nil {
		return err
	}
	// 调度任务 id 形如 "schedule:<id>:<rev>" / "recovery:<...>"，标识前缀不在
	// 首字符，按 payload 兜底再删一次。
	if err := w.clear(StoreModelCheck, "model_check_scheduler_tasks", "payload LIKE ?", "%"+CleanupIDPrefix+"%"); err != nil {
		return err
	}
	// 可信度聚合游标用真实 scope_key（运行时只会读这一个作用域），行的可删性
	// 由 cursor_id 承担。
	if err := w.clear(StoreModelCheck, "model_trust_aggregation_state", "scope_key = ? AND cursor_id LIKE ?",
		chatCodexTrustScopeKey, CleanupIDPrefix+"%"); err != nil {
		return err
	}
	// Token 拦截基线没有可承载清理标识的标识列（cohort_key_hmac 必须满足
	// hmac-sha256-v1:<64 hex> 格式，requested_model 必须是真实模型名）：只能按
	// 自己写的校准备注删除。这也是本域唯一一张 cleanup.go 通用扫描覆盖不到的
	// 表，已记入交付说明。
	return w.clear(StoreModelCheck, "model_token_intercept_baseline_versions", "calibration_note LIKE ?", CleanupNamePrefix+"%")
}

// writeModelCheckRuns 写 2×4 的运行矩阵。
func writeModelCheckRuns(w *chatCodexWriter, resources chatCodexResources) error {
	now := w.now
	for _, seed := range chatCodexRunMatrix {
		account, ok := resources.accountAt(seed.accountIdx)
		if !ok {
			continue
		}
		runID := chatCodexRunID(seed.key)
		probeSet := chatCodexQuickProbeSet
		if seed.profile == "full" {
			probeSet = chatCodexFullProbeSet
		}
		startedAt := now.Add(-time.Duration(seed.ageHours)*time.Hour - 4*time.Minute)
		finishedAt := startedAt.Add(3 * time.Minute)
		trusted := 0
		trustedAvailable := 0
		if seed.trusted {
			trusted, trustedAvailable = 1, 1
		}
		// 运行中：finished/duration/同步状态必须留空（页面据此显示执行中）；
		// 终态：写入结束时间、耗时与（已完成运行的）健康同步结果。
		var finished any
		var duration any
		var syncStatus any
		switch seed.status {
		case "running":
			finished, duration, syncStatus = nil, nil, nil
		default:
			finished = chatCodexStamp(finishedAt)
			duration = 180000
			if seed.status == "completed" {
				syncStatus = "applied"
			}
		}
		requestSummary := chatCodexRequestSummary(seed, account, probeSet)
		policySnapshot := chatCodexPolicySnapshot(resources)
		resultSummary := chatCodexResultSummary(seed, resources, now)
		qualityDecision := chatCodexQualityDecision(seed, resultSummary)
		columns := map[string]any{
			"id": runID, "system_account_id": account.Owner, "actor_system_account_id": account.Owner,
			"provider_code": account.Provider, "target_type": "account", "target_id": account.ID,
			"target_name": account.Name, "target_owner_system_account_id": account.Owner,
			"account_id": account.ID, "group_id": chatCodexNullable(account.GroupID),
			"api_key_id": chatCodexNullable(chatCodexKeyIDFor(resources, account.Owner)),
			"model":      account.Model, "profile": seed.profile, "trigger_kind": "manual", "schedule_id": nil,
			"trusted_comparison_enabled": trusted, "trusted_comparison_available": trustedAvailable,
			"status": seed.status, "level": seed.level, "score": seed.score, "max_score": 100,
			"message": seed.message, "request_summary_json": requestSummary,
			"result_summary_json": resultSummary, "policy_snapshot_json": policySnapshot,
			"quality_decision_json": qualityDecision, "probe_set_version": probeSet,
			"started_at": chatCodexStamp(startedAt), "trace_id": CleanupTracePrefix + "model-check-" + seed.key,
			"quality_health_sync_status": syncStatus, "created_at": chatCodexStamp(startedAt),
			"updated_at": chatCodexStamp(finishedAt), "finished_at": finished, "duration_ms": duration,
			"error_code": nil, "error_message": nil,
		}
		if seed.errorCode != "" {
			columns["error_code"] = seed.errorCode
			columns["error_message"] = seed.errorMsg
		}
		if err := w.put(StoreModelCheck, "model_check_runs", columns); err != nil {
			return err
		}
	}
	return nil
}

// chatCodexKeyIDFor 取该用户名下的 Key ID（没有则空串 → 写 NULL）。
func chatCodexKeyIDFor(resources chatCodexResources, owner string) string {
	if key, ok := resources.keyForOwner(owner); ok {
		return key.ID
	}
	return ""
}

// chatCodexRequestSummary 复刻 runtime 的 request_summary 快照形状。
func chatCodexRequestSummary(seed chatCodexRunSeed, account chatCodexAccount, probeSet string) string {
	snapshot := map[string]any{
		"targetType": "account", "targetId": account.ID, "targetName": account.Name,
		"targetOwnerSystemAccountId": account.Owner, "groupId": account.GroupID, "model": account.Model,
		"upstreamModel": account.Model, "profile": seed.profile, "protocol": account.Protocol,
		"providerProtocolProfileId": account.Profile, "sourceEndpointFamily": chatCodexEndpointFamily,
		"upstreamProtocol": account.Protocol, "upstreamEndpointFamily": chatCodexEndpointFamily,
		"endpointMode": account.EndpointMode, "upstreamEndpointMode": account.EndpointMode,
		"credentialType": "api_key", "upstreamAdapter": "openai-responses", "endpointFingerprint": "mockdata-endpoint",
		"probeSetVersion": probeSet, "traceId": CleanupTracePrefix + "model-check-" + seed.key,
		"configRevision": "1", "sourceConfigRevision": "1", "sourceDispatchRevision": 1,
		"policyRevision": "1", "manualEnforcementEnabled": true, "ownPhysicalAccount": true,
		"manualEnforcementEligible": true,
	}
	if seed.trusted {
		snapshot["trustedComparison"] = map[string]any{
			"accountId": account.ID, "systemAccountId": account.Owner, "configRevision": "1",
			"dispatchRevision": 1, "sourceConfigRevision": "1", "sourceDispatchRevision": 1,
			"upstreamModel": account.Model, "protocol": account.Protocol,
			"providerProtocolProfileId": account.Profile, "sourceEndpointFamily": chatCodexEndpointFamily,
			"upstreamProtocol": account.Protocol, "upstreamEndpointFamily": chatCodexEndpointFamily,
			"upstreamAdapter": "openai-responses", "endpointFingerprint": "mockdata-endpoint",
		}
	}
	return string(chatCodexMustJSON(snapshot))
}

// chatCodexPolicySnapshot 复刻 policy_snapshot 形状；题库绑定与运行样本的
// customQuestionIds 一致（页面据此展示本次实际引用的题目）。
func chatCodexPolicySnapshot(resources chatCodexResources) string {
	snapshot := map[string]any{
		"revision": "1", "threshold": 70, "action": "fallback", "recoveryIntervalMinutes": 10,
		"manualEnforcementEnabled": true, "ownPhysicalAccount": true, "manualEnforcementEligible": true,
	}
	if ids := chatCodexQuestionIDs(resources); len(ids) > 0 {
		snapshot["customQuestionIds"] = ids
	}
	return string(chatCodexMustJSON(snapshot))
}

// chatCodexQuizQuestions 取本次运行绑定的已审核题目（上限 chatCodexQuizQuestionLimit）。
//
// 为什么要有统一入口：检查项（custom_quiz:<id>）、resultSummary.customQuiz、
// policy_snapshot.customQuestionIds 与调度 payload 必须引用同一批题目，否则页面
// 上的题库小节与运行快照会互相矛盾。
func chatCodexQuizQuestions(resources chatCodexResources) []chatCodexQuestion {
	questions := make([]chatCodexQuestion, 0, chatCodexQuizQuestionLimit)
	for _, question := range resources.questions {
		if len(questions) == chatCodexQuizQuestionLimit {
			break
		}
		questions = append(questions, question)
	}
	return questions
}

// chatCodexQuestionIDs 返回同一批题目的 id（供 policy_snapshot / 调度 payload 使用）。
func chatCodexQuestionIDs(resources chatCodexResources) []string {
	questions := chatCodexQuizQuestions(resources)
	ids := make([]string, 0, len(questions))
	for _, question := range questions {
		ids = append(ids, question.ID)
	}
	return ids
}

// chatCodexResultSummary 复刻 result_summary：evaluations 与检查项一一对应，
// 深度完成样本额外携带 trustReport（详情抽屉的受控样本 / 独立来源 / 配对窗口
// 读数都来自这里）。
func chatCodexResultSummary(seed chatCodexRunSeed, resources chatCodexResources, now time.Time) string {
	if seed.status == "running" {
		return "{}"
	}
	evaluations := make([]map[string]any, 0, len(seed.items))
	for _, item := range seed.items {
		evaluations = append(evaluations, map[string]any{
			"kind": item.typ, "status": item.status, "score": item.score, "maxScore": item.maxScore,
			"evidence": item.evidence,
		})
	}
	summary := map[string]any{
		"evaluations": evaluations, "score": seed.score, "maxScore": 100, "level": seed.level,
		"modelCheckUnverified": seed.status != "completed",
	}
	if seed.status == "failed" {
		summary["message"] = seed.errorMsg
	}
	if seed.profile == "full" && seed.status == "completed" {
		summary["trustReport"] = chatCodexTrustReport(seed, resources, now)
		if quiz, ok := chatCodexQuizSummary(resources); ok {
			summary["customQuiz"] = quiz
		}
	}
	return string(chatCodexMustJSON(summary))
}

// chatCodexTrustReport 构造深度样本的可信报告。
//
// 计数与 observation 一一对应（observationCount = 本运行 observation 行数、
// roundCount = 不同 round_index 数、independentSourceCount = 不同上游桶数、
// identityObservationCount = 身份家族行数、pairedProbeCount = 配对家族行数），
// 基线读数来自同一 cohort 的 model_token_intercept_baseline_versions 行。
func chatCodexTrustReport(seed chatCodexRunSeed, resources chatCodexResources, now time.Time) map[string]any {
	account, _ := resources.accountAt(seed.accountIdx)
	observedAt := now.Add(-time.Duration(seed.ageHours)*time.Hour - 2*time.Minute)
	return map[string]any{
		"identityStatus": "consistent", "mappingStatus": "direct", "usageIntegrityStatus": "insufficient_evidence",
		"protocolStatus": "consistent", "evidenceStatus": "stable",
		"requestedModel": account.Model, "mappedUpstreamModel": account.Model, "observedModel": account.Model,
		"mappingApplied": false, "probeSetVersion": chatCodexFullProbeSet, "evidenceCoverage": 92,
		"reasonCodes": []string{}, "trustScore": 0.87, "trustFormed": true, "evidenceFormed": true,
		"observationCount": 3, "roundCount": 3, "independentSourceCount": 2,
		"identityObservationCount": 1, "pairedProbeCount": 2,
		"slope": 1.001, "intercept": 50.0, "interceptBaselineMedian": 50.0, "interceptBaselineMad": 2.5,
		"interceptBaselineVersion": 1, "interceptBaselineStatus": "active", "interceptStrongGateEnabled": true,
		"identityDistance": 0.42, "pairedDistance": 0.19, "pairedBaselineMedian": 0.18, "pairedBaselineMad": 0.03,
		"baselineVersion": 1, "baselineVersionStatus": "active",
		"featureVersion": chatCodexFeatureVersion, "tokenizerVersion": chatCodexTokenizerVersion,
		"lastObservedAt": chatCodexStamp(observedAt),
	}
}

// chatCodexQuizSummary 复刻 resultSummary.customQuiz（题库池 31 分，扣分来自
// 失败项）；没有已审核题目时返回 false，运行照常落库但页面不展示题库环节。
func chatCodexQuizSummary(resources chatCodexResources) (map[string]any, bool) {
	questions := chatCodexQuizQuestions(resources)
	if len(questions) == 0 {
		return nil, false
	}
	items := make([]map[string]any, 0, len(questions))
	deduction := 0
	for index, question := range questions {
		verdict, reason := "passed", CleanupNamePrefix+"回答命中关键点"
		if index == len(questions)-1 && len(questions) > 1 {
			verdict, reason = "failed", CleanupNamePrefix+"缺少关键点，按题扣分"
			deduction += 8
		}
		items = append(items, map[string]any{
			"questionId": question.ID, "title": question.Title, "verdict": verdict, "reason": reason,
		})
	}
	score := 31 - deduction
	if score < 0 {
		score = 0
	}
	return map[string]any{"enabled": true, "score": score, "maxScore": 31, "deduction": deduction, "items": items}, true
}

// chatCodexQualityDecision 复刻 quality_decision 形状（判定与运行状态一致）。
func chatCodexQualityDecision(seed chatCodexRunSeed, resultSummary string) string {
	if seed.status == "running" {
		return "{}"
	}
	var summary map[string]any
	_ = json.Unmarshal([]byte(resultSummary), &summary)
	decision := map[string]any{
		"evidenceFormed": seed.status == "completed", "trustFormed": seed.status == "completed",
		"missingFamilies": []string{}, "partialFamilies": []string{}, "invalidFamilies": []string{},
		"manualEnforcementEnabled": true, "ownPhysicalAccount": true,
		"hardQualityFailure": false, "enforcementAllowed": false,
		"modelCheckUnverified": seed.status != "completed",
	}
	if trust, ok := summary["trustReport"]; ok {
		decision["trustReport"] = trust
	}
	if seed.status != "completed" {
		decision["result"] = "not_triggered"
	}
	return string(chatCodexMustJSON(decision))
}

// writeModelCheckItems 写检查项：已完成 / 失败 / 取消的运行才有项，运行中没有
// （与运行时 append 时机一致）。
func writeModelCheckItems(w *chatCodexWriter, resources chatCodexResources) error {
	now := w.now
	for _, seed := range chatCodexRunMatrix {
		runID := chatCodexRunID(seed.key)
		items := append([]chatCodexItemSeed(nil), seed.items...)
		if seed.quiz {
			if quizItems, ok := chatCodexQuizItems(resources); ok {
				items = append(items, quizItems...)
			} else {
				chatCodexSkip(w, "题库没有 status='approved' 题目，模型检测题库环节跳过", StoreModelCheck, "model_check_items")
			}
		}
		for index, item := range items {
			createdAt := now.Add(-time.Duration(seed.ageHours)*time.Hour - 3*time.Minute)
			columns := map[string]any{
				"id": chatCodexItemID(seed.key, index), "run_id": runID, "item_key": item.key,
				"item_type": item.typ, "status": item.status, "score": item.score, "max_score": item.maxScore,
				"duration_ms": item.duration, "trace_id": chatCodexNullable(CleanupTracePrefix + "model-check-" + seed.key),
				"evidence_summary_json": string(chatCodexMustJSON(item.evidence)),
				"error_code":            nil, "error_message": nil,
				"created_at": chatCodexStamp(createdAt), "updated_at": chatCodexStamp(createdAt),
			}
			if item.errorCode != "" {
				columns["error_code"] = item.errorCode
				columns["error_message"] = item.errorMsg
			}
			if err := w.put(StoreModelCheck, "model_check_items", columns); err != nil {
				return err
			}
		}
	}
	return nil
}

// chatCodexQuizItems 把已审核题目变成 custom_quiz 检查项（item_key 用
// custom_quiz:<questionId>，与运行时的 itemKey 契约一致）。
func chatCodexQuizItems(resources chatCodexResources) ([]chatCodexItemSeed, bool) {
	questions := chatCodexQuizQuestions(resources)
	if len(questions) == 0 {
		return nil, false
	}
	items := make([]chatCodexItemSeed, 0, len(questions))
	for index, question := range questions {
		verdict := "pass"
		status := "passed"
		score := 31
		if index == len(questions)-1 && len(questions) > 1 {
			verdict, status, score = "fail", "failed", 23
		}
		items = append(items, chatCodexItemSeed{
			key: "custom_quiz:" + question.ID, typ: "custom_quiz", status: status, score: score,
			maxScore: 31, duration: 2400,
			evidence: map[string]any{
				"questionId": question.ID, "questionTitle": question.Title, "verdict": verdict,
				"reason": CleanupNamePrefix + "题库判定样本",
			},
		})
	}
	return items, true
}

// writeModelCheckObservations 写受控 observation：只绑定深度样本；已完成样本的
// observation 标记为已聚合，运行中样本留空 aggregation_completed_at（与脏账户行
// 自洽）。
func writeModelCheckObservations(w *chatCodexWriter, resources chatCodexResources) error {
	processedAt := chatCodexStamp(w.now.Add(-time.Hour))
	for _, seed := range chatCodexRunMatrix {
		if len(seed.observations) == 0 {
			continue
		}
		account, ok := resources.accountAt(seed.accountIdx)
		if !ok {
			continue
		}
		runID := chatCodexRunID(seed.key)
		for index, observation := range seed.observations {
			createdAt := chatCodexObservationCreatedAt(w.now, seed)
			aggregatedAt := any(nil)
			if observation.complete {
				aggregatedAt = processedAt
			}
			columns := map[string]any{
				"id": chatCodexObservationID(runID, index), "run_id": runID,
				"system_account_id": account.Owner, "account_id": account.ID,
				"provider_code": account.Provider, "provider_protocol_profile_id": account.Profile,
				"endpoint_family": chatCodexEndpointFamily, "requested_model": account.Model,
				"mapped_upstream_model": account.Model, "observed_model": chatCodexNullable(account.Model),
				"mapping_applied": 0, "upstream_bucket_hmac": observation.bucket,
				"cohort_key_hmac": observation.cohort, "population_key_hmac": observation.population,
				"probe_key_hmac": observation.probe, "system_fingerprint_hmac": chatCodexNullable(observation.fingerprint),
				"probe_family": observation.family, "probe_set_version": chatCodexFullProbeSet,
				"tokenizer_version": chatCodexTokenizerVersion, "feature_version": chatCodexFeatureVersion,
				"round_index": observation.roundIndex, "padding_tokens": observation.paddingTokens,
				"local_input_tokens": observation.localTokens, "reported_input_tokens": chatCodexIntValue(observation.reportedTok),
				"cached_input_tokens": chatCodexIntValue(observation.cachedTok), "constraint_passed": 1,
				"observation_status": observation.status, "identity_status": "consistent",
				"mapping_status": "direct", "protocol_status": "consistent", "evidence_coverage": 92,
				"trace_id":   CleanupTracePrefix + "model-check-" + seed.key,
				"created_at": chatCodexStamp(createdAt), "aggregation_completed_at": aggregatedAt,
			}
			for featureIndex, value := range observation.features {
				columns[fmt.Sprintf("feature_%d", featureIndex+1)] = value
			}
			if err := w.put(StoreModelCheck, "model_check_observations", columns); err != nil {
				return err
			}
		}
	}
	return nil
}

func chatCodexIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// writeModelCheckTrust 写可信度投影：已完成深度样本的 observation 有回执、
// 账户最新结果与聚合游标；运行中深度样本的 observation 对应脏账户行（尚未
// 聚合）。Token 拦截基线按 cohort 写一版已生效的基线，供详情页展示基线读数。
func writeModelCheckTrust(w *chatCodexWriter, resources chatCodexResources, trustedRunID string, now time.Time) error {
	trustedAccount, ok := resources.accountAt(0)
	if !ok {
		return nil
	}
	processedAt := chatCodexStamp(now.Add(-time.Hour))
	// observation 行与可信度计数必须指同一批行：这里按写 observation 时的同一
	// 规则（运行 id + 家族序号）推导 id 与创建时刻。
	observations := make([]string, 0, len(chatCodexObservationSamples))
	for index := range chatCodexObservationSamples {
		observations = append(observations, chatCodexObservationID(trustedRunID, index))
	}
	lastID := observations[len(observations)-1]
	lastCreatedAt := chatCodexStamp(chatCodexObservationCreatedAt(now, chatCodexRunMatrix[4]))
	for _, observationID := range observations {
		if err := w.put(StoreModelCheck, "model_trust_observation_receipts", map[string]any{
			"observation_id": observationID, "observation_created_at": lastCreatedAt, "processed_at": processedAt,
		}); err != nil {
			return err
		}
	}
	cohort := chatCodexObservationSamples[0].cohort
	baselineDigest := chatCodexDigest([]byte(cohort + ":" + trustedAccount.Model))
	baselineNote := CleanupNamePrefix + "模型检测 Token 拦截基线（造数样本）"
	if err := w.put(StoreModelCheck, "model_token_intercept_baseline_versions", map[string]any{
		"cohort_key_hmac": "hmac-sha256-v1:" + baselineDigest, "requested_model": trustedAccount.Model,
		"tokenizer_version": chatCodexTokenizerVersion, "probe_set_version": chatCodexFullProbeSet,
		"baseline_version": 1, "version_status": "active", "evidence_status": "stable",
		"independent_source_count": 2, "retained_source_count": 2, "excluded_source_count": 0,
		"median_intercept": 50.0, "mad_intercept": 2.5, "q10_intercept": 48.0, "q90_intercept": 52.0,
		"strong_threshold_intercept": 96.0, "strong_gate_enabled": 1, "calibration_note": baselineNote,
		"first_observed_at": chatCodexStamp(now.Add(-72 * time.Hour)), "last_observed_at": lastCreatedAt,
		"updated_at": processedAt,
	}); err != nil {
		return err
	}
	// 账户最新可信结果：observation_count / round_count / independent_source_count /
	// identity_observation_count / paired_probe_count 全部由上面 3 条 observation 推导。
	if err := w.put(StoreModelCheck, "model_account_trust_results", map[string]any{
		"system_account_id": trustedAccount.Owner, "account_id": trustedAccount.ID,
		"requested_model": trustedAccount.Model, "identity_status": "consistent",
		"mapping_status": "direct", "usage_integrity_status": "insufficient_evidence",
		"protocol_status": "consistent", "evidence_status": "stable", "evidence_coverage": 92,
		"observation_count": 3, "round_count": 3, "independent_source_count": 2,
		"identity_observation_count": 1, "paired_probe_count": 2,
		"slope": 1.001, "intercept": 50.0, "intercept_baseline_median": 50.0, "intercept_baseline_mad": 2.5,
		"intercept_baseline_version": 1, "intercept_baseline_status": "active", "intercept_strong_gate_enabled": 1,
		"identity_distance": 0.42, "paired_distance": 0.19, "paired_baseline_median": 0.18,
		"paired_baseline_mad": 0.03, "baseline_version": 1, "baseline_version_status": "active",
		"feature_version": chatCodexFeatureVersion, "tokenizer_version": chatCodexTokenizerVersion,
		"probe_set_version": chatCodexFullProbeSet, "reason_codes_json": "[]",
		"last_observed_id": lastID, "last_observed_at": lastCreatedAt, "updated_at": processedAt,
	}); err != nil {
		return err
	}
	// 聚合游标：scope_key 用运行时唯一作用域，游标指向最后一条已聚合 observation。
	if err := w.put(StoreModelCheck, "model_trust_aggregation_state", map[string]any{
		"scope_key": chatCodexTrustScopeKey, "cursor_created_at": lastCreatedAt, "cursor_id": lastID,
		"last_success_at": processedAt, "last_error_message": nil, "lag_seconds": 3600,
		"updated_at": processedAt,
	}); err != nil {
		return err
	}
	// 脏账户行：运行中深度样本已 append observation 但尚未聚合，与运行时的
	// 「最新脏账户」队列语义一致（聚合成功后会删除该行）。
	runningAccount, ok := resources.accountAt(chatCodexRunSeedByKey("full_running").accountIdx)
	if ok {
		if err := w.put(StoreModelCheck, "model_trust_latest_dirty_accounts", map[string]any{
			"system_account_id": runningAccount.Owner, "account_id": runningAccount.ID,
			"requested_model": runningAccount.Model,
			"dirty_reason":    "model_check_observation_pending_aggregation",
			"updated_at":      chatCodexStamp(now.Add(-2 * time.Minute)),
		}); err != nil {
			return err
		}
	}
	return nil
}

// chatCodexRunSeedByKey 按 key 取矩阵行（key 是样本的稳定标识）。
func chatCodexRunSeedByKey(key string) chatCodexRunSeed {
	for _, seed := range chatCodexRunMatrix {
		if seed.key == key {
			return seed
		}
	}
	return chatCodexRunSeed{}
}

// chatCodexObservationCreatedAt 与写 observation 行时用同一公式，保证回执的
// observation_created_at 与 observation 行完全一致。
func chatCodexObservationCreatedAt(now time.Time, seed chatCodexRunSeed) time.Time {
	return now.Add(-time.Duration(seed.ageHours)*time.Hour - 2*time.Minute)
}

// writeModelCheckSchedulerTasks 写调度任务历史。
//
// 只写终态（completed）任务：pending / failed 且已到期的行会被运行时调度器
// 认领并真的发起上游探测，造数不能替用户按下这个开关。
func writeModelCheckSchedulerTasks(w *chatCodexWriter, resources chatCodexResources, now time.Time) error {
	account, ok := resources.accountAt(0)
	if !ok {
		return nil
	}
	dueAt := now.Add(-8 * time.Hour)
	completedAt := now.Add(-8*time.Hour + 3*time.Minute)
	schedulePayload := map[string]any{
		"systemAccountId": account.Owner, "actorSystemAccountId": account.Owner,
		"targetType": "account", "targetId": account.ID, "model": account.Model, "profile": "quick",
		"providerCode": account.Provider, "threshold": 70, "penaltyAction": "fallback",
		"configRevision": "1", "dispatchRevision": 1, "sourceConfigRevision": "1", "sourceDispatchRevision": 1,
		"policyRevision": "1", "probeSetVersion": chatCodexQuickProbeSet,
		"identityKey": account.Owner + ":" + account.ID + ":" + account.Model,
		"scheduleId":  CleanupIDPrefix + "model_quality_schedule", "ownerId": chatCodexTrustOwnerID,
		"scheduleRevision": 1, "intervalMinutes": 60, "recoveryIntervalMinutes": 10,
	}
	if ids := chatCodexQuestionIDs(resources); len(ids) > 0 {
		schedulePayload["customQuestionIds"] = ids
	}
	if err := w.put(StoreModelCheck, "model_check_scheduler_tasks", map[string]any{
		"id": CleanupIDPrefix + "model_check_scheduler_scheduled", "kind": "scheduled",
		"due_at": chatCodexStamp(dueAt), "claim_owner": nil, "claim_until": nil, "fence_token": 1,
		"state": "completed", "last_error": nil, "completed_at": chatCodexStamp(completedAt),
		"payload": []byte(chatCodexMustJSON(schedulePayload)), "updated_at": chatCodexStamp(completedAt),
	}); err != nil {
		return err
	}
	recoveryPayload := map[string]any{
		"systemAccountId": account.Owner, "actorSystemAccountId": account.Owner, "targetType": "account",
		"targetId": account.ID, "model": account.Model, "profile": "full", "providerCode": account.Provider,
		"threshold": 70, "penaltyAction": "quality_isolate", "configRevision": "1", "dispatchRevision": 1,
		"sourceConfigRevision": "1", "sourceDispatchRevision": 1, "policyRevision": "1",
		"probeSetVersion": chatCodexFullProbeSet,
		"identityKey":     account.Owner + ":" + account.ID + ":" + account.Model + ":full:actor:" + account.Owner,
		"scheduleId":      CleanupIDPrefix + "model_quality_schedule", "ownerId": chatCodexTrustOwnerID,
		"enforcementId": CleanupIDPrefix + "model_quality_enforcement", "generation": 1,
		"recoveryIntervalMinutes": 10,
	}
	if err := w.put(StoreModelCheck, "model_check_scheduler_tasks", map[string]any{
		"id": CleanupIDPrefix + "model_check_scheduler_recovery", "kind": "quality_recovery",
		"due_at": chatCodexStamp(dueAt), "claim_owner": nil, "claim_until": nil, "fence_token": 1,
		"state": "completed", "last_error": nil, "completed_at": chatCodexStamp(completedAt),
		"payload": []byte(chatCodexMustJSON(recoveryPayload)), "updated_at": chatCodexStamp(completedAt),
	}); err != nil {
		return err
	}
	return nil
}

// writeAccountQualityHealthHourly 为每个终态运行写一条小时健康（PK 是
// (account_id, stat_hour)，样本按 ageHours 拉开因此不会撞键）。
func writeAccountQualityHealthHourly(w *chatCodexWriter, resources chatCodexResources) error {
	for _, seed := range chatCodexRunMatrix {
		switch seed.status {
		case "running":
			continue
		}
		account, ok := resources.accountAt(seed.accountIdx)
		if !ok {
			continue
		}
		observedAt := w.now.Add(-time.Duration(seed.ageHours) * time.Hour)
		columns := map[string]any{
			"account_id": account.ID, "system_account_id": account.Owner, "provider_code": account.Provider,
			"stat_hour": observedAt.UTC().Format("2006-01-02T15"), "observed_at": chatCodexStamp(observedAt),
			"model_check_run_id": chatCodexRunID(seed.key), "model": account.Model, "profile": seed.profile,
			"score": seed.score, "threshold": 70, "level": seed.level,
			"error_code": nil, "error_message": nil, "updated_at": chatCodexStamp(observedAt),
		}
		if seed.errorCode != "" {
			columns["error_code"] = seed.errorCode
			columns["error_message"] = seed.errorMsg
		}
		if err := w.put(StoreModelCheck, "account_quality_health_hourly", columns); err != nil {
			return err
		}
	}
	return nil
}

// writeModelCheckDurableInputs 写深度已完成样本的持久化输入闭环：
// input_versions / inputs / execution_claims / outcomes。
//
// input_digest 复刻 gateway digestInput 的算法（字段名与顺序、payload 规范化、
// 时间 RFC3339Nano），否则运行时 LoadInput 会把它判成 tampered。认领行的
// claim_until 是「已释放」的过去时刻，不代表活跃持有者；owner_id 用造数自己的
// 标记值，运行时不会认领它。
func writeModelCheckDurableInputs(w *chatCodexWriter, resources chatCodexResources, trustedRunID string) error {
	seed := chatCodexRunSeedByKey("full_completed")
	account, ok := resources.accountAt(seed.accountIdx)
	if !ok {
		return nil
	}
	startedAt := w.now.Add(-time.Duration(seed.ageHours)*time.Hour - 4*time.Minute)
	finishedAt := startedAt.Add(3 * time.Minute)
	inputID := CleanupIDPrefix + "model_check_input_1"
	identityKey := account.Owner + ":" + account.ID + ":" + account.Model + ":full:actor:" + account.Owner
	payload := []byte(chatCodexRequestSummary(seed, account, chatCodexFullProbeSet))
	digest, err := chatCodexInputDigest(inputID, identityKey, account.ID, "1", "1", "manual", startedAt, startedAt.Add(time.Minute), payload)
	if err != nil {
		return err
	}
	if err := w.put(StoreModelCheck, "model_check_input_versions", map[string]any{
		"identity_key": identityKey, "next_version": 2, "updated_at": chatCodexStamp(startedAt),
	}); err != nil {
		return err
	}
	if err := w.put(StoreModelCheck, "model_check_inputs", map[string]any{
		"input_id": inputID, "identity_key": identityKey, "input_version": 1, "input_digest": digest,
		"target_id": account.ID, "config_revision": "1", "policy_revision": "1", "trigger": "manual",
		"issued_at": chatCodexStamp(startedAt), "expires_at": chatCodexStamp(startedAt.Add(time.Minute)),
		"payload": payload,
	}); err != nil {
		return err
	}
	outcomeID := CleanupIDPrefix + "model_check_outcome_1"
	if err := w.put(StoreModelCheck, "model_check_execution_claims", map[string]any{
		"input_id": inputID, "claim_token": CleanupIDPrefix + "model_check_claim_1", "outcome_id": outcomeID,
		"owner_id": chatCodexTrustOwnerID, "fence_token": 1,
		"claim_until": chatCodexStamp(finishedAt), "updated_at": chatCodexStamp(finishedAt),
	}); err != nil {
		return err
	}
	outcomePayload := []byte(chatCodexResultSummary(seed, resources, w.now))
	if err := w.put(StoreModelCheck, "model_check_outcomes", map[string]any{
		"outcome_id": outcomeID, "input_id": inputID, "input_digest": digest, "fence_token": 1,
		"observed_at": chatCodexStamp(finishedAt), "stored_at": chatCodexStamp(finishedAt),
		"payload": outcomePayload, "payload_digest": chatCodexDigest(outcomePayload), "committed": 1,
	}); err != nil {
		return err
	}
	return nil
}

// chatCodexInputDigest 复刻 gateway 的 digestInput：把不可变快照（字段名按
// Go 结构体名，JSON 键顺序即字段声明顺序）序列化后取 sha256。
func chatCodexInputDigest(inputID, identityKey, targetID, configRevision, policyRevision, trigger string, issuedAt, expiresAt time.Time, payload []byte) (string, error) {
	var canonical any
	if err := json.Unmarshal(payload, &canonical); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	immutable := struct {
		InputID, IdentityKey, TargetID, ConfigRevision, PolicyRevision, Trigger string
		IssuedAt, ExpiresAt                                                     string
		Payload                                                                 json.RawMessage
	}{
		InputID: inputID, IdentityKey: identityKey, TargetID: targetID,
		ConfigRevision: configRevision, PolicyRevision: policyRevision, Trigger: trigger,
		IssuedAt: issuedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
		Payload: encoded,
	}
	raw, err := json.Marshal(immutable)
	if err != nil {
		return "", err
	}
	return chatCodexDigest(raw), nil
}
