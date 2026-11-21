// 五域上游接口能力声明退役执行器（《AI账户上游接口能力设计》§3，revision 30
// capability-domain-declaration-retirement 步骤的库操作面）。与 revision 29
// ruleversion 升级执行器（ruleversion_upgrade.go）同构：凭据解密-改写-加密、
// Satisfied/Apply/Verify 三段式、失败清单 fail-closed（不猜、不静默跳过）。
// 边界：
//   - 只清洗五域（image/audio/video/embedding/rerank）账户（含软删除行，凭据
//     键扫描与连接行删除对 deleted_at 对称）：删除其加密凭据中的
//     supported_endpoint_modes 键（键不存在即跳过，其余凭据键原样保留）与
//     account_endpoint_modes 中五域账户的连接行（经 account_connections 关
//     联）；chat 域账户与连接一律不动。
//   - 无 DDL 纯数据步骤：不触碰表结构与 config_revision/dispatch_revision/
//     updated_at（停写窗口内无并发写，版本列无增量含义）。
//   - 回滚安全（设计 §3.5）：旧二进制读空声明 = 不受限，清洗后状态向前兼容，
//     因此不标 destructive。
//   - fresh 库语义（与 cmd 侧 sqlScalar 同口径）：accounts /
//     account_endpoint_modes / account_connections 表不存在（42P01）或 schema
//     不存在（3F000）按 0 行处理——「无表/空表」即「无可清洗数据」，视为已
//     满足。
//   - 解密失败行计入失败清单并阻断一切写入（先全量扫描，失败清单为空才进
//     写阶段），修复后同步骤重跑收敛；报告只含账户 ID、分类与脱敏明细，
//     不含任何凭据内容。
package capabilitydomain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// retirementDomainsSQL 是本步骤清洗的五域 capability_domain SQL IN 词表
// （chat 域账户一律不动；六值域词表由 shared contracts 与共享 capabilitydomain
// 包钉死，此处字面量与之逐值一致，TestRetirementDomainsCoverFiveDomains 钉住）。
const retirementDomainsSQL = `'image', 'audio', 'video', 'embedding', 'rerank'`

// retiredModeKey 是被退役的凭据键（历史字段名，现行配置面为
// account_endpoint_modes 连接行；共享包 ParseEndpointModes 同名键）。
const retiredModeKey = "supported_endpoint_modes"

// DeclarationRetirementOptions 是 revision 30 清洗/核验命令的注入参数。
type DeclarationRetirementOptions struct {
	Secret    string // 解密密钥（调用方已经 ResolveSecret 归一）
	BatchSize int    // <=0 取 DefaultBatchSize
}

// DeclarationRetirementReport 是 revision 30 步骤 Apply / Verify 的 JSON 报告。
// Apply 语义下三个计数字段是「本次清洗量」；Verify 语义下
// CredentialKeysRemoved / ConnectionRowsDeleted 保持 0，残留量由
// PendingCredentialKeys / PendingConnectionRows 表达（复核计数必须为 0）。
type DeclarationRetirementReport struct {
	Driver                string              `json:"driver"`
	ScannedAccounts       int                 `json:"scannedAccounts"`
	CredentialKeysRemoved int64               `json:"credentialKeysRemoved"`
	ConnectionRowsDeleted int64               `json:"connectionRowsDeleted"`
	PendingCredentialKeys int                 `json:"pendingCredentialKeys,omitempty"`
	PendingConnectionRows int                 `json:"pendingConnectionRows,omitempty"`
	Failures              []capdomain.Failure `json:"failures"`
}

// Ready 报告后置条件：失败清单清零，且 Verify 语义下两项复核计数为 0
// （Apply 语义下 pending 恒为 0，等价于失败清单清零）。
func (r DeclarationRetirementReport) Ready() bool {
	return len(r.Failures) == 0 && r.PendingCredentialKeys == 0 && r.PendingConnectionRows == 0
}

// isUndefinedRelationOrSchema 判定 PG 42P01（表/关系不存在）与 3F000
// （schema 不存在），fresh 库语义见包注释（与 cmd 侧 upgrade_steps 同口径，
// internal 包不得反向 import cmd，故此处独立实现）。
func isUndefinedRelationOrSchema(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "3F000")
}

// retirementAccount 是清洗扫描阶段的一行内存投影。
type retirementAccount struct {
	id            string
	credentials   map[string]any // 仅解密成功行携带
	hasKey        bool           // 凭据含 supported_endpoint_modes 键（待清洗）
	emptyEnvelope bool           // 空信封（{} / 空串 / null）：无自身凭据，无可清洗键
	decryptFailed bool
}

