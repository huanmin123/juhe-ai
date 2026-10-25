// 迁移器：四阶段执行协议（PLAN §执行协议）中的「执行」与「核验」库操作面。
//
//   - 停写与备份由 CLI 门禁旗标表达，命令本身不负责停进程；
//   - 执行顺序：核对阶段记录（条件更新领取）→ 加列并校验形状 → 源账户
//     分批赋值 → 授权实例分批继承（两遍法：先只读推导全部账户，再分批
//     写入，实例域取源账户推导结果）；
//   - 每批短事务；批间只读扫描完整读取（rows.Err + Close）后再进入写事务
//     （BUG-0308）；迁移命令执行「赋域 + 声明裁剪」：写 capability_domain
//     的同时把 ModesOK 行的 supported_endpoint_modes 裁剪至落域 lane 集
//     （kept = declared ∩ lane，保序去重，仅替换该键、其余凭据键原样保留）
//     并同语句回写 credentials_encrypted——账户声明集必须 ⊆ 所在域允许集，
//     不存在任何跨域保留状态。config_revision/dispatch_revision/updated_at
//     一律不动：--node-stopped --go-stopped 门禁保证停写窗口内无并发写，
//     版本列无增量含义，改动反而会伪造配置变更；
//   - 解密失败、声明格式错误或缺失源账户不猜域，计入失败清单；失败清单
//     非空或任一批失败都返回非零结果且不写完成阶段；最终落库核验与完成
//     记录提交处于同一事务，核验或阶段提交失败都保留 running。
//
// 域模型、声明解析与阶段记录语义在共享包
// github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain（2026-10
// M2 自本包下沉：gateway/jobs 启动门禁跨模块读取所需）；本包只保留迁移
// 执行器，全部经 capdomain 别名引用共享符号，不复制任何解析语义。

package capabilitydomain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// DefaultDevSecret 与 schema.seedDefaultRuntimeSecret 同源（Node
// defaultRuntimeSecret）：未显式提供 --secret/JUHE_AI_SECRET 时按 seed 同款
// 规则回退，保证开发库凭据可解密。
const DefaultDevSecret = "juhe-ai-dev-secret-change-me"

// ResolveSecret 按 旗标 → JUHE_AI_SECRET → Node dev default 的顺序解析解密
// 密钥（与 seed/mockdata 同一链）。
func ResolveSecret(flagValue string) string {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		return trimmed
	}
	if env := strings.TrimSpace(os.Getenv("JUHE_AI_SECRET")); env != "" {
		return env
	}
	return DefaultDevSecret
}

