// 存量迁移执行面：只读扫描 → 内存规划 → （正式执行）单事务条件写入 → 只读
// 后检核验。分类规则全部在 classify.go 纯函数内；本文件只负责行投影、凭据
// 信封解封/再封（JUHE_AI_SECRET，先例 legacybusinessimport decodeCredit）与
// DB 写入纪律：
//   - dry-run（Plan）全程零 DB 写入；manifest 是唯一产物（CLI 落盘）；
//   - 正式执行（Apply）要求 CLI 层三确认门禁（gateway/jobs 停写 + 备份确认），
//     单个业务库事务内对每行做「旧值相等」条件更新（WHERE base_url = 旧值 /
//     credentials_encrypted = 旧密文），行被并发改写计失败不覆盖；
//   - 幂等可重跑：迁移后全部行分类为 unchanged/review，重跑零写入；
//   - 只写 base_url / credentials_encrypted 两列，不触碰 config_revision、
//     updated_at、credential_fingerprint / credential_mask（指纹口径是
//     source secret 的 sha256，见 gateway accounts write；凭据内仅 base_url
//     变更不影响指纹与掩码）。
package upstreambaseurlmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// DefaultBatchSize 是分批只读扫描的批大小（schedprefmigration 同纪律：批间
// 完整读取后再取下一批，扫描全程不开写事务）。
const DefaultBatchSize = 500

// BalanceImpactNote 是随 manifest 固定输出的余额影响披露：base_url 参与
// 余额凭据逻辑身份，凭据内 base_url 变更会使既有余额快照立即失效并在下一轮
// 查询自动重算（设计 :58-80 / accountbalance input_digest.go 的消费契约）。
const BalanceImpactNote = "base_url 参与余额凭据逻辑身份（backend-go/shared/platform/accountbalance/input_digest.go CredentialBalanceIdentity）：credentials 内 base_url 变更会改变 BalanceInputDigest，既有余额快照立即失效，下一轮余额查询（jobs J2，≤5 分钟间隔）自动重算，无需人工清理快照；account_connections.base_url 不参与余额身份，仅影响上游请求 URL。"

// Options 是扫描/执行/核验的注入参数。
type Options struct {
	Now       time.Time
	BatchSize int    // <=0 取 DefaultBatchSize
	Secret    string // 凭据信封密钥（JUHE_AI_SECRET）；仅 Apply 消费
}

// EntryKind 区分两类迁移对象。
type EntryKind string

const (
	KindConnection EntryKind = "account_connection"
	KindCredential EntryKind = "account_credential"
)

// Change 是一条改写/回填（apply 落库；dry-run 记录为计划）。
type Change struct {
	Kind      EntryKind `json:"kind"`
	ID        string    `json:"id"`
	AccountID string    `json:"accountId,omitempty"`
	Preset    string    `json:"preset,omitempty"`
	Family    Family    `json:"family"`
	Reason    string    `json:"reason"`
	OldValue  string    `json:"oldValue"`
	NewValue  string    `json:"newValue"`
}

// ReviewEntry 是人工复核桶条目（不改数据）。
type ReviewEntry struct {
	Kind      EntryKind `json:"kind"`
	ID        string    `json:"id"`
	AccountID string    `json:"accountId,omitempty"`
	Preset    string    `json:"preset,omitempty"`
	Family    Family    `json:"family"`
	Value     string    `json:"value"`
	Reason    string    `json:"reason"`
}

// Counts 是一 类对象的分类计数。
type Counts struct {
	Total     int `json:"total"`
	Unchanged int `json:"unchanged"`
	Rewritten int `json:"rewritten"`
	Filled    int `json:"filled"`
	Review    int `json:"review"`
}

// Failure 是 apply 阶段的结构性失败（条件更新未命中等）；分类层不产生的
// 人工复核走 ReviewEntry，两者不混用。
type Failure struct {
	Kind   EntryKind `json:"kind"`
	ID     string    `json:"id"`
	Reason string    `json:"reason"`
	Detail string    `json:"detail"`
}

// Manifest 是 dry-run 与 apply 共用的报告（--manifest-out 落盘 + stdout 摘要）。
type Manifest struct {
	Mode              string        `json:"mode"` // dry-run | apply
	RuleVersion       int           `json:"ruleVersion"`
	GeneratedAt       string        `json:"generatedAt"`
	Connections       Counts        `json:"connections"`
	Credentials       Counts        `json:"credentials"`
	Changes           []Change      `json:"changes"`
	Review            []ReviewEntry `json:"review"`
	Failures          []Failure     `json:"failures"`
	BalanceImpactNote string        `json:"balanceImpactNote"`
	Notes             []string      `json:"notes"`
}