// scanRetirementAccounts 分批只读扫描全部五域账户（含软删除行——凭据键残留
// 与连接行残留对 deleted_at 对称清洗；keyset 按 id 前进，含 42P01/3F000
// fresh 库语义），解密凭据并标记 supported_endpoint_modes 键。每批先完整读
// 取（rows.Err + Close）再处理下一批，全程不开写事务。
func scanRetirementAccounts(ctx context.Context, db *sql.DB, secret string, batchSize int) ([]retirementAccount, []capdomain.Failure, error) {
	var (
		all      []retirementAccount
		failures []capdomain.Failure
	)
	after := ""
	for {
		// $1 复用（空串同时充当哨兵与下界，与 migration.go accountRowQuery
		// 同款写法）。
		rows, err := db.QueryContext(ctx, `SELECT id, credentials_encrypted FROM "juhe_business"."accounts"
			WHERE capability_domain IN (`+retirementDomainsSQL+`)
			  AND ($1 = '' OR id > $1)
			ORDER BY id
			LIMIT $2`, after, batchSize)
		if err != nil {
			if isUndefinedRelationOrSchema(err) {
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("revision 30 扫描五域账户失败: %w", err)
		}
		batch := []retirementAccount{}
		for rows.Next() {
			var (
				id          string
				credentials string
			)
			if err := rows.Scan(&id, &credentials); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("读取 revision 30 扫描行失败: %w", err)
			}
			item := retirementAccount{id: id}
			switch {
			case isEmptyCredentialsEnvelope(credentials):
				// 历史授权实例行可能落空信封（自身不持有凭据），无键可删，
				// 与 migration.go 同款宽松语义：不是解密损坏。
				item.emptyEnvelope = true
			case accountcrypto.DecryptJSON(secret, credentials, &item.credentials) != nil:
				item.decryptFailed = true
			default:
				_, item.hasKey = item.credentials[retiredModeKey]
			}
			batch = append(batch, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("遍历 revision 30 扫描 accounts 失败: %w", err)
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
			}
			all = append(all, item)
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1].id
		if last == after {
			return nil, nil, errors.New("revision 30 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return all, failures, nil
}

// rewriteRetirementCredentials 分批短事务回写清洗后的凭据（仅实际携带键的
// 行）：解密记录删除 supported_endpoint_modes 键、其余凭据键原样保留，重加密
// 回写；每条 UPDATE 携带五域条件（capability_domain IN 五域），停写窗口内被
// 并发漂移的行以 affected=0 暴露为失败，不静默覆盖。
func rewriteRetirementCredentials(ctx context.Context, db *sql.DB, secret string, accounts []retirementAccount, batchSize int) (int64, []capdomain.Failure, error) {
	updateSQL := `UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1
		WHERE id = $2 AND capability_domain IN (` + retirementDomainsSQL + `)`
	var updated int64
	failures := []capdomain.Failure{}
	for start := 0; start < len(accounts); start += batchSize {
		end := start + batchSize
		if end > len(accounts) {
			end = len(accounts)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return updated, failures, err
		}
		batchUpdated := int64(0)
		for _, item := range accounts[start:end] {
			if !item.hasKey {
				continue
			}
			delete(item.credentials, retiredModeKey)
			sealed, sealErr := accountcrypto.EncryptJSON(secret, item.credentials)
			if sealErr != nil {
				_ = tx.Rollback()
				return updated, failures, fmt.Errorf("重加密账户 %s 的清洗后凭据失败: %w", item.id, sealErr)
			}
			result, execErr := tx.ExecContext(ctx, updateSQL, sealed, item.id)
			if execErr != nil {
				_ = tx.Rollback()
				return updated, failures, fmt.Errorf("回写账户 %s 的清洗后凭据失败: %w", item.id, execErr)
			}
			affected, rowErr := result.RowsAffected()
			if rowErr != nil {
				_ = tx.Rollback()
				return updated, failures, fmt.Errorf("读取账户 %s 的更新行数失败: %w", item.id, rowErr)
			}
			if affected != 1 {
				failures = append(failures, capdomain.Failure{
					AccountID: item.id,
					Kind:      "retirement_rewrite_race",
					Detail:    "回写时账户已不是五域（停写窗口内 capability_domain 被并发写入），拒绝覆盖",
				})
				continue
			}
			batchUpdated += affected
		}
		if err := tx.Commit(); err != nil {
			return updated, failures, fmt.Errorf("提交 revision 30 凭据清洗批次失败: %w", err)
		}
		updated += batchUpdated
	}
	return updated, failures, nil
}

// deleteRetirementConnectionModes 删除 account_endpoint_modes 中五域账户的
// 连接行（经 account_connections 关联；不限定 deleted_at——软删除五域账户的
// 连接行同样是无声明面的残留）。fresh 库表不存在按 0 行处理。
func deleteRetirementConnectionModes(ctx context.Context, db *sql.DB) (int64, error) {
	result, err := db.ExecContext(ctx, `DELETE FROM "juhe_business"."account_endpoint_modes" AS em
		USING "juhe_business"."account_connections" AS ac, "juhe_business"."accounts" AS a
		WHERE em.connection_id = ac.id
		  AND ac.account_id = a.id
		  AND a.capability_domain IN (`+retirementDomainsSQL+`)`)
	if err != nil {
		if isUndefinedRelationOrSchema(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("删除五域账户连接行失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取五域连接行删除行数失败: %w", err)
	}
	return affected, nil
}

// countRetirementConnectionModes 复核 account_endpoint_modes 中五域账户连接行
// 残留数（全程零写入；fresh 库表不存在按 0 行）。
func countRetirementConnectionModes(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM "juhe_business"."account_endpoint_modes" AS em,
		"juhe_business"."account_connections" AS ac, "juhe_business"."accounts" AS a
		WHERE em.connection_id = ac.id
		  AND ac.account_id = a.id
		  AND a.capability_domain IN (`+retirementDomainsSQL+`)`).Scan(&count)
	if err != nil {
		if isUndefinedRelationOrSchema(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("复核五域账户连接行失败: %w", err)
	}
	return count, nil
}

// RetireCapabilityDeclarations 执行 revision 30 步骤的 Apply 语义：清洗全部
// 五域账户（含软删除行）的 supported_endpoint_modes 凭据键与
// account_endpoint_modes 五域连接行。语义：
//  1. 只读扫描 + 解密（含 fresh 库 42P01/3F000 空跑）：解密失败清单非空 →
//     返回 Ready=false，不写任何行（fail-closed，修复后重跑收敛）；
//  2. 分批短事务回写「实际携带键」的行（带五域条件防漂移，仅实际有变化的
//     行计数）；
//  3. 删除五域账户连接行（计数）；
//  4. 幂等：键不存在即跳过、连接行已删即 0 行，重复执行收敛于同一终态。
func RetireCapabilityDeclarations(ctx context.Context, db *sql.DB, options DeclarationRetirementOptions) (DeclarationRetirementReport, error) {
	report := DeclarationRetirementReport{
		Driver:   "postgres",
		Failures: []capdomain.Failure{},
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	accounts, failures, err := scanRetirementAccounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	report.ScannedAccounts = len(accounts)
	if len(failures) > 0 {
		sortFailures(failures)
		report.Failures = failures
		return report, nil
	}

	updated, writeFailures, err := rewriteRetirementCredentials(ctx, db, options.Secret, accounts, batchSize)
	if err != nil {
		return report, err
	}
	report.CredentialKeysRemoved = updated
	if len(writeFailures) > 0 {
		sortFailures(writeFailures)
		report.Failures = writeFailures
		return report, nil
	}

	deleted, err := deleteRetirementConnectionModes(ctx, db)
	if err != nil {
		return report, err
	}
	report.ConnectionRowsDeleted = deleted
	return report, nil
}

// VerifyCapabilityDeclarationRetirement 执行 revision 30 步骤的 Satisfied /
// Verify 语义（全程零写入）：全部五域账户（含软删除行）凭据无
// supported_endpoint_modes 键且 account_endpoint_modes 无五域账户连接行
// （fresh 库表不存在/为空视为满足）。报告输出扫描账户数与两项复核计数，
// 复核计数非 0 即 Ready=false。
func VerifyCapabilityDeclarationRetirement(ctx context.Context, db *sql.DB, options DeclarationRetirementOptions) (DeclarationRetirementReport, error) {
	report := DeclarationRetirementReport{
		Driver:   "postgres",
		Failures: []capdomain.Failure{},
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	accounts, failures, err := scanRetirementAccounts(ctx, db, options.Secret, batchSize)
	if err != nil {
		return report, err
	}
	report.ScannedAccounts = len(accounts)
	for _, item := range accounts {
		if !item.hasKey {
			continue
		}
		report.PendingCredentialKeys++
		failures = append(failures, capdomain.Failure{
			AccountID: item.id,
			Kind:      "retired_declaration_key_present",
			Detail:    "五域账户凭据仍携带 supported_endpoint_modes 键（revision 30 未清洗或清洗后回写）",
		})
	}
	if len(failures) > 0 {
		sortFailures(failures)
		report.Failures = failures
	}

	pendingRows, err := countRetirementConnectionModes(ctx, db)
	if err != nil {
		return report, err
	}
	report.PendingConnectionRows = pendingRows
	return report, nil
}