// OpenPostgres 打开业务 PostgreSQL 连接（显式 maintenance 作用域 URL，无
// 应用连接串回退）。
func OpenPostgres(rawURL string) (*sql.DB, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "postgres://") && !strings.HasPrefix(rawURL, "postgresql://") {
		return nil, errors.New("capability domain 迁移必须提供 postgres:// 或 postgresql:// URL")
	}
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		return nil, fmt.Errorf("打开 capability domain 迁移 PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Options 是迁移/核验命令的注入参数（PG 单形态；sqlite 方言随 SQLite 存储
// 形态退役删除，PLAN-20261024T051925945Z B4）。
type Options struct {
	Target    capdomain.Target
	Check     string // preflight | postflight（仅 Verify 消费）
	Secret    string // 解密密钥（调用方已经 ResolveSecret 归一）
	Now       time.Time
	BatchSize int // <=0 取 DefaultBatchSize
}

// DefaultBatchSize 是分批读取/写入的默认批大小。
const DefaultBatchSize = 500

// DomainCounts 是各域账户计数（报告 JSON）。
type DomainCounts map[string]int

// Report 是 migrate 命令的 JSON 报告。
type Report struct {
	Driver          string               `json:"driver"`
	Target          string               `json:"target"`
	RuleVersion     int                  `json:"ruleVersion"`
	Record          *capdomain.Record    `json:"record"`
	AlreadyComplete bool                 `json:"alreadyComplete"`
	ColumnEnsured   bool                 `json:"columnEnsured"`
	AccountsTotal   int                  `json:"accountsTotal"`
	UpdatedRows     int64                `json:"updatedRows"`
	DomainsAssigned DomainCounts         `json:"domainsAssigned"`
	Trimmed         []TrimmedDeclaration `json:"trimmedDeclarations,omitempty"`
	Failures        []capdomain.Failure  `json:"failures"`
	CompletedStage  capdomain.Stage      `json:"completedStage,omitempty"`
	CompletedAt     string               `json:"completedAt,omitempty"`
}

// TrimmedModeRecommendation 是单个被裁值的建议归属域明细（按
// DeriveDomain([]string{被裁值}) 单值推导），供操作者为被裁能力另建归属域
// 正确的账户。
type TrimmedModeRecommendation struct {
	Mode            string `json:"mode"`
	SuggestedDomain string `json:"suggestedDomain"`
}

// TrimmedDeclaration 是报告的声明裁剪明细行：账户 ID、落域与被裁剪值列表。
// 报告继续脱敏：不含任何凭据内容。
type TrimmedDeclaration struct {
	AccountID string                      `json:"accountId"`
	Domain    string                      `json:"domain"`
	Removed   []TrimmedModeRecommendation `json:"removed"`
}

// Ready 报告后置条件：失败清单清零且阶段推进到完成（或此前已完成）。
func (r Report) Ready() bool {
	return len(r.Failures) == 0 && r.CompletedStage != capdomain.StageNone
}

// scannedAccount 是扫描阶段的一行内存投影（推导列 + 裁剪面：解密后的凭据
// 记录在写入阶段重加密回填，报告只输出脱敏明细）。
type scannedAccount struct {
	id            string
	isInstance    bool
	sourceID      string // 仅实例行非空
	reading       capdomain.ModesReading
	domain        capdomain.Domain // 非实例行推导结果、实例行继承结果（reading 为 ModesOK 时有效）
	decryptFailed bool
	// credentials 是解密后的凭据记录（仅解密成功行携带）；trimTo 非 nil =
	// 声明集越出落域 lane，写入阶段按 kept 集裁剪后重加密回写。
	credentials map[string]any
	trimTo      []string
}

// accountRowQuery 返回扫描投影 SQL（含软删除账户，按 id keyset 分批）；
// $1 复用两次（after 同时充当空游标哨兵与下界）。
func accountRowQuery(batchSize int) string {
	return `SELECT id, credentials_encrypted, authorization_instance_source_account_id FROM "juhe_business"."accounts"
		WHERE ($1 = '' OR id > $1)
		ORDER BY id
		LIMIT $2`
}

// scanArgs 组装扫描查询参数。
func scanArgs(after string, batchSize int) []any {
	return []any{after, batchSize}
}

// scanAccounts 分批只读扫描全部账户（含软删除），解密凭据、解析声明并推导
// 源账户域，实例行另解密自身凭据以支撑声明裁剪。每批先完整读取（rows.Err +
// Close）再处理下一批，全程不开写事务。
func scanAccounts(ctx context.Context, db *sql.DB, secret string, batchSize int) ([]scannedAccount, []capdomain.Failure, error) {
	var (
		all      []scannedAccount
		failures []capdomain.Failure
	)
	after := ""
	for {
		rows, err := db.QueryContext(ctx, accountRowQuery(batchSize), scanArgs(after, batchSize)...)
		if err != nil {
			return nil, nil, fmt.Errorf("扫描 accounts 失败: %w", err)
		}
		batch := []scannedAccount{}
		for rows.Next() {
			var (
				id          string
				credentials string
				source      sql.NullString
			)
			if err := rows.Scan(&id, &credentials, &source); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("读取 accounts 行失败: %w", err)
			}
			item := scannedAccount{id: id}
			if source.Valid && strings.TrimSpace(source.String) != "" {
				item.isInstance = true
				item.sourceID = strings.TrimSpace(source.String)
				// 实例行解密自身凭据以支撑声明裁剪；解密失败阻断该阶段（失败
				// 清单语义不变），声明解析非 ModesOK 维持既有宽松语义（实例
				// 声明不参与域推导，无可裁集即不裁剪）。
				var credentialsMap map[string]any
				if err := accountcrypto.DecryptJSON(secret, credentials, &credentialsMap); err != nil {
					item.decryptFailed = true
					failures = append(failures, capdomain.Failure{
						AccountID: item.id,
						Kind:      capdomain.FailureCredentialsDecrypt,
						Detail:    "credentials_encrypted 不是合法 v1 信封或解密失败（授权实例行）",
					})
				} else {
					item.credentials = credentialsMap
					item.reading = capdomain.ParseEndpointModes(credentialsMap)
				}
			} else {
				var credentialsMap map[string]any
				if err := accountcrypto.DecryptJSON(secret, credentials, &credentialsMap); err != nil {
					item.decryptFailed = true
				} else {
					item.credentials = credentialsMap
					item.reading = capdomain.ParseEndpointModes(credentialsMap)
				}
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("遍历 accounts 失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			switch {
			case item.isInstance:
				all = append(all, item)
			case item.decryptFailed:
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      capdomain.FailureCredentialsDecrypt,
					Detail:    "credentials_encrypted 不是合法 v1 信封或解密失败",
				})
				all = append(all, item)
			default:
				if failure, bad := capdomain.FailureFor(item.id, item.reading); bad {
					failures = append(failures, failure)
					all = append(all, item)
					continue
				}
				item.domain = capdomain.DeriveDomain(item.reading.Modes)
				all = append(all, item)
			}
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return nil, nil, errors.New("accounts 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return all, failures, nil
}

// resolveInstances 在内存中把实例行落到源账户域。源行不存在、源行自身失败
// 或源行也是实例（关联不完整/嵌套实例）都计为实例依赖失败，不改写实例域。
func resolveInstances(accounts []scannedAccount) []capdomain.Failure {
	byID := make(map[string]scannedAccount, len(accounts))
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
		case source.decryptFailed:
			failures = append(failures, capdomain.Failure{
				AccountID: item.id,
				Kind:      capdomain.FailureInstanceSourceFailed,
				Detail:    "源账户凭据解密失败，实例列依赖失败: " + item.sourceID,
			})
		default:
			sourceFailure, bad := capdomain.FailureFor(item.sourceID, source.reading)
			if bad || !source.domain.Valid() {
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      capdomain.FailureInstanceSourceFailed,
					Detail:    "源账户声明不可推导，实例列依赖失败: " + item.sourceID + "（" + string(sourceFailure.Kind) + "）",
				})
				continue
			}
			item.domain = source.domain
		}
	}
	return failures
}

