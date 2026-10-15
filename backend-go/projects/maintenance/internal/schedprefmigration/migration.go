// Package schedprefmigration 实现「调度偏好独立实体与 API Key 绑定」
// （PLAN-20261012T033000000Z）P2 一次性迁移（计划 D7 与 P2 设计门五项裁决）：
//
//   - 预检（Preflight，只读）：owner/策略/Key 计数、五种模式（以 gateway
//     ModeSupportsSchedulingPreference 同口径：五种模式均承载调度配置）各自的
//     有效调度参数、显式绑定/NULL 普通/chat Key 计数、坏配置清单（JSON 损坏/
//     非法值/未知模式）与速度优先同 owner 重名冲突预测；坏数据阻断迁移，不自
//     动成本化；
//   - 执行（Run）：三确认门禁在 CLI 层；库内协议=扫描规划（只读，config_json
//     只读作回滚 oracle）→ 条件领取 running 记录（单执行者）→ 按 owner 分批
//     短事务写入（BUG-0308 纪律：批间只读扫描完整读取后才开写事务；每 owner
//     一个短事务）→ 后检与完成记录同事务提交。速度优先且带有效配置的策略生成
//     等值偏好（name={策略名}·速度优先，owner 内重名自动加序号 -2/-3；配置为
//     规范化 JSON，重跑按确定性 ID schpref-mig-<策略ID> 复用并做规范化等值比
//     较，不比键序）；cost_first/缺省策略不建偏好（NULL 普通 Key 回填内置
//     ID）；仅更新 scheduling_preference_id IS NULL AND purpose='general' 的
//     Key，显式绑定与 chat Key 一律不动；存量 legacy studio Key（2026-10-14
//     退场裁决：purpose 词表收窄为 general|chat，存量行保留至显式清理命令
//     --apply-studio-retirement 执行）按 B1 排除臂处理：绑定列恒 NULL 不回
//     填、单独计数 apiKeysStudioExcluded；只
//     写 api_keys.scheduling_preference_id 一列，不触碰 updated_at/revision
//     与 route_strategies.config_json（BUG-0308 同源纪律）；速度运行态不转
//     移（裁决①，瞬态自愈）。
//   - 后检（Run 内）：全库无 NULL 普通 Key 残留、绑定引用完整、计数核对；残
//     留非零 → 拒绝写完成记录、报告不就绪；后检与 running→completed 条件更新
//     处于同一事务（capabilitydomain 先例）。
//   - 核验（Verify，只读）：完成记录存在且 completed、无 NULL 残留、引用完整。
//
// 断点恢复：running 阶段重跑即续跑（NULL 判定天然幂等，确定性偏好 ID 天然复
// 用）；completed 重复执行只报告完成；记录损坏或 ruleVersion 不符拒绝执行。
// 记录键/形状/启动门禁语义单源于共享包 schedprefdomain，本包不复制。

package schedprefmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
)

// DefaultBatchSize 是分批只读扫描的默认批大小。
const DefaultBatchSize = 500

// builtinID 是内置行固定 id（契约固定值，与 schema.BuiltinSchedulingPreferenceID
// 同源；本包复制常量避免 schema ↔ 迁移包反向依赖，内置行本身由 --ensure-schema
// / --seed 链建立）。
const builtinID = "schpref-builtin-chat-cost-first"

// migrationIDPrefix 是迁移生成偏好的确定性 ID 前缀：稳定映射 = 确定性 ID 派生
// 自策略 ID（计划 D7「速度优先偏好建立稳定迁移映射」），重跑据此复用既有行，
// 不产生重复等值偏好。
const migrationIDPrefix = "schpref-mig-"

// nameSuffixFormat 是重名自动加序号的后缀格式（{策略名}·速度优先-2/-3）。
const nameSuffixFormat = "-%d"

// nameSuffixProbeUpper 是重名加序号的探测上限（防御性：同名耗尽视为数据异常，
// fail-closed 而不是死循环）。
const nameSuffixProbeUpper = 1000

// speedRuntimeNote 是迁移/预检报告对速度运行态处置的固定说明（裁决①：不转
// 移，停写窗口后瞬态自然过期，明示行为变化）。
const speedRuntimeNote = "速度运行态不迁移（P2 设计门裁决①）：降级 TTL 与慢样本均为瞬态，停写窗口后自然过期；迁移后被降级账户恢复参与派发、慢样本重新积累，属明示行为变化，不宣称零变化"

// Options 是迁移/预检/核验的注入参数。
type Options struct {
	Dialect   capabilitydomain.Dialect
	Now       time.Time
	BatchSize int // <=0 取 DefaultBatchSize
}

// Failure 是报告的失败条目（坏配置、悬空策略、复用偏好不一致）。
type Failure struct {
	StrategyID           string `json:"strategyId,omitempty"`
	APIKeyID             string `json:"apiKeyId,omitempty"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId,omitempty"`
	Kind                 string `json:"kind"`
	Detail               string `json:"detail"`
}

// 失败分类词表。
const (
	FailureStrategyConfigInvalid = "strategy_config_invalid" // config_json 损坏 / 非法值 / 结构错误
	FailureStrategyModeInvalid   = "strategy_mode_invalid"   // mode 不在五种模式内
	FailureKeyStrategyMissing    = "key_route_strategy_missing"
	FailurePreferenceMismatch    = "preference_mismatch" // 确定性 ID 复用行与策略配置不等值
	FailureResidualNullKeys      = "residual_null_general_keys"
	FailureDanglingBinding       = "dangling_binding"
	FailureRecordMissing         = "migration_record_missing"
	FailureRecordNotCompleted    = "migration_record_not_completed"
)

// ModeReport 是一种策略模式的调度参数计数（预检 JSON）。
type ModeReport struct {
	Total             int `json:"total"`
	SpeedFirst        int `json:"speedFirst"`
	CostFirstExplicit int `json:"costFirstExplicit"`
	CostFirstDefault  int `json:"costFirstDefault"`
	BadConfig         int `json:"badConfig"`
}

// PlannedPreference 是预检报告里的等值偏好生成计划行。
type PlannedPreference struct {
	StrategyID           string          `json:"strategyId"`
	OwnerSystemAccountID string          `json:"ownerSystemAccountId"`
	PreferenceID         string          `json:"preferenceId"`
	Name                 string          `json:"name"`
	SpeedConfig          json.RawMessage `json:"speedConfig"`
	Reused               bool            `json:"reused"`
}