// Pending 返回 apply/后检视角的待迁移行数（rewrite + fill；review 属设计内
// 不改数据，不计入未就绪）。
func (m *Manifest) Pending() int {
	return m.Connections.Rewritten + m.Connections.Filled + m.Credentials.Rewritten + m.Credentials.Filled
}

// VerifyReport 是 postflight 只读核验报告。
type VerifyReport struct {
	RuleVersion        int           `json:"ruleVersion"`
	GeneratedAt        string        `json:"generatedAt"`
	PendingConnections int           `json:"pendingConnections"`
	PendingCredentials int           `json:"pendingCredentials"`
	Connections        Counts        `json:"connections"`
	Credentials        Counts        `json:"credentials"`
	Review             []ReviewEntry `json:"review"`
	Failures           []Failure     `json:"failures"`
}

// Ready 报告后置条件：无待迁移残留（review 桶不影响就绪）。
func (r VerifyReport) Ready() bool {
	return r.PendingConnections == 0 && r.PendingCredentials == 0 && len(r.Failures) == 0
}

// ── 行投影 ───────────────────────────────────────────────────────────────

// connectionRow 是 account_connections 的扫描投影。
type connectionRow struct {
	ID        string
	AccountID string
	Preset    string
	BaseURL   string
}

// accountRow 是 accounts 的扫描投影（Envelope = credentials_encrypted 原文）。
type accountRow struct {
	ID       string
	Vendor   string
	Protocol string
	Envelope string
}