// planDeclarationTrims 对每个 ModesOK 行按落域 lane 集裁剪声明（kept =
// declared ∩ lane，保序去重），标记需回写的行并返回报告明细（按账户 ID 排序）。
// 授权实例行按其最终落域同样裁剪；声明非 ModesOK 的行不裁剪（失败清单已
// 覆盖源行格式错误，实例行维持既有宽松语义）。kept 与 declared 同长即同序
// 同值（kept 恒为 declared 的保序子集），零改动行不产生明细。
func planDeclarationTrims(accounts []scannedAccount) []TrimmedDeclaration {
	trims := []TrimmedDeclaration{}
	for index := range accounts {
		item := &accounts[index]
		if !item.domain.Valid() || item.credentials == nil || item.reading.Status != capdomain.ModesOK {
			continue
		}
		lanes := capdomain.LaneModesForDomain(item.domain)
		kept := make([]string, 0, len(item.reading.Modes))
		seen := map[string]bool{}
		for _, mode := range item.reading.Modes {
			if seen[mode] {
				continue
			}
			for _, lane := range lanes {
				if mode == lane {
					seen[mode] = true
					kept = append(kept, mode)
					break
				}
			}
		}
		if len(kept) == len(item.reading.Modes) {
			continue
		}
		trimmed := TrimmedDeclaration{AccountID: item.id, Domain: string(item.domain), Removed: []TrimmedModeRecommendation{}}
		counted := map[string]bool{}
		for _, mode := range item.reading.Modes {
			if seen[mode] || counted[mode] {
				continue
			}
			counted[mode] = true
			trimmed.Removed = append(trimmed.Removed, TrimmedModeRecommendation{
				Mode:            mode,
				SuggestedDomain: string(capdomain.DeriveDomain([]string{mode})),
			})
		}
		item.trimTo = kept
		trims = append(trims, trimmed)
	}
	sort.Slice(trims, func(i, j int) bool { return trims[i].AccountID < trims[j].AccountID })
	return trims
}

