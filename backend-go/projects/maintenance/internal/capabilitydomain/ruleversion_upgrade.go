// 推导规则 v2 升级执行器（《AI账户能力域向量重排拆分设计》§6，revision 29
// capability-domain-vector-rerank 步骤的库操作面）。与 M1/M2 迁移器（migration.go）
// 的边界：
//   - 只服务已封存（m2_finalized）的库：阶段记录 ruleVersion 1→2 经共享包
//     UpgradeRecordRuleVersion 专用通道升级（严格 ReadRecord 期望 2，不走
//     ParseRecord 读旧值）；M1/M2 赋域命令在封存后被状态机拒绝，本文件是
//     封存后唯一获授权改写 capability_domain 的入口。
//   - 重推导只作用于"声明含 embeddings_json / rerank_json"的账户（拆分的
//     直接涉及者）：纯单值声明落新域、混合声明进失败清单人工处理；其余账户
//     域已物化落库、不重推导（v2 与 v1 对不含两 mode 的输入输出一致，见共享
//     包 DeriveDomain v2 注释）。不裁剪声明（设计 §4：不自动拆域、不裁剪丢
//     能力），报告与失败清单脱敏，不含任何凭据内容。
//   - 授权实例行跟随源账户落域（实例域取源推导结果，与 M1/M2 两遍法同语义）。
//   - Apply 幂等：记录已是 ruleVersion=2 时跳过升级步，数据面按计划重算；
//     失败清单或混合清单非空时返回 Ready=false 且不写任何行，修复后同步骤
//     重跑收敛。
package capabilitydomain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// V2UpgradeOptions 是 v2 升级/核验命令的注入参数。
type V2UpgradeOptions struct {
	Secret    string // 解密密钥（调用方已经 ResolveSecret 归一）
	Now       time.Time
	BatchSize int // <=0 取 DefaultBatchSize
}