// NameConflictPrediction 是重名冲突预测行（finalName 与请求名不同时列出）。
type NameConflictPrediction struct {
	StrategyID           string `json:"strategyId"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId"`
	RequestedName        string `json:"requestedName"`
	FinalName            string `json:"finalName"`
}

// BadConfig 是坏配置清单行。
type BadConfig struct {
	StrategyID           string `json:"strategyId"`
	OwnerSystemAccountID string `json:"ownerSystemAccountId"`
	Name                 string `json:"name"`
	Kind                 string `json:"kind"`
	Detail               string `json:"detail"`
}

// PreflightReport 是预检命令的 JSON 报告（只读；Blocking>0 表示存在阻断迁移
// 的坏数据，预检本身仍以退出码 0 输出报告）。
type PreflightReport struct {
	Driver                  string                   `json:"driver"`
	RuleVersion             int                      `json:"ruleVersion"`
	Record                  *schedprefdomain.Record  `json:"record"`
	OwnersTotal             int                      `json:"ownersTotal"`
	StrategiesTotal         int                      `json:"strategiesTotal"`
	Modes                   map[string]ModeReport    `json:"modes"`
	PreferencesExisting     int                      `json:"preferencesExisting"`
	APIKeysTotal            int                      `json:"apiKeysTotal"`
	APIKeysGeneral          int                      `json:"apiKeysGeneral"`
	APIKeysChat             int                      `json:"apiKeysChat"`
	APIKeysStudioExcluded   int                      `json:"apiKeysStudioExcluded"`
	APIKeysExplicitBound    int                      `json:"apiKeysExplicitBound"`
	APIKeysNullGeneral      int                      `json:"apiKeysNullGeneral"`
	PlannedPreferences      []PlannedPreference      `json:"plannedPreferences"`
	PlannedBuiltinBindings  int                      `json:"plannedBuiltinBindings"`
	NameConflictPredictions []NameConflictPrediction `json:"nameConflictPredictions"`
	BadConfigs              []BadConfig              `json:"badConfigs"`
	Blocking                int                      `json:"blocking"`
	SpeedRuntimeNote        string                   `json:"speedRuntimeNote"`
}

// Report 是 migrate 命令的 JSON 报告。
type Report struct {
	Driver                string                  `json:"driver"`
	RuleVersion           int                     `json:"ruleVersion"`
	Record                *schedprefdomain.Record `json:"record"`
	AlreadyComplete       bool                    `json:"alreadyComplete"`
	OwnersTotal           int                     `json:"ownersTotal"`
	StrategiesTotal       int                     `json:"strategiesTotal"`
	StrategiesSpeedFirst  int                     `json:"strategiesSpeedFirst"`
	StrategiesCostFirst   int                     `json:"strategiesCostFirst"`
	PreferencesExisting   int                     `json:"preferencesExisting"`
	PreferencesCreated    int                     `json:"preferencesCreated"`
	PreferencesReused     int                     `json:"preferencesReused"`
	APIKeysTotal          int                     `json:"apiKeysTotal"`
	APIKeysNullGeneral    int                     `json:"apiKeysNullGeneral"`
	APIKeysBackfilled     int64                   `json:"apiKeysBackfilled"`
	APIKeysExplicitBound  int                     `json:"apiKeysExplicitBound"`
	APIKeysChat           int                     `json:"apiKeysChat"`
	APIKeysStudioExcluded int                     `json:"apiKeysStudioExcluded"`
	SpeedRuntimeNote      string                  `json:"speedRuntimeNote"`
	Failures              []Failure               `json:"failures"`
	CompletedStage        string                  `json:"completedStage,omitempty"`
	CompletedAt           string                  `json:"completedAt,omitempty"`
}

// Ready 报告后置条件：失败清单清零且完成记录已落库为 completed（或此前已完
// 成）。部分成功不满足上线门禁。
func (r Report) Ready() bool {
	return len(r.Failures) == 0 && r.CompletedStage == string(schedprefdomain.StageCompleted)
}

// VerifyReport 是 verify（postflight）命令的 JSON 报告（全程零写入）。
type VerifyReport struct {
	Driver                     string                  `json:"driver"`
	RuleVersion                int                     `json:"ruleVersion"`
	Record                     *schedprefdomain.Record `json:"record"`
	RecordPresent              bool                    `json:"recordPresent"`
	APIKeysTotal               int                     `json:"apiKeysTotal"`
	APIKeysNullGeneralResidual int                     `json:"apiKeysNullGeneralResidual"`
	DanglingBindingCount       int                     `json:"danglingBindingCount"`
	Failures                   []Failure               `json:"failures"`
}

// Ready 报告核验后置条件：完成记录存在且 completed、失败清单清零。
func (r VerifyReport) Ready() bool {
	return len(r.Failures) == 0 && r.Record != nil && r.Record.Stage == schedprefdomain.StageCompleted
}

// scannedStrategy 是策略扫描投影（config_json 只读，供回滚 oracle 保留）。
type scannedStrategy struct {
	id        string
	owner     string
	name      string
	mode      string
	status    string
	config    *NormalRoutingConfig // nil=缺省成本优先
	hasConfig bool                 // config_json 携带 normalRoutingConfig（区分显式 cost_first）
	bad       *BadConfig           // 非空=坏数据（阻断迁移）
}

// scannedKey 是 API Key 扫描投影。
type scannedKey struct {
	id         string
	owner      string
	strategyID string
	purpose    string
	bound      sql.NullString
}

// scannedPref 是既有自定义偏好投影（重名预测与复用判定输入）。
type scannedPref struct {
	id         string
	owner      string
	name       string
	lowerName  string
	chatMode   string
	configJSON sql.NullString
}

// scanResult 汇总一次只读扫描。
type scanResult struct {
	strategies []scannedStrategy
	keys       []scannedKey
	prefs      []scannedPref
	owners     map[string]bool
	failures   []Failure
	builtinOK  bool
}

// fiveModes 是 gateway ModeSupportsSchedulingPreference 的同口径词表（五种模
// 式均承载 normalRoutingConfig；本包按同一清单判定，未知模式计坏数据阻断）。
var fiveModes = [5]string{"normal", "weighted", "failover", "round_robin", "merge"}

// modeSupportsSchedulingPreference 与 gateway routestrategies 同口径。
func modeSupportsSchedulingPreference(mode string) bool {
	for _, candidate := range fiveModes {
		if mode == candidate {
			return true
		}
	}
	return false
}