// writeDomains 分批短事务写入推导结果。每批一个事务，批内只有 UPDATE；
// 声明越出落域 lane 的行在同一语句内回写裁剪后的凭据（赋域 + 声明裁剪原子
// 生效，仅替换 supported_endpoint_modes 键、其余凭据键原样保留），不触碰
// config_revision/dispatch_revision/updated_at（停写窗口内无并发写，版本列
// 无增量含义）。
func writeDomains(ctx context.Context, db *sql.DB, secret string, accounts []scannedAccount, batchSize int) (int64, error) {
	updateSQL := `UPDATE "juhe_business"."accounts" SET capability_domain = $1 WHERE id = $2`
	updateWithModesSQL := `UPDATE "juhe_business"."accounts" SET capability_domain = $1, credentials_encrypted = $2 WHERE id = $3`
	var updated int64
	for start := 0; start < len(accounts); start += batchSize {
		end := start + batchSize
		if end > len(accounts) {
			end = len(accounts)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return updated, err
		}
		batchUpdated := int64(0)
		for _, item := range accounts[start:end] {
			if !item.domain.Valid() {
				_ = tx.Rollback()
				return updated, fmt.Errorf("账户 %s 缺少可写域（内部错误，拒绝提交批次）", item.id)
			}
			var err error
			var affected int64
			if item.trimTo != nil {
				item.credentials["supported_endpoint_modes"] = item.trimTo
				sealed, sealErr := accountcrypto.EncryptJSON(secret, item.credentials)
				if sealErr != nil {
					_ = tx.Rollback()
					return updated, fmt.Errorf("重加密账户 %s 的裁剪后凭据失败: %w", item.id, sealErr)
				}
				result, execErr := tx.ExecContext(ctx, updateWithModesSQL, string(item.domain), sealed, item.id)
				err = execErr
				if execErr == nil {
					affected, _ = result.RowsAffected()
				}
			} else {
				result, execErr := tx.ExecContext(ctx, updateSQL, string(item.domain), item.id)
				err = execErr
				if execErr == nil {
					affected, _ = result.RowsAffected()
				}
			}
			if err != nil {
				_ = tx.Rollback()
				return updated, fmt.Errorf("写入账户 %s 的 capability_domain 失败: %w", item.id, err)
			}
			batchUpdated += affected
		}
		if err := tx.Commit(); err != nil {
			return updated, fmt.Errorf("提交 capability_domain 批次失败: %w", err)
		}
		updated += batchUpdated
	}
	return updated, nil
}