// scanConnections 分批只读扫描全部连接行。
func scanConnections(ctx context.Context, db *sql.DB, batchSize int) ([]connectionRow, error) {
	query := `SELECT id, account_id, connection_preset_code, base_url FROM "juhe_business"."account_connections"
		WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`
	after := ""
	var rows []connectionRow
	for {
		batch, err := db.QueryContext(ctx, query, after, batchSize)
		if err != nil {
			return nil, fmt.Errorf("扫描 account_connections 失败: %w", err)
		}
		batchRows := []connectionRow{}
		for batch.Next() {
			var row connectionRow
			if err := batch.Scan(&row.ID, &row.AccountID, &row.Preset, &row.BaseURL); err != nil {
				batch.Close()
				return nil, fmt.Errorf("读取 account_connections 行失败: %w", err)
			}
			batchRows = append(batchRows, row)
		}
		if err := batch.Err(); err != nil {
			batch.Close()
			return nil, fmt.Errorf("遍历 account_connections 失败: %w", err)
		}
		batch.Close()
		if len(batchRows) == 0 {
			break
		}
		rows = append(rows, batchRows...)
		if len(batchRows) < batchSize {
			break
		}
		last := batchRows[len(batchRows)-1].ID
		if last == after {
			return nil, errors.New("account_connections 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return rows, nil
}

// scanAccounts 分批只读扫描未删除账户（软删除账户的凭据不再被运行时消费，
// 不进迁移面；连接行无删除语义，全量扫描）。
func scanAccounts(ctx context.Context, db *sql.DB, batchSize int) ([]accountRow, error) {
	query := `SELECT id, COALESCE(vendor_code, ''), protocol_code, credentials_encrypted FROM "juhe_business"."accounts"
		WHERE deleted_at IS NULL AND ($1 = '' OR id > $1) ORDER BY id LIMIT $2`
	after := ""
	var rows []accountRow
	for {
		batch, err := db.QueryContext(ctx, query, after, batchSize)
		if err != nil {
			return nil, fmt.Errorf("扫描 accounts 失败: %w", err)
		}
		batchRows := []accountRow{}
		for batch.Next() {
			var row accountRow
			if err := batch.Scan(&row.ID, &row.Vendor, &row.Protocol, &row.Envelope); err != nil {
				batch.Close()
				return nil, fmt.Errorf("读取 accounts 行失败: %w", err)
			}
			batchRows = append(batchRows, row)
		}
		if err := batch.Err(); err != nil {
			batch.Close()
			return nil, fmt.Errorf("遍历 accounts 失败: %w", err)
		}
		batch.Close()
		if len(batchRows) == 0 {
			break
		}
		rows = append(rows, batchRows...)
		if len(batchRows) < batchSize {
			break
		}
		last := batchRows[len(batchRows)-1].ID
		if last == after {
			return nil, errors.New("accounts 扫描 keyset 游标未前进（id 为空或非递增），终止避免死循环")
		}
		after = last
	}
	return rows, nil
}

// ── 规划（纯内存；dry-run 与 apply 共用） ────────────────────────────────

// plannedCredential 是凭据回填的写计划（解密产物仅存在于计划内，不落日志）。
type plannedCredential struct {
	id          string
	oldEnvelope string
	newEnvelope string
	oldValue    string
	newValue    string
	presetHint  string
	family      Family
	reason      ChangeReason
}

// planOutput 汇总规划结果。
type planOutput struct {
	manifest    Manifest
	credentials []plannedCredential
}

// planRows 是 planFromRows 的输入行集（DB 层与测试共用的纯内存入口）。
type planRows struct {
	Connections []connectionRow
	Accounts    []accountRow
}

// planFromRows 对行集执行分类规划（零 I/O）。凭据信封用 secret 解封；解封
// 失败 / 载荷非对象 / base_url 键缺失（oauth 等无 base_url 语义的凭据）不中
// 断：分别进复核桶 / 原样跳过（键缺失 = 无可迁移对象，不计任何计数变更）。
func planFromRows(rows planRows, secret string, now time.Time) *planOutput {
	out := &planOutput{manifest: Manifest{
		RuleVersion:       RuleVersion,
		GeneratedAt:       now.UTC().Format(time.RFC3339),
		Changes:           []Change{},
		Review:            []ReviewEntry{},
		Failures:          []Failure{},
		BalanceImpactNote: BalanceImpactNote,
		Notes: []string{
			"分类规则：空值按 registry 默认回填（generic 三 preset 无默认 → 复核桶）；裸 origin 补版本根；已合规（版本根后缀 / registry 默认值）不变；其余非空值进人工复核桶，不改数据。",
			"家族判定：连接行按 connection_preset_code；账户凭据按 vendor_code + protocol_code 合成 preset 提示（glm 的 openai 协议账户按 general 档案根 /api/paas/v4，coding 连接根由各连接行 preset 承载）。",
			"qwen 口径：base_url 语义是 origin（v2 R6）——/compatible-mode/v1 后缀剥回 origin，裸根不变，空值不变，其余复核。",
			"volcengine_openspeech 不在版本根词表内：registry 默认值原样保留，其余值复核。",
			"凭据键缺失（oauth 等无 base_url 语义的凭据）原样跳过，不注入 base_url 键。",
			"写入纪律：只写 account_connections.base_url 与 accounts.credentials_encrypted 两列（条件更新=旧值），不触碰 config_revision / updated_at / credential_fingerprint / credential_mask；解密失败的账户进复核桶不中断。",
			"幂等：对已迁移值重跑必然全部 unchanged/review，零写入。",
		},
	}}
	for _, row := range rows.Connections {
		classify := ClassifyBaseURL(row.Preset, contracts.PresetDefaultBaseURL(row.Preset), row.BaseURL)
		counts := &out.manifest.Connections
		counts.Total++
		switch classify.Disposition {
		case DispositionUnchanged:
			counts.Unchanged++
		case DispositionRewrite, DispositionFill:
			out.manifest.Changes = append(out.manifest.Changes, Change{
				Kind: KindConnection, ID: row.ID, AccountID: row.AccountID, Preset: row.Preset,
				Family: classify.Family, Reason: string(classify.ChangeReason),
				OldValue: row.BaseURL, NewValue: classify.NewValue,
			})
			if classify.Disposition == DispositionRewrite {
				counts.Rewritten++
			} else {
				counts.Filled++
			}
		default:
			counts.Review++
			out.manifest.Review = append(out.manifest.Review, ReviewEntry{
				Kind: KindConnection, ID: row.ID, AccountID: row.AccountID, Preset: row.Preset,
				Family: classify.Family, Value: row.BaseURL, Reason: string(classify.ReviewReason),
			})
		}
	}
	for _, row := range rows.Accounts {
		presetHint := AccountPresetHint(row.Vendor, row.Protocol)
		classify := classifyCredentialEnvelope(presetHint, secret, row.Envelope)
		counts := &out.manifest.Credentials
		counts.Total++
		switch classify.Disposition {
		case dispositionCredentialSkip:
			counts.Unchanged++
		case DispositionUnchanged:
			counts.Unchanged++
		case DispositionRewrite, DispositionFill:
			if classify.newEnvelope == "" {
				// 分类判定改写但信封未生成：内部错误按失败披露，不静默跳过。
				counts.Review++
				out.manifest.Failures = append(out.manifest.Failures, Failure{
					Kind: KindCredential, ID: row.ID, Reason: "reencrypt_failed",
					Detail: "凭据再加密失败：" + classify.reviewDetail,
				})
				continue
			}
			out.credentials = append(out.credentials, plannedCredential{
				id: row.ID, oldEnvelope: row.Envelope, newEnvelope: classify.newEnvelope,
				oldValue: classify.oldValue, newValue: classify.NewValue,
				presetHint: presetHint, family: classify.Family,
				reason: classify.ChangeReason,
			})
			out.manifest.Changes = append(out.manifest.Changes, Change{
				Kind: KindCredential, ID: row.ID, Preset: presetHint,
				Family: classify.Family, Reason: string(classify.ChangeReason),
				OldValue: classify.oldValue, NewValue: classify.NewValue,
			})
			if classify.Disposition == DispositionRewrite {
				counts.Rewritten++
			} else {
				counts.Filled++
			}
		default:
			counts.Review++
			out.manifest.Review = append(out.manifest.Review, ReviewEntry{
				Kind: KindCredential, ID: row.ID, Preset: presetHint,
				Family: classify.Family, Value: classify.oldValue, Reason: string(classify.ReviewReason),
			})
		}
	}
	sort.Slice(out.manifest.Changes, func(i, j int) bool {
		if out.manifest.Changes[i].Kind != out.manifest.Changes[j].Kind {
			return out.manifest.Changes[i].Kind < out.manifest.Changes[j].Kind
		}
		return out.manifest.Changes[i].ID < out.manifest.Changes[j].ID
	})
	sort.Slice(out.manifest.Review, func(i, j int) bool {
		if out.manifest.Review[i].Kind != out.manifest.Review[j].Kind {
			return out.manifest.Review[i].Kind < out.manifest.Review[j].Kind
		}
		return out.manifest.Review[i].ID < out.manifest.Review[j].ID
	})
	return out
}

// dispositionCredentialSkip 表示凭据内无 base_url 键（无可迁移对象），按
// unchanged 计数、零改动。
const dispositionCredentialSkip Disposition = "credential_skip"

// credentialClassification 扩展 Classification：携带凭据再封产物与跳过态。
type credentialClassification struct {
	Classification
	newEnvelope  string
	oldValue     string
	reviewDetail string
}

// classifyCredentialEnvelope 解封凭据并对其中 base_url 键执行分类（不改动
// 其他键；再封用 json.RawMessage 保留未知键的原始字节）。
func classifyCredentialEnvelope(presetHint, secret, envelope string) credentialClassification {
	result := credentialClassification{}
	if strings.TrimSpace(envelope) == "" {
		result.Classification = Classification{Family: FamilyForPreset(presetHint), Disposition: DispositionUnchanged}
		return result
	}
	var payload map[string]json.RawMessage
	if err := accountcrypto.DecryptJSON(secret, envelope, &payload); err != nil {
		result.Classification = Classification{Family: FamilyForPreset(presetHint), Disposition: DispositionReview, ReviewReason: ReviewDecryptFailed}
		result.reviewDetail = err.Error()
		return result
	}
	raw, present := payload["base_url"]
	if !present {
		result.Classification = Classification{Family: FamilyForPreset(presetHint), Disposition: dispositionCredentialSkip}
		return result
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		result.Classification = Classification{Family: FamilyForPreset(presetHint), Disposition: DispositionReview, ReviewReason: ReviewInvalidURL}
		result.reviewDetail = "base_url 键非字符串: " + err.Error()
		result.oldValue = string(raw)
		return result
	}
	result.oldValue = value
	result.Classification = ClassifyBaseURL(presetHint, contracts.PresetDefaultBaseURL(presetHint), value)
	switch result.Disposition {
	case DispositionRewrite, DispositionFill:
		encoded, err := json.Marshal(result.NewValue)
		if err != nil {
			result.Disposition = DispositionReview
			result.ReviewReason = ReviewInvalidURL
			result.reviewDetail = err.Error()
			result.NewValue = ""
			return result
		}
		payload["base_url"] = encoded
		sealed, err := accountcrypto.EncryptJSON(secret, payload)
		if err != nil {
			result.Disposition = DispositionReview
			result.ReviewReason = ReviewInvalidURL
			result.reviewDetail = err.Error()
			result.NewValue = ""
			return result
		}
		result.newEnvelope = sealed
	}
	return result
}

// ── DB 命令入口 ──────────────────────────────────────────────────────────

// Plan 执行只读扫描与规划（dry-run；全程零 DB 写入）。
func Plan(ctx context.Context, db *sql.DB, options Options) (Manifest, error) {
	rows, err := scan(ctx, db, options.BatchSize)
	if err != nil {
		return Manifest{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	manifest := planFromRows(rows, options.Secret, now).manifest
	manifest.Mode = "dry-run"
	return manifest, nil
}

// Apply 执行正式迁移：只读扫描与规划 → 单业务库事务内按「旧值相等」条件
// 写入 → 返回执行后 manifest。条件更新未命中（停写窗口被改写）计失败，不覆
// 盖他行；失败清单非空由 CLI 层映射未就绪退出码。
func Apply(ctx context.Context, db *sql.DB, options Options) (Manifest, error) {
	rows, err := scan(ctx, db, options.BatchSize)
	if err != nil {
		return Manifest{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	plan := planFromRows(rows, options.Secret, now)
	manifest := plan.manifest
	manifest.Mode = "apply"

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("开启迁移事务失败: %w", err)
	}
	defer tx.Rollback()

	updateConnection := `UPDATE "juhe_business"."account_connections" SET base_url = $1
		WHERE id = $2 AND base_url = $3`
	for _, change := range manifest.Changes {
		if change.Kind != KindConnection {
			continue
		}
		result, err := tx.ExecContext(ctx, updateConnection, change.NewValue, change.ID, change.OldValue)
		if err != nil {
			return Manifest{}, fmt.Errorf("回填连接 %s base_url 失败: %w", change.ID, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Manifest{}, fmt.Errorf("读取连接 %s 回填行数失败: %w", change.ID, err)
		} else if affected != 1 {
			manifest.Failures = append(manifest.Failures, Failure{
				Kind: KindConnection, ID: change.ID, Reason: "conditional_update_miss",
				Detail: "base_url 已非规划时旧值（停写窗口被改写），拒绝覆盖",
			})
		}
	}
	updateCredential := `UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1
		WHERE id = $2 AND credentials_encrypted = $3`
	for _, credential := range plan.credentials {
		result, err := tx.ExecContext(ctx, updateCredential, credential.newEnvelope, credential.id, credential.oldEnvelope)
		if err != nil {
			return Manifest{}, fmt.Errorf("回填账户 %s 凭据 base_url 失败: %w", credential.id, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Manifest{}, fmt.Errorf("读取账户 %s 回填行数失败: %w", credential.id, err)
		} else if affected != 1 {
			manifest.Failures = append(manifest.Failures, Failure{
				Kind: KindCredential, ID: credential.id, Reason: "conditional_update_miss",
				Detail: "credentials_encrypted 已非规划时旧密文（停写窗口被改写），拒绝覆盖",
			})
		}
	}
	if err := tx.Commit(); err != nil {
		return Manifest{}, fmt.Errorf("提交迁移事务失败: %w", err)
	}
	sort.Slice(manifest.Failures, func(i, j int) bool {
		if manifest.Failures[i].Kind != manifest.Failures[j].Kind {
			return manifest.Failures[i].Kind < manifest.Failures[j].Kind
		}
		return manifest.Failures[i].ID < manifest.Failures[j].ID
	})
	return manifest, nil
}

// Verify 执行只读后检核验（postflight）：重新扫描分类，待迁移残留（rewrite
// + fill）必须为 0；复核桶与失败清单随报告透出，不影响就绪判定之外的语义。
func Verify(ctx context.Context, db *sql.DB, options Options) (VerifyReport, error) {
	rows, err := scan(ctx, db, options.BatchSize)
	if err != nil {
		return VerifyReport{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	manifest := planFromRows(rows, options.Secret, now).manifest
	report := VerifyReport{
		RuleVersion:        RuleVersion,
		GeneratedAt:        now.UTC().Format(time.RFC3339),
		PendingConnections: manifest.Connections.Rewritten + manifest.Connections.Filled,
		PendingCredentials: manifest.Credentials.Rewritten + manifest.Credentials.Filled,
		Connections:        manifest.Connections,
		Credentials:        manifest.Credentials,
		Review:             manifest.Review,
		Failures:           manifest.Failures,
	}
	return report, nil
}

// scan 汇总两路只读扫描。
func scan(ctx context.Context, db *sql.DB, batchSize int) (planRows, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	connections, err := scanConnections(ctx, db, batchSize)
	if err != nil {
		return planRows{}, err
	}
	accounts, err := scanAccounts(ctx, db, batchSize)
	if err != nil {
		return planRows{}, err
	}
	return planRows{Connections: connections, Accounts: accounts}, nil
}