// qualifiedTable 返回方言限定的业务表名（PG 走 juhe_business schema）。
func qualifiedTable(dialect capabilitydomain.Dialect, table string) string {
	if dialect == capabilitydomain.DialectPostgres {
		return `"juhe_business".` + table
	}
	return table
}

// scan 全量只读扫描（策略/Key/偏好/内置行四路；每路分批 keyset，批间完整读取
// 后再取下一批，全程不开写事务）。内置行缺失或结构不符记入 failures（阻断迁
// 移，先 --ensure-schema）。
func scan(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, batchSize int) (*scanResult, error) {
	result := &scanResult{owners: map[string]bool{}}
	if err := scanStrategies(ctx, db, dialect, batchSize, result); err != nil {
		return nil, err
	}
	if err := scanKeys(ctx, db, dialect, batchSize, result); err != nil {
		return nil, err
	}
	if err := scanPreferences(ctx, db, dialect, batchSize, result); err != nil {
		return nil, err
	}
	if err := scanBuiltin(ctx, db, dialect, result); err != nil {
		return nil, err
	}
	return result, nil
}

// scanStrategies 分批只读扫描全部策略并就地规范化配置。每批完整读取（rows.Err
// + Close）后再取下一批，全程不开写事务（BUG-0308）。
func scanStrategies(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, batchSize int, result *scanResult) error {
	query := `SELECT id, system_account_id, name, mode, status, config_json FROM route_strategies
		WHERE (? = '' OR id > ?) ORDER BY id LIMIT ?`
	if dialect == capabilitydomain.DialectPostgres {
		query = `SELECT id, system_account_id, name, mode, status, config_json FROM "juhe_business"."route_strategies"
			WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`
	}
	after := ""
	for {
		var rows *sql.Rows
		var err error
		if dialect == capabilitydomain.DialectPostgres {
			rows, err = db.QueryContext(ctx, query, after, batchSize)
		} else {
			rows, err = db.QueryContext(ctx, query, after, after, batchSize)
		}
		if err != nil {
			return fmt.Errorf("扫描 route_strategies 失败: %w", err)
		}
		batch := []scannedStrategy{}
		for rows.Next() {
			var (
				id, owner, name, mode, status string
				configJSON                    sql.NullString
			)
			if err := rows.Scan(&id, &owner, &name, &mode, &status, &configJSON); err != nil {
				rows.Close()
				return fmt.Errorf("读取 route_strategies 行失败: %w", err)
			}
			item := scannedStrategy{id: id, owner: owner, name: name, mode: mode, status: status}
			result.owners[owner] = true
			if !modeSupportsSchedulingPreference(mode) {
				item.bad = &BadConfig{StrategyID: id, OwnerSystemAccountID: owner, Name: name, Kind: FailureStrategyModeInvalid,
					Detail: "mode 不在 normal/weighted/failover/round_robin/merge 内: " + mode}
			} else {
				normal, normalizeErr := parseStrategyConfigJSON(configJSON)
				switch {
				case normalizeErr != nil:
					item.bad = &BadConfig{StrategyID: id, OwnerSystemAccountID: owner, Name: name, Kind: FailureStrategyConfigInvalid,
						Detail: normalizeErr.Error()}
				case normal != nil:
					item.config = normal
					item.hasConfig = true
				}
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("遍历 route_strategies 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if item.bad != nil {
				result.failures = append(result.failures, Failure{
					StrategyID: item.bad.StrategyID, OwnerSystemAccountID: item.bad.OwnerSystemAccountID,
					Kind: item.bad.Kind, Detail: item.bad.Detail,
				})
			}
			result.strategies = append(result.strategies, item)
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return errors.New("route_strategies 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return nil
}

// scanKeys 分批只读扫描全部 API Key（含 chat、legacy studio 与停用 Key：显
// 式绑定与 chat 一律不动，legacy studio 保持 B1 排除臂——恒 NULL、单独计数，
// 但须纳入计数与保护断言）。
func scanKeys(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, batchSize int, result *scanResult) error {
	query := `SELECT id, system_account_id, route_strategy_id, purpose, scheduling_preference_id FROM api_keys
		WHERE (? = '' OR id > ?) ORDER BY id LIMIT ?`
	if dialect == capabilitydomain.DialectPostgres {
		query = `SELECT id, system_account_id, route_strategy_id, purpose, scheduling_preference_id FROM "juhe_business"."api_keys"
			WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`
	}
	after := ""
	for {
		var rows *sql.Rows
		var err error
		if dialect == capabilitydomain.DialectPostgres {
			rows, err = db.QueryContext(ctx, query, after, batchSize)
		} else {
			rows, err = db.QueryContext(ctx, query, after, after, batchSize)
		}
		if err != nil {
			return fmt.Errorf("扫描 api_keys 失败: %w", err)
		}
		batch := []scannedKey{}
		for rows.Next() {
			var (
				id, owner, strategyID, purpose string
				bound                          sql.NullString
			)
			if err := rows.Scan(&id, &owner, &strategyID, &purpose, &bound); err != nil {
				rows.Close()
				return fmt.Errorf("读取 api_keys 行失败: %w", err)
			}
			batch = append(batch, scannedKey{id: id, owner: owner, strategyID: strategyID, purpose: purpose, bound: bound})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("遍历 api_keys 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		result.keys = append(result.keys, batch...)
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return errors.New("api_keys 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return nil
}

// scanPreferences 分批只读扫描既有自定义偏好行（built_in=0；重名预测与确定性
// ID 复用判定的输入）。
func scanPreferences(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, batchSize int, result *scanResult) error {
	query := `SELECT id, system_account_id, name, chat_mode, chat_speed_first_config_json FROM scheduling_preferences
		WHERE built_in = 0 AND (? = '' OR id > ?) ORDER BY id LIMIT ?`
	if dialect == capabilitydomain.DialectPostgres {
		query = `SELECT id, system_account_id, name, chat_mode, chat_speed_first_config_json FROM "juhe_business"."scheduling_preferences"
			WHERE built_in = 0 AND ($1 = '' OR id > $1) ORDER BY id LIMIT $2`
	}
	after := ""
	for {
		var rows *sql.Rows
		var err error
		if dialect == capabilitydomain.DialectPostgres {
			rows, err = db.QueryContext(ctx, query, after, batchSize)
		} else {
			rows, err = db.QueryContext(ctx, query, after, after, batchSize)
		}
		if err != nil {
			return fmt.Errorf("扫描 scheduling_preferences 失败: %w", err)
		}
		batch := []scannedPref{}
		for rows.Next() {
			var (
				id, owner, name, chatMode string
				configJSON                sql.NullString
			)
			if err := rows.Scan(&id, &owner, &name, &chatMode, &configJSON); err != nil {
				rows.Close()
				return fmt.Errorf("读取 scheduling_preferences 行失败: %w", err)
			}
			batch = append(batch, scannedPref{id: id, owner: owner, name: name, lowerName: strings.ToLower(name), chatMode: chatMode, configJSON: configJSON})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("遍历 scheduling_preferences 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		result.prefs = append(result.prefs, batch...)
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return errors.New("scheduling_preferences 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return nil
}

// scanBuiltin 校验内置行存在且合规（built_in=1、cost_first/speed_first）；
// 缺失或不符记入 failures——回填内置 ID 之前必须保证内置行可用，先
// --ensure-schema。enabled 已随 v2 用户裁决修订第 4 条退役（创建即启用），
// 内置行合规性不再读取该列。
func scanBuiltin(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, result *scanResult) error {
	query := `SELECT built_in, chat_mode FROM scheduling_preferences WHERE id = ?`
	if dialect == capabilitydomain.DialectPostgres {
		query = `SELECT built_in, chat_mode FROM "juhe_business"."scheduling_preferences" WHERE id = $1`
	}
	var builtIn int
	var chatMode string
	err := db.QueryRowContext(ctx, query, builtinID).Scan(&builtIn, &chatMode)
	if errors.Is(err, sql.ErrNoRows) {
		result.failures = append(result.failures, Failure{
			Kind:   "builtin_row_missing",
			Detail: fmt.Sprintf("内置调度偏好行（id=%s）缺失，先运行 juhe-ai-maintenance --ensure-schema（幂等建表/补列并补种内置行）", builtinID),
		})
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取内置调度偏好行失败: %w", err)
	}
	if builtIn != 1 || (chatMode != "cost_first" && chatMode != "speed_first") {
		result.failures = append(result.failures, Failure{
			Kind: "builtin_row_invalid",
			Detail: fmt.Sprintf("内置调度偏好行结构不符（built_in=%d chat_mode=%q，期望 built_in=1、cost_first/speed_first），先运行 juhe-ai-maintenance --ensure-schema 修复",
				builtIn, chatMode),
		})
		return nil
	}
	result.builtinOK = true
	return nil
}

// ownerStrategyPlans 是单 owner 的写入计划（一个 owner 一个短事务）。
type ownerStrategyPlans struct {
	preferences []plannedWrite // 待 INSERT 的等值偏好行（reuse 行不在内）
	keyUpdates  []keyUpdate    // 待回填的 NULL 普通 Key
}

type plannedWrite struct {
	id         string
	owner      string
	name       string
	configJSON string
}

type keyUpdate struct {
	id           string
	preferenceID string
}

// planInput 携带规划输入（策略/Key/既有偏好投影）。
type planInput struct {
	strategies []scannedStrategy
	keys       []scannedKey
	prefs      []scannedPref
}

// planOutput 汇总规划结果与报告计数。
type planOutput struct {
	perOwner                map[string]*ownerStrategyPlans
	ownerOrder              []string
	plannedPreferences      []PlannedPreference
	nameConflictPredictions []NameConflictPrediction
	builtinBindings         int
	nullGeneral             int
	studioExcluded          int // studio NULL Key 排除计数（与 chat 同规，不进回填）
	speedFirstStrategies    int
	costFirstStrategies     int
	reused                  int // 计划级复用（确定性 ID 命中既有行，不再 INSERT）
	failures                []Failure
}

// buildPlan 从扫描结果推导写入计划（纯内存，只读）。规则：
//   - speed_first 有效配置策略 → 生成等值偏好（确定性 ID；重名自动加序号；复
//     用行做规范化等值比较）；
//   - cost_first/缺省策略 → 不建偏好；
//   - NULL 普通 Key 按其策略回填（生成偏好 ID 或内置 ID）；策略行缺失 → 失败
//     条目（fail-closed，不猜目标）；
//   - 显式绑定与 chat / studio Key 一律不动（studio NULL Key 与 chat 同规：
//     2026-10-13 主代理裁决，绑定列恒 NULL 不回填，单独计数保证规划计数与
//     purpose='general' 回填 SQL / 后检残留判定一致，否则完成记录永拒）。
func buildPlan(input planInput) *planOutput {
	output := &planOutput{perOwner: map[string]*ownerStrategyPlans{}}
	byID := make(map[string]scannedStrategy, len(input.strategies))
	for _, item := range input.strategies {
		byID[item.id] = item
	}
	// 既有名称占用：owner → {name/lowerName 集合}（复用行自身名称同样占用，
	// 由确定性 ID 命中后豁免——见下）。
	takenNames := map[string]map[string]bool{}
	for _, pref := range input.prefs {
		if takenNames[pref.owner] == nil {
			takenNames[pref.owner] = map[string]bool{}
		}
		takenNames[pref.owner][pref.name] = true
		takenNames[pref.owner][pref.lowerName] = true
	}
	// 本轮已分配名称（同一 owner 内两次生成撞名时加序号）。
	assignedNames := map[string]map[string]bool{}
	takeName := func(owner, name string) {
		if assignedNames[owner] == nil {
			assignedNames[owner] = map[string]bool{}
		}
		assignedNames[owner][name] = true
		assignedNames[owner][strings.ToLower(name)] = true
	}

	ensurePlans := func(owner string) *ownerStrategyPlans {
		if output.perOwner[owner] == nil {
			output.perOwner[owner] = &ownerStrategyPlans{}
			output.ownerOrder = append(output.ownerOrder, owner)
		}
		return output.perOwner[owner]
	}

	// 1) 速度优先策略 → 等值偏好计划（含无 Key 策略：停用/无绑定策略也生成，
	// 避免之后启用丢失配置）。
	for _, strategy := range input.strategies {
		if strategy.bad != nil || strategy.config == nil || strategy.config.SchedulingPreference != "speed_first" {
			continue
		}
		output.speedFirstStrategies++
		preferenceID := migrationIDPrefix + strategy.id
		plans := ensurePlans(strategy.owner)
		if existing := findPref(input.prefs, preferenceID); existing != nil {
			// 复用路径：chat_mode 与规范化配置必须与策略等值（不比键序）；
			// 不一致属停写窗口被改写或数据损坏，fail-closed。
			stored, parseErr := parsePreferenceSpeedConfig(existing.configJSON)
			if parseErr != nil || existing.chatMode != "speed_first" || !equalNormalConfig(stored, strategy.config) {
				output.failures = append(output.failures, Failure{
					StrategyID: strategy.id, OwnerSystemAccountID: strategy.owner,
					Kind: FailurePreferenceMismatch,
					Detail: fmt.Sprintf("迁移映射偏好 %s 与策略 %s 的规范化配置不等值（%v），拒绝复用也拒绝覆盖",
						preferenceID, strategy.id, parseErr),
				})
				continue
			}
			output.plannedPreferences = append(output.plannedPreferences, PlannedPreference{
				StrategyID: strategy.id, OwnerSystemAccountID: strategy.owner,
				PreferenceID: preferenceID, Name: existing.name,
				SpeedConfig: marshalSpeedConfig(strategy.config), Reused: true,
			})
			output.reused++
			continue
		}
		baseName := strategy.name + "·速度优先"
		finalName := baseName
		conflicted := nameTaken(takenNames, assignedNames, strategy.owner, baseName)
		for suffix := 2; conflicted && suffix < nameSuffixProbeUpper+2; suffix++ {
			finalName = baseName + fmt.Sprintf(nameSuffixFormat, suffix)
			if !nameTaken(takenNames, assignedNames, strategy.owner, finalName) {
				conflicted = false
			}
		}
		if conflicted {
			output.failures = append(output.failures, Failure{
				StrategyID: strategy.id, OwnerSystemAccountID: strategy.owner,
				Kind:   FailurePreferenceMismatch,
				Detail: fmt.Sprintf("偏好名称 %q 在 owner %s 内重名加序号耗尽（探测上限 %d）", baseName, strategy.owner, nameSuffixProbeUpper),
			})
			continue
		}
		if finalName != baseName {
			output.nameConflictPredictions = append(output.nameConflictPredictions, NameConflictPrediction{
				StrategyID: strategy.id, OwnerSystemAccountID: strategy.owner,
				RequestedName: baseName, FinalName: finalName,
			})
		}
		configJSON := string(marshalSpeedConfig(strategy.config))
		plans.preferences = append(plans.preferences, plannedWrite{id: preferenceID, owner: strategy.owner, name: finalName, configJSON: configJSON})
		takeName(strategy.owner, finalName)
		output.plannedPreferences = append(output.plannedPreferences, PlannedPreference{
			StrategyID: strategy.id, OwnerSystemAccountID: strategy.owner,
			PreferenceID: preferenceID, Name: finalName,
			SpeedConfig: json.RawMessage(configJSON),
		})
	}

	// 2) NULL 普通 Key → 目标绑定（生成偏好 / 内置）。
	for _, key := range input.keys {
		switch key.purpose {
		case "chat":
			continue // chat Key 恒 NULL，一律不动
		case "studio":
			// studio Key 与 chat 同规（2026-10-13 主代理裁决）：媒体工作室
			// Key 绑定列恒 NULL、不建立绑定，不进 keyUpdates 也不进 nullGeneral；
			// 单独计数进预检/执行报告（B1 修复：原实现把 studio NULL Key 计入
			// 回填对象，与 purpose='general' 回填 SQL 计数不匹配，完成记录永拒）。
			output.studioExcluded++
			continue
		}
		if key.bound.Valid && strings.TrimSpace(key.bound.String) != "" {
			continue // 显式绑定一律不动
		}
		output.nullGeneral++
		strategy, ok := byID[key.strategyID]
		if !ok {
			output.failures = append(output.failures, Failure{
				APIKeyID: key.id, OwnerSystemAccountID: key.owner,
				Kind:   FailureKeyStrategyMissing,
				Detail: "route_strategy_id 指向的策略行不存在: " + key.strategyID + "（无法定位回填目标，拒绝猜测）",
			})
			continue
		}
		target := builtinID
		if strategy.config != nil && strategy.config.SchedulingPreference == "speed_first" && strategy.bad == nil {
			target = migrationIDPrefix + strategy.id
		}
		if target == builtinID {
			output.builtinBindings++
		}
		plans := ensurePlans(key.owner)
		plans.keyUpdates = append(plans.keyUpdates, keyUpdate{id: key.id, preferenceID: target})
	}

	// 3) 计数（cost_first = 显式 + 缺省；坏配置策略不计入有效参数）。
	for _, strategy := range input.strategies {
		if strategy.bad != nil {
			continue
		}
		if strategy.config != nil && strategy.config.SchedulingPreference == "cost_first" {
			output.costFirstStrategies++
		}
	}
	return output
}

// findPref 按 ID 查既有偏好行。
func findPref(prefs []scannedPref, id string) *scannedPref {
	for index := range prefs {
		if prefs[index].id == id {
			return &prefs[index]
		}
	}
	return nil
}

// nameTaken 判定 owner 内名称是否被既有行或本轮已分配名称占用（owner 内精确
// + lower 归一双唯一索引同口径）。
func nameTaken(takenNames, assignedNames map[string]map[string]bool, owner, name string) bool {
	lower := strings.ToLower(name)
	if names := takenNames[owner]; names != nil && (names[name] || names[lower]) {
		return true
	}
	if names := assignedNames[owner]; names != nil && (names[name] || names[lower]) {
		return true
	}
	return false
}

// marshalSpeedConfig 渲染偏好实体存储形状 JSON（D2：{firstByteDeadlineMs,
// speedFirstConfig}，struct 序列化键序稳定）。
func marshalSpeedConfig(normal *NormalRoutingConfig) json.RawMessage {
	document := map[string]any{}
	if normal.FirstByteDeadlineMs != nil {
		document["firstByteDeadlineMs"] = *normal.FirstByteDeadlineMs
	}
	if normal.SpeedFirstConfig != nil {
		document["speedFirstConfig"] = normal.SpeedFirstConfig
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		// NormalRoutingConfig 只含整数与字符串字段，序列化不可失败；失败即内
		// 部错误，panic 由迁移命令以运行失败暴露，不静默写坏数据。
		panic(fmt.Sprintf("schedprefmigration: 序列化速度配置失败: %v", err))
	}
	return encoded
}

// sortFailures 稳定排序失败清单，保证报告可回放。
func sortFailures(failures []Failure) {
	sort.Slice(failures, func(i, j int) bool {
		left, right := failures[i], failures[j]
		if left.StrategyID != right.StrategyID {
			return left.StrategyID < right.StrategyID
		}
		if left.APIKeyID != right.APIKeyID {
			return left.APIKeyID < right.APIKeyID
		}
		return left.Kind < right.Kind
	})
}

// readRecord 读取迁移记录（不存在返回 nil）。
func readRecord(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect) (*schedprefdomain.Record, error) {
	record, present, err := schedprefdomain.ReadRecord(ctx, db, dialect)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	return &record, nil
}

// Preflight 执行只读预检并输出结构化报告。全程零写入；坏配置只列入清单与
// Blocking 计数，不改变退出语义（退出语义由 CLI 层决定）。
func Preflight(ctx context.Context, db *sql.DB, options Options) (PreflightReport, error) {
	report := PreflightReport{
		Driver:                  options.Dialect.String(),
		RuleVersion:             schedprefdomain.RuleVersion,
		Modes:                   map[string]ModeReport{},
		PlannedPreferences:      []PlannedPreference{},
		NameConflictPredictions: []NameConflictPrediction{},
		BadConfigs:              []BadConfig{},
		SpeedRuntimeNote:        speedRuntimeNote,
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	record, err := readRecord(ctx, db, options.Dialect)
	if err != nil {
		return report, err
	}
	report.Record = record
	scanned, err := scan(ctx, db, options.Dialect, batchSize)
	if err != nil {
		return report, err
	}
	plan := buildPlan(planInput{strategies: scanned.strategies, keys: scanned.keys, prefs: scanned.prefs})
	collectPreflight(&report, scanned, plan)
	return report, nil
}

// collectPreflight 把扫描与规划结果折叠进预检报告。
func collectPreflight(report *PreflightReport, scanned *scanResult, plan *planOutput) {
	for _, mode := range fiveModes {
		report.Modes[mode] = ModeReport{}
	}
	for _, strategy := range scanned.strategies {
		entry := report.Modes[strategy.mode]
		if strategy.bad != nil {
			entry.Total++
			entry.BadConfig++
			report.Modes[strategy.mode] = entry
			report.BadConfigs = append(report.BadConfigs, *strategy.bad)
			continue
		}
		entry.Total++
		switch {
		case strategy.config != nil && strategy.config.SchedulingPreference == "speed_first":
			entry.SpeedFirst++
		case strategy.hasConfig:
			entry.CostFirstExplicit++
		default:
			entry.CostFirstDefault++
		}
		report.Modes[strategy.mode] = entry
	}
	seen := map[string]bool{}
	for _, strategy := range scanned.strategies {
		if seen[strategy.owner] {
			continue
		}
		seen[strategy.owner] = true
		report.OwnersTotal++
	}
	report.StrategiesTotal = len(scanned.strategies)
	report.PreferencesExisting = len(scanned.prefs)
	for _, key := range scanned.keys {
		report.APIKeysTotal++
		switch {
		case key.purpose == "chat":
			report.APIKeysChat++
		case key.purpose == "studio":
			// studio Key 与 chat 同规，单独计数；不再计入普通 Key 总数。
			report.APIKeysStudioExcluded++
		default:
			report.APIKeysGeneral++
		}
		if key.bound.Valid && strings.TrimSpace(key.bound.String) != "" {
			report.APIKeysExplicitBound++
		}
	}
	report.APIKeysNullGeneral = plan.nullGeneral
	report.PlannedPreferences = plan.plannedPreferences
	report.PlannedBuiltinBindings = plan.builtinBindings
	report.NameConflictPredictions = plan.nameConflictPredictions
	report.Blocking = len(report.BadConfigs) + len(plan.failures)
	// 计划期失败（复用不一致/悬空策略/加序号耗尽）以坏配置同型条目透出。
	for _, failure := range plan.failures {
		report.BadConfigs = append(report.BadConfigs, BadConfig{
			StrategyID: failure.StrategyID, OwnerSystemAccountID: failure.OwnerSystemAccountID,
			Name: failure.APIKeyID, Kind: failure.Kind, Detail: failure.Detail,
		})
	}
}

// Run 执行一次迁移命令。语义：
//  1. 读取迁移记录并判定（completed 只报告完成；损坏拒绝；running 续跑）；
//  2. 只读扫描与规划；失败清单非空 → 零写入返回不就绪报告（坏数据阻断）；
//  3. 条件领取 running 记录（首次 INSERT；并行竞争失败即放弃）；
//  4. 按 owner 分批短事务写入（等值偏好 INSERT ON CONFLICT(id) DO NOTHING +
//     NULL 普通 Key 回填；只写绑定列）；
//  5. 后检（NULL 残留/引用完整/计数核对）与 running→completed 条件更新同事务
//     提交；后检失败保留 running，不写完成记录。
func Run(ctx context.Context, db *sql.DB, options Options) (Report, error) {
	report := Report{
		Driver:           options.Dialect.String(),
		RuleVersion:      schedprefdomain.RuleVersion,
		SpeedRuntimeNote: speedRuntimeNote,
		Failures:         []Failure{},
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	record, err := readRecord(ctx, db, options.Dialect)
	if err != nil {
		return report, err
	}
	report.Record = record
	if record != nil && record.Stage == schedprefdomain.StageCompleted {
		report.AlreadyComplete = true
		report.CompletedStage = string(record.Stage)
		report.CompletedAt = record.CompletedAt
		return report, nil
	}

	scanned, err := scan(ctx, db, options.Dialect, batchSize)
	if err != nil {
		return report, err
	}
	plan := buildPlan(planInput{strategies: scanned.strategies, keys: scanned.keys, prefs: scanned.prefs})
	fillReport(&report, scanned, plan)
	if len(scanned.failures) > 0 || len(plan.failures) > 0 {
		report.Failures = append(append([]Failure{}, scanned.failures...), plan.failures...)
		sortFailures(report.Failures)
		return report, nil
	}

	// 领取 running 记录（已存在即续跑，不重复领取；INSERT ON CONFLICT 条件写
	// 入保证并行执行者只有一个能完成 None→running）。
	if record == nil {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		acquired, err := schedprefdomain.ClaimRunningStage(ctx, tx, options.Dialect, now)
		if err != nil {
			_ = tx.Rollback()
			return report, err
		}
		if err := tx.Commit(); err != nil {
			return report, err
		}
		if !acquired {
			latest, readErr := readRecord(ctx, db, options.Dialect)
			if readErr != nil {
				return report, readErr
			}
			stage := ""
			if latest != nil {
				stage = string(latest.Stage)
			}
			return report, fmt.Errorf("检测到并行迁移执行者：迁移记录已被推进为 %q，本次命令放弃执行（并行迁移进程不得同时运行）", stage)
		}
	}

	// 按 owner 分批短事务写入。
	preferencesCreated, preferencesReused, keysBackfilled, writeErr := writePlan(ctx, db, options.Dialect, plan, now)
	if writeErr != nil {
		return report, writeErr
	}
	report.PreferencesCreated = preferencesCreated
	report.PreferencesReused = plan.reused + preferencesReused
	report.APIKeysBackfilled = keysBackfilled

	// 后检与完成记录同事务：NULL 残留、引用完整、计数核对任一不满足都保留
	// running，不提前标记完成。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	postcheckFailures, err := postcheck(ctx, tx, options.Dialect, plan, keysBackfilled)
	if err != nil {
		return report, err
	}
	if len(postcheckFailures) > 0 {
		report.Failures = postcheckFailures
		sortFailures(report.Failures)
		return report, nil
	}
	counts := schedprefdomain.Counts{
		StrategiesSpeedFirstMigrated: report.StrategiesSpeedFirst,
		PreferencesCreated:           preferencesCreated,
		APIKeysNullGeneral:           report.APIKeysNullGeneral,
		APIKeysBackfilled:            int(keysBackfilled),
	}
	acquired, err := schedprefdomain.CompleteStage(ctx, tx, options.Dialect, counts, now)
	if err != nil {
		return report, err
	}
	if !acquired {
		return report, errors.New("完成阶段条件更新未命中：迁移记录已被并行执行者改变，保留 running")
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	report.CompletedStage = string(schedprefdomain.StageCompleted)
	report.CompletedAt = now.UTC().Format(time.RFC3339)
	report.Record = &schedprefdomain.Record{Stage: schedprefdomain.StageCompleted, RuleVersion: schedprefdomain.RuleVersion, CompletedAt: report.CompletedAt, Counts: counts}
	return report, nil
}

// fillReport 把扫描与规划计数折叠进迁移报告（不含写入结果）。
func fillReport(report *Report, scanned *scanResult, plan *planOutput) {
	seen := map[string]bool{}
	for _, strategy := range scanned.strategies {
		if !seen[strategy.owner] {
			seen[strategy.owner] = true
			report.OwnersTotal++
		}
	}
	report.StrategiesTotal = len(scanned.strategies)
	report.StrategiesSpeedFirst = plan.speedFirstStrategies
	report.StrategiesCostFirst = plan.costFirstStrategies
	report.PreferencesExisting = len(scanned.prefs)
	report.APIKeysNullGeneral = plan.nullGeneral
	for _, key := range scanned.keys {
		report.APIKeysTotal++
		switch {
		case key.purpose == "chat":
			report.APIKeysChat++
		case key.purpose == "studio":
			report.APIKeysStudioExcluded++
		}
		if key.bound.Valid && strings.TrimSpace(key.bound.String) != "" {
			report.APIKeysExplicitBound++
		}
	}
}

// writePlan 按 owner 分批短事务写入：每 owner 一个事务（等值偏好先行、Key 回
// 填随后，满足列级 REFERENCES belt）。只写 scheduling_preferences 新行与
// api_keys.scheduling_preference_id 列，不触碰策略 config_json 与 Key 其他列。
func writePlan(ctx context.Context, db *sql.DB, dialect capabilitydomain.Dialect, plan *planOutput, now time.Time) (created int, reused int, backfilled int64, err error) {
	timestamp := now.UTC().Format("2006-01-02T15:04:05.000") + "Z"
	// enabled 已随 v2 用户裁决修订第 4 条退役：等值偏好行不再写该列（创建即
	// 启用，无停用态）。
	insertSQL := `INSERT INTO scheduling_preferences
		(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
		VALUES (?, ?, ?, 'speed_first', ?, 0, ?, ?) ON CONFLICT (id) DO NOTHING`
	updateSQL := `UPDATE api_keys SET scheduling_preference_id = ?
		WHERE id = ? AND scheduling_preference_id IS NULL AND purpose = 'general'`
	if dialect == capabilitydomain.DialectPostgres {
		insertSQL = `INSERT INTO "juhe_business"."scheduling_preferences"
			(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ($1, $2, $3, 'speed_first', $4, 0, $5, $6) ON CONFLICT (id) DO NOTHING`
		updateSQL = `UPDATE "juhe_business"."api_keys" SET scheduling_preference_id = $1
			WHERE id = $2 AND scheduling_preference_id IS NULL AND purpose = 'general'`
	}
	owners := append([]string{}, plan.ownerOrder...)
	sort.Strings(owners)
	for _, owner := range owners {
		plans := plan.perOwner[owner]
		if plans == nil || (len(plans.preferences) == 0 && len(plans.keyUpdates) == 0) {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return created, reused, backfilled, err
		}
		for _, preference := range plans.preferences {
			result, err := tx.ExecContext(ctx, insertSQL, preference.id, preference.owner, preference.name, preference.configJSON, timestamp, timestamp)
			if err != nil {
				_ = tx.Rollback()
				return created, reused, backfilled, fmt.Errorf("写入等值偏好 %s 失败: %w", preference.id, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				_ = tx.Rollback()
				return created, reused, backfilled, fmt.Errorf("读取等值偏好 %s 写入行数失败: %w", preference.id, err)
			}
			if affected == 1 {
				created++
				continue
			}
			// 并行执行者已插入同 ID（领取语义下罕见）：校验等值后计复用。
			existing, equal, equalErr := readInsertedPreference(ctx, tx, dialect, preference)
			if equalErr != nil {
				_ = tx.Rollback()
				return created, reused, backfilled, equalErr
			}
			if !existing || !equal {
				_ = tx.Rollback()
				return created, reused, backfilled, fmt.Errorf("等值偏好 %s 已存在且与策略配置不等值（停写窗口被改写），拒绝覆盖", preference.id)
			}
			reused++
		}
		for _, update := range plans.keyUpdates {
			result, err := tx.ExecContext(ctx, updateSQL, update.preferenceID, update.id)
			if err != nil {
				_ = tx.Rollback()
				return created, reused, backfilled, fmt.Errorf("回填 API Key %s 绑定失败: %w", update.id, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				_ = tx.Rollback()
				return created, reused, backfilled, fmt.Errorf("读取 API Key %s 回填行数失败: %w", update.id, err)
			}
			backfilled += affected
		}
		if err := tx.Commit(); err != nil {
			return created, reused, backfilled, fmt.Errorf("提交 owner %s 迁移批次失败: %w", owner, err)
		}
	}
	return created, reused, backfilled, nil
}

// readInsertedPreference 读取并行插入的同 ID 偏好行并做规范化等值比较。
func readInsertedPreference(ctx context.Context, tx *sql.Tx, dialect capabilitydomain.Dialect, preference plannedWrite) (bool, bool, error) {
	query := `SELECT chat_mode, chat_speed_first_config_json FROM scheduling_preferences WHERE id = ?`
	if dialect == capabilitydomain.DialectPostgres {
		query = `SELECT chat_mode, chat_speed_first_config_json FROM "juhe_business"."scheduling_preferences" WHERE id = $1`
	}
	var chatMode string
	var configJSON sql.NullString
	err := tx.QueryRowContext(ctx, query, preference.id).Scan(&chatMode, &configJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("读取既有等值偏好 %s 失败: %w", preference.id, err)
	}
	stored, parseErr := parsePreferenceSpeedConfig(configJSON)
	if parseErr != nil || chatMode != "speed_first" {
		return true, false, nil
	}
	expected, expectErr := parsePreferenceSpeedConfig(sql.NullString{String: preference.configJSON, Valid: true})
	if expectErr != nil {
		return true, false, fmt.Errorf("解析计划配置 %s 失败: %w", preference.id, expectErr)
	}
	return true, equalNormalConfig(stored, expected), nil
}

// postcheck 只读核对：全库无 NULL 普通 Key 残留、绑定引用完整、回填计数与计划
// 一致。残留非零 → 拒绝写完成记录。残留谓词固定 purpose='general'：studio
// Key 的 NULL 不是残留（与 chat 同规，绑定列恒 NULL，见 buildPlan 排除臂），
// 也不参与回填计数核对。
func postcheck(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, dialect capabilitydomain.Dialect, plan *planOutput, keysBackfilled int64) ([]Failure, error) {
	failures := []Failure{}
	apiKeys := qualifiedTable(dialect, "api_keys")
	schedulingPreferences := qualifiedTable(dialect, "scheduling_preferences")
	var residual int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+apiKeys+` WHERE scheduling_preference_id IS NULL AND purpose = 'general'`).Scan(&residual); err != nil {
		return nil, fmt.Errorf("后检统计 NULL 普通 Key 残留失败: %w", err)
	}
	if residual > 0 {
		failures = append(failures, Failure{
			Kind:   FailureResidualNullKeys,
			Detail: fmt.Sprintf("仍有 %d 个 purpose=general 且绑定列为 NULL 的 API Key 未回填（停写窗口被并发写入或目标缺失），拒绝写完成记录", residual),
		})
	}
	var dangling int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+apiKeys+` api_keys LEFT JOIN `+schedulingPreferences+` scheduling_preferences
		ON scheduling_preferences.id = api_keys.scheduling_preference_id
		WHERE api_keys.scheduling_preference_id IS NOT NULL AND scheduling_preferences.id IS NULL`).Scan(&dangling); err != nil {
		return nil, fmt.Errorf("后检统计绑定引用失败: %w", err)
	}
	if dangling > 0 {
		failures = append(failures, Failure{
			Kind:   FailureDanglingBinding,
			Detail: fmt.Sprintf("存在 %d 个绑定引用指向不存在的调度偏好行，拒绝写完成记录", dangling),
		})
	}
	if int64(plan.nullGeneral) != keysBackfilled {
		failures = append(failures, Failure{
			Kind:   "backfill_count_mismatch",
			Detail: fmt.Sprintf("回填计数与计划不一致：计划 %d，实际 %d（缺失行见 NULL 残留或失败清单）", plan.nullGeneral, keysBackfilled),
		})
	}
	return failures, nil
}

// Verify 执行只读核验：完成记录存在且 completed、无 NULL 普通 Key 残留、绑定
// 引用完整。全程零写入。
func Verify(ctx context.Context, db *sql.DB, options Options) (VerifyReport, error) {
	report := VerifyReport{
		Driver:      options.Dialect.String(),
		RuleVersion: schedprefdomain.RuleVersion,
		Failures:    []Failure{},
	}
	record, err := readRecord(ctx, db, options.Dialect)
	if err != nil {
		return report, err
	}
	if record == nil {
		report.Failures = append(report.Failures, Failure{
			Kind:   FailureRecordMissing,
			Detail: "迁移记录 " + schedprefdomain.MigrationSettingKey + " 不存在：先运行 juhe-ai-maintenance --migrate-scheduling-preferences --node-stopped --go-stopped --backup-confirmed",
		})
	} else {
		report.Record = record
		report.RecordPresent = true
		if record.Stage != schedprefdomain.StageCompleted {
			report.Failures = append(report.Failures, Failure{
				Kind:   FailureRecordNotCompleted,
				Detail: "迁移记录当前为 " + string(record.Stage) + "，核验要求 completed：迁移未完成或停在中间阶段，同命令重跑续跑",
			})
		}
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	apiKeys := qualifiedTable(options.Dialect, "api_keys")
	schedulingPreferences := qualifiedTable(options.Dialect, "scheduling_preferences")
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+apiKeys).Scan(&report.APIKeysTotal); err != nil {
		return report, fmt.Errorf("核验统计 api_keys 失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+apiKeys+` WHERE scheduling_preference_id IS NULL AND purpose = 'general'`).Scan(&report.APIKeysNullGeneralResidual); err != nil {
		return report, fmt.Errorf("核验统计 NULL 普通 Key 残留失败: %w", err)
	}
	// 谓词 purpose='general' 已排除 studio：studio Key 的 NULL 不是残留（与
	// chat 同规，绑定列恒 NULL），不触发未迁移失败。
	if report.APIKeysNullGeneralResidual > 0 {
		report.Failures = append(report.Failures, Failure{
			Kind:   FailureResidualNullKeys,
			Detail: fmt.Sprintf("仍有 %d 个 purpose=general 且绑定列为 NULL 的 API Key（P2 完成态下属未迁移错误）", report.APIKeysNullGeneralResidual),
		})
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+apiKeys+` api_keys LEFT JOIN `+schedulingPreferences+` scheduling_preferences
		ON scheduling_preferences.id = api_keys.scheduling_preference_id
		WHERE api_keys.scheduling_preference_id IS NOT NULL AND scheduling_preferences.id IS NULL`).Scan(&report.DanglingBindingCount); err != nil {
		return report, fmt.Errorf("核验统计绑定引用失败: %w", err)
	}
	if report.DanglingBindingCount > 0 {
		report.Failures = append(report.Failures, Failure{
			Kind:   FailureDanglingBinding,
			Detail: fmt.Sprintf("存在 %d 个绑定引用指向不存在的调度偏好行", report.DanglingBindingCount),
		})
	}
	sortFailures(report.Failures)
	return report, nil
}