// querier 是 *sql.DB 与 *sql.Tx 共有的只读查询面（verifyDomains 需要在
// 完成事务内执行最终核验）。
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// verifyDomains 只读核对全部账户域值与推导计划一致（分批完整读取）。
// 已封存（m2_finalized）时跳过推导对账，只核对域值合法与实例同源一致。
func verifyDomains(ctx context.Context, q querier, accounts []scannedAccount, finalized bool) ([]capdomain.Failure, error) {
	var failures []capdomain.Failure
	expected := make(map[string]capdomain.Domain, len(accounts))
	for _, item := range accounts {
		if item.isInstance || item.decryptFailed {
			continue
		}
		if _, bad := capdomain.FailureFor(item.id, item.reading); !bad {
			expected[item.id] = item.domain
		}
	}
	instanceDomains := make(map[string]capdomain.Domain, len(accounts))
	for _, item := range accounts {
		if item.isInstance && item.domain.Valid() {
			instanceDomains[item.id] = item.domain
		}
	}
	table := `"juhe_business"."accounts"`
	after := ""
	batchSize := DefaultBatchSize
	seen := 0
	// verifyRow 是核验流式扫描的一行投影（类型提升到函数体：封存留底切片
	// sealedInstances 在循环外引用它）。
	type verifyRow struct {
		id         string
		domain     string
		isInstance bool
		sourceID   string
	}
	// 封存核验需要实例与源的落库现值：流式扫描时全量留底（行数=账户总量，
	// 与既有扫描同量级）。
	var sealedStoredByID map[string]string
	var sealedInstances []verifyRow
	if finalized {
		sealedStoredByID = make(map[string]string)
	}
	for {
		// $1 复用（与 accountRowQuery 同款写法）：此前写成
		// `id > $2` 并把空串绑到 $2，keyset 游标恒不前进，核验在第二批
		// 触发死循环保护（2026-10-15 生产 M1 迁移实测）。
		query := fmt.Sprintf(`SELECT id, capability_domain, authorization_instance_source_account_id FROM %s
				WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`, table)
		rows, err := q.QueryContext(ctx, query, after, batchSize)
		if err != nil {
			return nil, fmt.Errorf("核验 accounts 域值失败: %w", err)
		}
		batch := []verifyRow{}
		for rows.Next() {
			var (
				id, domain string
				source     sql.NullString
			)
			if err := rows.Scan(&id, &domain, &source); err != nil {
				rows.Close()
				return nil, fmt.Errorf("读取核验行失败: %w", err)
			}
			item := verifyRow{id: id, domain: domain}
			if source.Valid && strings.TrimSpace(source.String) != "" {
				item.isInstance = true
				item.sourceID = strings.TrimSpace(source.String)
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("遍历核验行失败: %w", err)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			seen++
			if finalized {
				sealedStoredByID[row.id] = row.domain
				if row.isInstance {
					sealedInstances = append(sealedInstances, row)
				}
			}
			value := capdomain.Domain(row.domain)
			if !value.Valid() {
				failures = append(failures, capdomain.Failure{AccountID: row.id, Kind: "domain_value_invalid", Detail: "落库域值不在 chat/video/audio/image 内: " + row.domain})
				continue
			}
			if finalized {
				// 封存后推导对账不适用（modes 可能已被 M2 写契约改写）；
				// 域值合法性已在上方核对，实例与源的一致性在流式扫描结束后
				// 按双方落库现值统一核对。
				continue
			}
			if row.isInstance {
				if want, ok := instanceDomains[row.id]; ok && want != value {
					failures = append(failures, capdomain.Failure{AccountID: row.id, Kind: "instance_domain_mismatch", Detail: fmt.Sprintf("实例落库域 %s 与源推导域 %s 不一致", row.domain, want)})
				}
				continue
			}
			if want, ok := expected[row.id]; ok && want != value {
				failures = append(failures, capdomain.Failure{AccountID: row.id, Kind: "domain_mismatch", Detail: fmt.Sprintf("落库域 %s 与推导域 %s 不一致", row.domain, want)})
			}
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return nil, errors.New("核验扫描 keyset 游标未前进，终止避免死循环")
		}
		after = last
	}
	if finalized {
		for _, instance := range sealedInstances {
			sourceDomain, ok := sealedStoredByID[instance.sourceID]
			if !ok {
				failures = append(failures, capdomain.Failure{AccountID: instance.id, Kind: "instance_source_missing", Detail: "authorization_instance_source_account_id 指向的源账户行不存在: " + instance.sourceID})
				continue
			}
			if sourceDomain != instance.domain {
				failures = append(failures, capdomain.Failure{AccountID: instance.id, Kind: "instance_domain_mismatch", Detail: fmt.Sprintf("实例落库域 %s 与源落库域 %s 不一致", instance.domain, sourceDomain)})
			}
		}
	}
	if !finalized && seen != len(accounts) {
		return nil, fmt.Errorf("核验读取 %d 行，与扫描计数 %d 不一致（停写窗口被并发写入）", seen, len(accounts))
	}
	return failures, nil
}

// finalizeCompletedStage 是完成阶段值：M1 → m1_complete，M2 → m2_finalized。
func finalizeCompletedStage(target capdomain.Target) capdomain.Stage {
	if target == capdomain.TargetM2Cutover {
		return capdomain.StageM2Finalized
	}
	return capdomain.StageM1Complete
}

// Run 执行一次迁移命令。语义：
//  1. 读取阶段记录并按状态机判定（损坏/拒绝/已完成直接返回，零写入）；
//  2. 条件更新领取阶段（首次 INSERT 或跃迁/续约 UPDATE），竞争失败即拒绝；
//  3. 加列并校验形状（schema 包专用入口，只承载 capability_domain 一列）；
//  4. 只读扫描推导（含实例继承）与声明裁剪计划（ModesOK 行按落域 lane 集
//     保序去重）；
//  5. 失败清单非空 → 返回 Ready=false 报告（已提交批次保留，修复后同阶段
//     重跑），不写完成阶段；
//  6. 分批写入（赋域 + 声明裁剪同语句）→ 最终核验与完成阶段记录同事务提交。
func Run(ctx context.Context, db *sql.DB, options Options) (Report, error) {
	report := Report{
		Driver:          capdomain.DialectPostgres.String(),
		Target:          string(options.Target),
		RuleVersion:     capdomain.RuleVersion,
		DomainsAssigned: DomainCounts{},
		Failures:        []capdomain.Failure{},
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	record, present, err := capdomain.ReadRecord(ctx, db, capdomain.DialectPostgres)
	if err != nil {
		return report, err
	}
	current := capdomain.StageNone
	if present {
		report.Record = &record
		current = record.Stage
	}
	decision, next, reason := capdomain.PlanMigration(options.Target, current)
	switch decision {
	case capdomain.DecisionReject:
		return report, fmt.Errorf("迁移被拒绝：%s", reason)
	case capdomain.DecisionAlreadyComplete:
		report.AlreadyComplete = true
		report.CompletedStage = current
		report.CompletedAt = record.UpdatedAt
		return report, nil
	}

	// 领取阶段（一次客户端命令一个阶段跃迁；条件更新保证单执行者）。
	claimed := false
	if current == capdomain.StageNone {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		claimed, err = capdomain.ClaimNewStage(ctx, tx, capdomain.DialectPostgres, next, now)
		if err != nil {
			_ = tx.Rollback()
			return report, err
		}
		if err := tx.Commit(); err != nil {
			return report, err
		}
	} else {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		claimed, err = capdomain.TransitionStage(ctx, tx, capdomain.DialectPostgres, current, next, now)
		if err != nil {
			_ = tx.Rollback()
			return report, err
		}
		if err := tx.Commit(); err != nil {
			return report, err
		}
	}
	if !claimed {
		latest, _, readErr := capdomain.ReadRecord(ctx, db, capdomain.DialectPostgres)
		if readErr != nil {
			return report, readErr
		}
		return report, fmt.Errorf("检测到并行迁移执行者：阶段记录已被推进为 %q，本次命令放弃执行（并行迁移进程不得同时运行）", latest.Stage)
	}
	report.Record = &capdomain.Record{Stage: next, RuleVersion: capdomain.RuleVersion, UpdatedAt: now.UTC().Format(time.RFC3339)}
	report.ColumnEnsured = true

	// 加列并校验形状（缺列则加；列存在则四值形状校验）。
	if err := schema.EnsurePostgresBusinessCapabilityDomainColumn(ctx, db); err != nil {
		return report, err
	}

	// 只读扫描推导（含软删除、实例继承两遍法）。
	accounts, failures, err := scanAccounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	failures = append(failures, resolveInstances(accounts)...)
	report.AccountsTotal = len(accounts)
	for _, item := range accounts {
		if item.domain.Valid() {
			report.DomainsAssigned[string(item.domain)]++
		}
	}
	// 声明裁剪计划（严格不变式：账户声明集必须 ⊆ 落域 lane 集，不存在任何
	// 跨域保留状态）。
	report.Trimmed = planDeclarationTrims(accounts)
	if len(failures) > 0 {
		sortFailures(failures)
		report.Failures = failures
		return report, nil
	}

	// 分批短事务写入（赋域 + 声明裁剪）。
	updated, err := writeDomains(ctx, db, options.Secret, accounts, batchSize)
	if err != nil {
		return report, err
	}
	report.UpdatedRows = updated

	// 最终核验与完成阶段记录同事务：核验失败或阶段条件更新失败都保留
	// running，不提前标记完成。
	completed := finalizeCompletedStage(options.Target)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	verifyFailures, err := verifyDomains(ctx, tx, accounts, false)
	if err != nil {
		return report, err
	}
	if len(verifyFailures) > 0 {
		sortFailures(verifyFailures)
		report.Failures = verifyFailures
		return report, nil
	}
	claimed, err = capdomain.TransitionStage(ctx, tx, capdomain.DialectPostgres, next, completed, now)
	if err != nil {
		return report, err
	}
	if !claimed {
		return report, errors.New("完成阶段条件更新未命中：阶段记录已被并行执行者改变，保留 running")
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	report.CompletedStage = completed
	report.CompletedAt = now.UTC().Format(time.RFC3339)
	return report, nil
}

// VerifyReport 是 verify 命令的 JSON 报告。
type VerifyReport struct {
	Driver           string              `json:"driver"`
	Check            string              `json:"check"`
	Target           string              `json:"target"`
	RuleVersion      int                 `json:"ruleVersion"`
	Record           *capdomain.Record   `json:"record"`
	ColumnPresent    bool                `json:"columnPresent"`
	ReconcileSkipped bool                `json:"reconcileSkipped"`
	AccountsTotal    int                 `json:"accountsTotal"`
	PendingMigration int                 `json:"pendingMigration"`
	AlreadyMatching  int                 `json:"alreadyMatching"`
	DomainsExpected  DomainCounts        `json:"domainsExpected"`
	Failures         []capdomain.Failure `json:"failures"`
	ReconcileNote    string              `json:"reconcileNote,omitempty"`
}

// Ready 报告核验后置条件：失败清单清零（postflight 额外要求列存在）。
func (r VerifyReport) Ready() bool {
	return len(r.Failures) == 0
}

// CheckPreflight / CheckPostflight 是 --account-capability-domain-check 的取值。
const (
	CheckPreflight  = "preflight"
	CheckPostflight = "postflight"
)

// ValidCheck 校验 check 旗标取值。
func ValidCheck(check string) bool {
	return check == CheckPreflight || check == CheckPostflight
}

// Verify 执行只读核验。preflight 允许域列不存在（预计赋域 N 个账户是正常
// 结果）；postflight 要求列存在并核对全表域值合法、实例与源一致、阶段内
// 推导一致与失败清单清零；m2_finalized 后推导对账不适用，仍可执行 schema、
// 域值合法与实例同源一致检查。全程零写入。
func Verify(ctx context.Context, db *sql.DB, options Options) (VerifyReport, error) {
	report := VerifyReport{
		Driver:          capdomain.DialectPostgres.String(),
		Check:           options.Check,
		Target:          string(options.Target),
		RuleVersion:     capdomain.RuleVersion,
		DomainsExpected: DomainCounts{},
		Failures:        []capdomain.Failure{},
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	columnPresent, err := capabilityDomainColumnPresent(ctx, db)
	if err != nil {
		return report, err
	}
	report.ColumnPresent = columnPresent
	if options.Check == CheckPostflight && !columnPresent {
		return report, errors.New("postflight 核验要求 accounts.capability_domain 列存在；当前列缺失，请先执行迁移命令")
	}
	if columnPresent {
		if err := schema.VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err != nil {
			return report, err
		}
	}

	record, present, err := capdomain.ReadRecord(ctx, db, capdomain.DialectPostgres)
	if err != nil {
		return report, err
	}
	if present {
		report.Record = &record
	}

	accounts, failures, err := scanAccounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	failures = append(failures, resolveInstances(accounts)...)
	report.AccountsTotal = len(accounts)

	finalized := present && record.Stage == capdomain.StageM2Finalized
	if finalized {
		report.ReconcileSkipped = true
		if options.Check == CheckPostflight {
			report.ReconcileNote = "迁移已封存（m2_finalized）：推导对账不适用，仅核对 schema、域值合法与实例同源一致"
			// 封存后核验仍须执行计划承诺的域值合法与实例同源一致检查
			// （按落库现值核对，推导对账在 verifyDomains 内部跳过）。
			domainFailures, err := verifyDomains(ctx, db, accounts, true)
			if err != nil {
				return report, err
			}
			failures = append(failures, domainFailures...)
		} else {
			report.ReconcileNote = "迁移已封存（m2_finalized）：预检仅核对 schema 与扫描级失败，推导对账与核验不适用"
		}
	} else if options.Check == CheckPostflight {
		// postflight：核对落库域值与阶段内推导一致。
		domainFailures, err := verifyDomains(ctx, db, accounts, false)
		if err != nil {
			return report, err
		}
		failures = append(failures, domainFailures...)
	}

	if !finalized {
		for _, item := range accounts {
			if !item.domain.Valid() {
				continue
			}
			report.DomainsExpected[string(item.domain)]++
		}
		if columnPresent {
			pending, matching, countErr := countPendingMigration(ctx, db, accounts)
			if countErr != nil {
				return report, countErr
			}
			report.PendingMigration = pending
			report.AlreadyMatching = matching
		} else {
			report.PendingMigration = report.AccountsTotal
		}
	}
	sortFailures(failures)
	report.Failures = failures
	return report, nil
}

// countPendingMigration 统计「预计域 ≠ 现值」与「已一致」的行数（preflight
// 语义：差异只是待迁移项，不是失败）。
func countPendingMigration(ctx context.Context, db *sql.DB, accounts []scannedAccount) (pending, matching int, err error) {
	expected := make(map[string]capdomain.Domain, len(accounts))
	for _, item := range accounts {
		if item.domain.Valid() && !item.isInstance {
			expected[item.id] = item.domain
		}
	}
	table := `"juhe_business"."accounts"`
	after := ""
	batchSize := DefaultBatchSize
	for {
		query := fmt.Sprintf(`SELECT id, capability_domain FROM %s WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`,
			table)
		rows, err := db.QueryContext(ctx, query, after, batchSize)
		if err != nil {
			return 0, 0, fmt.Errorf("统计待迁移行失败: %w", err)
		}
		type row struct {
			id     string
			domain string
		}
		batch := []row{}
		for rows.Next() {
			var item row
			if err := rows.Scan(&item.id, &item.domain); err != nil {
				rows.Close()
				return 0, 0, err
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, 0, err
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if want, ok := expected[item.id]; ok {
				if string(want) == item.domain {
					matching++
				} else {
					pending++
				}
			}
		}
		if len(batch) < batchSize {
			break
		}
		after = batch[len(batch)-1].id
	}
	return pending, matching, nil
}

// capabilityDomainColumnPresent 判断域列是否已存在。
func capabilityDomainColumnPresent(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = 'capability_domain'`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// sortFailures 稳定排序失败清单（账户 ID → 分类），保证报告可回放。
func sortFailures(failures []capdomain.Failure) {
	sort.Slice(failures, func(i, j int) bool {
		if failures[i].AccountID != failures[j].AccountID {
			return failures[i].AccountID < failures[j].AccountID
		}
		return failures[i].Kind < failures[j].Kind
	})
}

// SealFreshInstall 是全新空库自动封存（PLAN §阶段记录与重跑边界「全新 M2
// 安装」与 §M2 启动门禁「全新空库自动封存」）：ensure-schema/bootstrap 在
// schema 确保后调用。触发条件=阶段记录不存在且 accounts 无任何行（含软删
// 除）；满足则按序执行空库 M1 → M2 cutover（空库无凭据可解密，Secret 留
// 空）写 m2_finalized。记录已存在直接跳过（幂等）；accounts 非空的库一律
// 不自动写记录（真实切换必须显式跑迁移命令）。返回是否本次写入封存记录。
func SealFreshInstall(ctx context.Context, db *sql.DB) (bool, error) {
	if _, present, err := capdomain.ReadRecord(ctx, db, capdomain.DialectPostgres); err != nil {
		return false, err
	} else if present {
		return false, nil
	}
	countQuery := `SELECT COUNT(*) FROM "juhe_business"."accounts"`
	var accounts int
	if err := db.QueryRowContext(ctx, countQuery).Scan(&accounts); err != nil {
		return false, fmt.Errorf("空库封存检查 accounts 行数失败: %w", err)
	}
	if accounts > 0 {
		return false, nil
	}
	m1, err := Run(ctx, db, Options{Target: capdomain.TargetM1})
	if err != nil {
		return false, fmt.Errorf("空库自动封存 M1 阶段失败: %w", err)
	}
	if !m1.Ready() {
		return false, fmt.Errorf("空库自动封存 M1 阶段未就绪：失败清单 %d 项", len(m1.Failures))
	}
	m2, err := Run(ctx, db, Options{Target: capdomain.TargetM2Cutover})
	if err != nil {
		return false, fmt.Errorf("空库自动封存 M2 切换失败: %w", err)
	}
	if !m2.Ready() {
		return false, fmt.Errorf("空库自动封存 M2 切换未就绪：失败清单 %d 项", len(m2.Failures))
	}
	return true, nil
}