// V2Reassignment 是一行域重写明细（脱敏：账户 ID 与前后域值）。
type V2Reassignment struct {
	AccountID string `json:"accountId"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// V2UpgradeReport 是 revision 29 步骤 Apply / Verify 的 JSON 报告。
type V2UpgradeReport struct {
	Driver            string              `json:"driver"`
	Record            *capdomain.Record   `json:"record"`
	RecordUpgraded    bool                `json:"recordUpgraded"`
	AccountsTotal     int                 `json:"accountsTotal"`
	ReassignedRows    int64               `json:"reassignedRows"`
	Reassigned        []V2Reassignment    `json:"reassigned,omitempty"`
	PendingRows       int                 `json:"pendingRows,omitempty"`
	MixedDeclarations []capdomain.Failure `json:"mixedDeclarations"`
	Failures          []capdomain.Failure `json:"failures"`
}

// Ready 报告后置条件：失败清单与混合声明清单清零。
func (r V2UpgradeReport) Ready() bool {
	return len(r.Failures) == 0 && len(r.MixedDeclarations) == 0
}

// v2ScanAccount 是 v2 扫描阶段的一行内存投影（现落库域 + 声明推导结果）。
type v2ScanAccount struct {
	id            string
	currentDomain string
	isInstance    bool
	sourceID      string // 仅实例行非空
	reading       capdomain.ModesReading
	domain        capdomain.Domain // 非实例行 v2 推导结果；实例行=源推导结果
	decryptFailed bool
}

// scanV2Accounts 分批只读扫描全部账户（含软删除），读现落库域、解密凭据、
// 解析声明并按 v2 推导域；实例行不推导（域由 resolveV2Instances 落源域）。
// 五域退役终态（落库五域 + 凭据无键，revision 30 清洗后）不判
// modes_missing、不进重写计划，见 retiredDeclarationTerminal（上游接口能力
// 设计 §3）。每批先完整读取（rows.Err + Close）再处理下一批，全程不开写事务。
func scanV2Accounts(ctx context.Context, db *sql.DB, secret string, batchSize int) ([]v2ScanAccount, []capdomain.Failure, error) {
	var (
		all      []v2ScanAccount
		failures []capdomain.Failure
	)
	after := ""
	for {
		// $1 复用（空串同时充当哨兵与下界，与 accountRowQuery 同款写法）。
		rows, err := db.QueryContext(ctx, `SELECT id, capability_domain, credentials_encrypted, authorization_instance_source_account_id
			FROM "juhe_business"."accounts"
			WHERE ($1 = '' OR id > $1)
			ORDER BY id
			LIMIT $2`, after, batchSize)
		if err != nil {
			return nil, nil, fmt.Errorf("v2 扫描 accounts 失败: %w", err)
		}
		batch := []v2ScanAccount{}
		for rows.Next() {
			var (
				id          string
				domain      string
				credentials string
				source      sql.NullString
			)
			if err := rows.Scan(&id, &domain, &credentials, &source); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("读取 v2 扫描行失败: %w", err)
			}
			item := v2ScanAccount{id: id, currentDomain: domain}
			if source.Valid && strings.TrimSpace(source.String) != "" {
				item.isInstance = true
				item.sourceID = strings.TrimSpace(source.String)
			}
			var credentialsMap map[string]any
			if err := accountcrypto.DecryptJSON(secret, credentials, &credentialsMap); err != nil {
				item.decryptFailed = true
			} else {
				item.reading = capdomain.ParseEndpointModes(credentialsMap)
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("遍历 v2 扫描 accounts 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if item.decryptFailed {
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      capdomain.FailureCredentialsDecrypt,
					Detail:    "credentials_encrypted 不是合法 v1 信封或解密失败",
				})
			} else if item.isInstance {
				// 实例行维持 M2 既有宽松语义（声明不参与域推导）；源行格式
				// 错误由源行自身入清单，实例行不重复计。
			} else if retiredDeclarationTerminal(item.currentDomain, item.reading) {
				// 五域退役终态（上游接口能力设计 §3）：声明面已清洗，凭据无
				// supported_endpoint_modes 键是合法形态，域以落库列为权威，
				// 不再按 modes_missing 判失败、不进重写计划（现域即目标域）。
				item.domain = capdomain.Domain(item.currentDomain)
			} else if failure, bad := capdomain.FailureFor(item.id, item.reading); bad {
				failures = append(failures, failure)
			} else {
				item.domain = capdomain.DeriveDomain(item.reading.Modes)
			}
			all = append(all, item)
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return nil, nil, errors.New("v2 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return all, failures, nil
}

// resolveV2Instances 在内存中把实例行落到源账户的 v2 推导域。源行不存在、
// 源行自身也是实例或源行推导域非法都计为实例依赖失败。
func resolveV2Instances(accounts []v2ScanAccount) []capdomain.Failure {
	byID := make(map[string]v2ScanAccount, len(accounts))
	for _, item := range accounts {
		byID[item.id] = item
	}
	failures := []capdomain.Failure{}
	for i := range accounts {
		item := &accounts[i]
		if !item.isInstance {
			continue
		}
		source, ok := byID[item.sourceID]
		switch {
		case !ok:
			failures = append(failures, capdomain.Failure{
				AccountID: item.id,
				Kind:      capdomain.FailureInstanceNoSource,
				Detail:    "authorization_instance_source_account_id 指向的源账户行不存在: " + item.sourceID,
			})
		case source.isInstance:
			failures = append(failures, capdomain.Failure{
				AccountID: item.id,
				Kind:      capdomain.FailureInstanceNoSource,
				Detail:    "源账户本身也是授权实例（关联不完整）: " + item.sourceID,
			})
		default:
			if !source.domain.Valid() {
				if retiredDeclarationTerminal(source.currentDomain, source.reading) {
					// 源账户为五域退役终态：实例域取源落库域（列权威），
					// 不按「源声明不可推导」判失败（上游接口能力设计 §3）。
					item.domain = capdomain.Domain(source.currentDomain)
					continue
				}
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      capdomain.FailureInstanceSourceFailed,
					Detail:    "源账户声明不可推导，实例列依赖失败: " + item.sourceID,
				})
				continue
			}
			item.domain = source.domain
		}
	}
	return failures
}

// planV2Reassignments 生成重写计划：只覆盖"声明含 embeddings_json /
// rerank_json"的账户链（直接涉及者及其授权实例；实例域恒取源推导结果，源
// 非直接涉及者但实例现值与源漂移时同样按现域条件修复）；混合声明进清单并
// 从计划中剔除该链（不自动拆域、不裁剪丢能力）。返回计划（现域 ≠ 目标域的
// 行）、混合清单与计划明细。
func planV2Reassignments(accounts []v2ScanAccount) ([]v2ScanAccount, []capdomain.Failure, []V2Reassignment) {
	plan := []v2ScanAccount{}
	mixed := []capdomain.Failure{}
	changed := []V2Reassignment{}
	for i := range accounts {
		item := &accounts[i]
		if !item.domain.Valid() || item.decryptFailed {
			continue
		}
		if item.reading.Status == capdomain.ModesOK && vectorRerankDeclared(item.reading.Modes) && item.domain == capdomain.DomainChat {
			// 混合声明：向量/重排与对话生成形态、媒体触发值或彼此同存
			//（v2 推导落 chat 即混合），进失败清单人工处理，不改写。
			mixed = append(mixed, capdomain.Failure{
				AccountID: item.id,
				Kind:      capdomain.FailureMixedVectorRerank,
				Detail:    "声明同时包含 embeddings_json/rerank_json 与对话生成形态、媒体触发值或另一新域 mode；不自动拆域不裁剪，建议拆分为独立账户（向量域或重排域各一）",
			})
			continue
		}
		if item.currentDomain != string(item.domain) {
			plan = append(plan, *item)
			changed = append(changed, V2Reassignment{AccountID: item.id, From: item.currentDomain, To: string(item.domain)})
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].AccountID < changed[j].AccountID })
	return plan, mixed, changed
}

// vectorRerankDeclared 报告声明是否含 embeddings_json 或 rerank_json。
func vectorRerankDeclared(modes []string) bool {
	for _, mode := range modes {
		if mode == "embeddings_json" || mode == "rerank_json" {
			return true
		}
	}
	return false
}

// writeV2Reassignments 分批短事务写入重写计划。每条 UPDATE 携带现域条件
//（capability_domain = 旧值），停写窗口内被并发漂移的行以 affected=0 暴露为
// 失败，不静默覆盖。不触碰 config_revision/dispatch_revision/updated_at。
func writeV2Reassignments(ctx context.Context, db *sql.DB, plan []v2ScanAccount, batchSize int) (int64, []capdomain.Failure, error) {
	updateSQL := `UPDATE "juhe_business"."accounts" SET capability_domain = $1 WHERE id = $2 AND capability_domain = $3`
	var updated int64
	failures := []capdomain.Failure{}
	for start := 0; start < len(plan); start += batchSize {
		end := start + batchSize
		if end > len(plan) {
			end = len(plan)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return updated, failures, err
		}
		batchUpdated := int64(0)
		for _, item := range plan[start:end] {
			result, err := tx.ExecContext(ctx, updateSQL, string(item.domain), item.id, item.currentDomain)
			if err != nil {
				_ = tx.Rollback()
				return updated, failures, fmt.Errorf("写入账户 %s 的 capability_domain 失败: %w", item.id, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				_ = tx.Rollback()
				return updated, failures, fmt.Errorf("读取账户 %s 的更新行数失败: %w", item.id, err)
			}
			if affected != 1 {
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      "v2_reassign_race",
					Detail:    fmt.Sprintf("重写时落库域已不是扫描现值 %s（停写窗口内被并发写入），拒绝覆盖", item.currentDomain),
				})
				continue
			}
			batchUpdated += affected
		}
		if err := tx.Commit(); err != nil {
			return updated, failures, fmt.Errorf("提交 v2 重写批次失败: %w", err)
		}
		updated += batchUpdated
	}
	return updated, failures, nil
}

// v2StoredRow 是落库对账扫描的一行投影。
type v2StoredRow struct {
	id       string
	domain   string
	isInst   bool
	sourceID string
}

// scanV2StoredDomains 流式读回全部行的落库域值（keyset 分批，完整读取）。
func scanV2StoredDomains(ctx context.Context, db *sql.DB, batchSize int) ([]v2StoredRow, error) {
	all := []v2StoredRow{}
	after := ""
	for {
		rows, err := db.QueryContext(ctx, `SELECT id, capability_domain, authorization_instance_source_account_id
			FROM "juhe_business"."accounts"
			WHERE ($1 = '' OR id > $1)
			ORDER BY id
			LIMIT $2`, after, batchSize)
		if err != nil {
			return nil, fmt.Errorf("v2 对账扫描 accounts 失败: %w", err)
		}
		batch := []v2StoredRow{}
		for rows.Next() {
			var (
				id     string
				domain string
				source sql.NullString
			)
			if err := rows.Scan(&id, &domain, &source); err != nil {
				rows.Close()
				return nil, fmt.Errorf("读取 v2 对账行失败: %w", err)
			}
			item := v2StoredRow{id: id, domain: domain}
			if source.Valid && strings.TrimSpace(source.String) != "" {
				item.isInst = true
				item.sourceID = strings.TrimSpace(source.String)
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("遍历 v2 对账 accounts 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return nil, errors.New("v2 对账扫描 keyset 游标未前进，终止避免死循环")
		}
		after = last
	}
	return all, nil
}

// reconcileV2Stored 对落库现值做对账：域值合法、直接涉及行现域 == 计划域
//（统计 pending）、实例与源落库一致。返回 pending 行数与失败清单。
func reconcileV2Stored(stored []v2StoredRow, plan []v2ScanAccount, accounts []v2ScanAccount) (int, []capdomain.Failure) {
	targets := make(map[string]string, len(plan))
	for _, item := range plan {
		targets[item.id] = string(item.domain)
	}
	storedByID := make(map[string]string, len(stored))
	failures := []capdomain.Failure{}
	pending := 0
	for _, item := range stored {
		storedByID[item.id] = item.domain
		if !capdomain.Domain(item.domain).Valid() {
			failures = append(failures, capdomain.Failure{AccountID: item.id, Kind: "domain_value_invalid", Detail: "落库域值不在 chat/video/audio/image/embedding/rerank 内: " + item.domain})
			continue
		}
		if want, ok := targets[item.id]; ok && want != item.domain {
			pending++
		}
	}
	// 实例与源落库一致（按双方现值核对，封存后语义与 verifyDomains 相同）；
	// 源缺失已在 resolveV2Instances 入清单，此处不重复计。
	for _, item := range accounts {
		if !item.isInstance {
			continue
		}
		sourceDomain, ok := storedByID[item.sourceID]
		if !ok {
			continue
		}
		storedDomain, present := storedByID[item.id]
		if present && storedDomain != sourceDomain {
			failures = append(failures, capdomain.Failure{
				AccountID: item.id,
				Kind:      "instance_domain_mismatch",
				Detail:    fmt.Sprintf("实例落库域 %s 与源落库域 %s 不一致", storedDomain, sourceDomain),
			})
		}
	}
	return pending, failures
}

// readRawRecord 读取阶段记录原始 JSON（不校验 ruleVersion，供升级通道消费）。
// 无记录返回 ok=false。
func readRawRecord(ctx context.Context, db *sql.DB) (string, bool, error) {
	query := `SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`
	var valueJSON string
	err := db.QueryRowContext(ctx, query, capdomain.MigrationSettingKey).Scan(&valueJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return valueJSON, true, nil
}

// UpgradeRuleVersionV2 执行 revision 29 步骤的 Apply 语义：阶段记录
// ruleVersion 1→2（stage 保持 m2_finalized）+ 按 v2 重推导直接涉及账户域。
// 语义：
//  1. 记录必须已存在且 stage=m2_finalized（M1/M2 未封存的库先跑 revision 4
//     迁移命令，本步骤不代劳）；
//  2. ruleVersion=1 → 专用通道升级（条件 UPDATE 带 ruleVersion 谓词，单执行
//     者）；ruleVersion=2 → 已升级跳过（幂等重跑）；其他值拒绝；
//  3. 只读扫描推导 → 解密/声明失败或混合声明清单非空 → 返回 Ready=false
//     （不写任何行）；
//  4. 分批短事务写入（带现域条件）→ 只读对账；对账失败返回 Ready=false，
//     修复后同步骤重跑收敛。
func UpgradeRuleVersionV2(ctx context.Context, db *sql.DB, options V2UpgradeOptions) (V2UpgradeReport, error) {
	report := V2UpgradeReport{
		Driver:            "postgres",
		MixedDeclarations: []capdomain.Failure{},
		Failures:          []capdomain.Failure{},
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	raw, present, err := readRawRecord(ctx, db)
	if err != nil {
		return report, err
	}
	if !present {
		return report, errors.New("v2 升级要求阶段记录已存在且封存 m2_finalized（先执行 revision 4 能力域迁移命令）")
	}
	var current capdomain.Record
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return report, fmt.Errorf("阶段记录 %s 不是合法 JSON: %w", capdomain.MigrationSettingKey, err)
	}
	switch current.Stage {
	case capdomain.StageM2Finalized:
	default:
		return report, fmt.Errorf("阶段记录 %s 的 stage 为 %q，v2 升级要求已封存 m2_finalized", capdomain.MigrationSettingKey, current.Stage)
	}
	switch current.RuleVersion {
	case 1:
		payload, upgradeErr := capdomain.UpgradeRecordRuleVersion(raw, now)
		if upgradeErr != nil {
			return report, upgradeErr
		}
		result, execErr := db.ExecContext(ctx, `UPDATE "juhe_business"."global_settings" SET value_json = $1, updated_at = $2
			WHERE key = $3 AND value_json::jsonb->>'ruleVersion' = '1'`,
			payload, now.UTC().Format(time.RFC3339), capdomain.MigrationSettingKey)
		if execErr != nil {
			return report, fmt.Errorf("升级阶段记录 %s 失败: %w", capdomain.MigrationSettingKey, execErr)
		}
		affected, rowErr := result.RowsAffected()
		if rowErr != nil {
			return report, fmt.Errorf("读取阶段记录升级行数失败: %w", rowErr)
		}
		if affected != 1 {
			return report, errors.New("阶段记录升级条件更新未命中：ruleVersion 已被并行执行者改变，本次放弃")
		}
		report.RecordUpgraded = true
		upgradedRecord, parseErr := capdomain.ParseRecord(payload)
		if parseErr != nil {
			return report, parseErr
		}
		report.Record = &upgradedRecord
	case capdomain.RuleVersion:
		report.Record = &current
	default:
		return report, fmt.Errorf("阶段记录 %s 的 ruleVersion 为 %d，只接受 1（升级）或 %d（已完成）", capdomain.MigrationSettingKey, current.RuleVersion, capdomain.RuleVersion)
	}

	// 只读扫描推导（含软删除、实例继承）。
	accounts, failures, err := scanV2Accounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	failures = append(failures, resolveV2Instances(accounts)...)
	report.AccountsTotal = len(accounts)
	if len(failures) > 0 {
		sortFailures(failures)
		report.Failures = failures
		return report, nil
	}

	plan, mixed, changed := planV2Reassignments(accounts)
	report.MixedDeclarations = mixed
	if len(mixed) > 0 {
		// 混合声明必须人工处理（拆分独立账户），升级保持未收敛。
		return report, nil
	}
	if len(plan) == 0 {
		return report, nil
	}

	// 分批短事务写入（带现域条件）。
	updated, writeFailures, err := writeV2Reassignments(ctx, db, plan, batchSize)
	if err != nil {
		return report, err
	}
	report.ReassignedRows = updated
	report.Reassigned = changed
	if len(writeFailures) > 0 {
		sortFailures(writeFailures)
		report.Failures = writeFailures
		return report, nil
	}

	// 只读对账：直接涉及行现域 == 计划域、实例与源一致。
	stored, err := scanV2StoredDomains(ctx, db, batchSize)
	if err != nil {
		return report, err
	}
	_, verifyFailures := reconcileV2Stored(stored, plan, accounts)
	report.Failures = verifyFailures
	sortFailures(report.Failures)
	return report, nil
}

// VerifyRuleVersionV2 执行 revision 29 步骤的 Satisfied / Verify 语义（全程
// 零写入）：记录已升级为 ruleVersion=2 且封存（严格 ReadRecord），失败/混合
// 清单清零，直接涉及行现域 == v2 推导域（pending=0）。
func VerifyRuleVersionV2(ctx context.Context, db *sql.DB, options V2UpgradeOptions) (V2UpgradeReport, error) {
	report := V2UpgradeReport{
		Driver:            "postgres",
		MixedDeclarations: []capdomain.Failure{},
		Failures:          []capdomain.Failure{},
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	record, present, err := capdomain.ReadRecord(ctx, db)
	if err != nil {
		return report, err
	}
	if !present {
		return report, errors.New("v2 核验要求阶段记录已存在（先执行 revision 4 能力域迁移命令与 revision 29 升级）")
	}
	report.Record = &record

	accounts, failures, err := scanV2Accounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	failures = append(failures, resolveV2Instances(accounts)...)
	report.AccountsTotal = len(accounts)
	if len(failures) > 0 {
		sortFailures(failures)
		report.Failures = failures
		return report, nil
	}

	plan, mixed, _ := planV2Reassignments(accounts)
	report.MixedDeclarations = mixed
	stored, err := scanV2StoredDomains(ctx, db, batchSize)
	if err != nil {
		return report, err
	}
	pending, verifyFailures := reconcileV2Stored(stored, plan, accounts)
	report.PendingRows = pending
	report.Failures = append(report.Failures, verifyFailures...)
	sortFailures(report.Failures)
	return report, nil
}
