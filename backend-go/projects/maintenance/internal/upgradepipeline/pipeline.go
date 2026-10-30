// Package upgradepipeline 实现 `maintenance upgrade` 的编排引擎
// （升级兼容与迁移账本设计 §5/§9）。本包只含与具体迁移无关的机制：步骤
// 契约、账本读写、报告与退出码、执行序列、advisory 锁、输出捕获。12 个具体
// 步骤的注册表在 cmd/juhe-ai-maintenance（upgrade_steps.go）——步骤 Apply
// 复用各一次性命令的既有内部函数，那些函数在 package main，不能被本包 import。
//
// P2 起结构收敛分相（§7.3）：加法相（步骤 10）→ 数据步骤（12）→ 破坏相
// （13）；rollback 判定为语句级——加法相失败 binary-only，破坏相开始执行后
// backup-required。
package upgradepipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// Target 是步骤的作用库（§8.3）。j3a/account_balance 落 juhe_jobs schema、
// j3b 落 juhe_j3b（单库多 schema 事实，§8.3 映射表）。
type Target string

const (
	TargetPrimary        Target = "primary"
	TargetJ3A            Target = "j3a"
	TargetJ3B            Target = "j3b"
	TargetAccountBalance Target = "account_balance"
)

// SchemaForTarget 返回 target 首次 Apply 前需要确保存在的 schema（§8.3：
// CREATE SCHEMA IF NOT EXISTS，已存在时 no-op 不改 owner）。primary 的 schema
// 由 EnsurePostgres 自建，返回空串。
func (t Target) SchemaForTarget() string {
	switch t {
	case TargetJ3A, TargetAccountBalance:
		return "juhe_jobs"
	case TargetJ3B:
		return "juhe_j3b"
	default:
		return ""
	}
}

// Detail 是步骤 Apply 的结果载荷，进入账本 detail_json 与报告。
type Detail map[string]any

// Step 是注册表条目契约（§8.1）。Satisfied 与 Verify 必须与 Apply 的达成
// 判据严格同源——实现上二者共同委托同一个 check 函数（§8.1 注释）。
type Step struct {
	Revision    int64
	ID          string
	Title       string
	Target      Target
	Destructive bool
	ImplVersion int
	// Satisfied 只看库内事实，独立于账本求值；为真时 upgrade 记账 adopted
	// 而不执行 Apply。
	Satisfied func(ctx context.Context, env Env) (bool, error)
	// Apply 必须幂等：重复执行零写入或写等值结果；中断后可从头重跑。
	Apply func(ctx context.Context, env Env) (Detail, error)
	// Verify 在 Apply 之后执行；不通过则 upgrade 停止（exit 3）。
	Verify func(ctx context.Context, env Env) error
}

// Env 是步骤执行环境（步骤内自开连接；一次性 CLI 允许连接churn）。
type Env struct {
	PrimaryDSN string
	Secret     string
}

// Checksum 按 §7.4：sha256(step_id|revision|target|title|destructive)。
// 实现体的漂移由 ImplVersion 检测（§7.4），checksum 不承载该职责。
func (s Step) Checksum() string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s|%d|%s|%s|%t", s.ID, s.Revision, string(s.Target), s.Title, s.Destructive)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// ---------- 报告（§9.3） ----------

type binaryIdentity struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	SchemaRevision int64  `json:"schemaRevision"`
}

type databaseState struct {
	SchemaRevision int64  `json:"schemaRevision"`
	AppliedSteps   int64  `json:"appliedSteps"`
	Digest         string `json:"schemaDigest,omitempty"`
}

type pendingStep struct {
	Revision    int64  `json:"revision"`
	StepID      string `json:"stepId"`
	Title       string `json:"title"`
	Target      string `json:"target"`
	Destructive bool   `json:"destructive"`
	Status      string `json:"status"` // absent | running | outdated-impl
}

type adoptStep struct {
	Revision int64  `json:"revision"`
	StepID   string `json:"stepId"`
	Reason   string `json:"reason"`
}

type executedStep struct {
	Revision   int64  `json:"revision"`
	Status     string `json:"status"` // applied | adopted | failed
	DurationMs int64  `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
}

type postflightCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

type estimateInfo struct {
	ApplySteps                  int `json:"applySteps"`
	ApplyUpperBoundMinutes      int `json:"applyUpperBoundMinutes"`
	StructuralUpperBoundMinutes int `json:"structuralUpperBoundMinutes"`
	TotalUpperBoundMinutes      int `json:"totalUpperBoundMinutes"`
}

type planInfo struct {
	Pending                  []pendingStep `json:"pending"`
	Adopt                    []adoptStep   `json:"adopt"`
	DestructiveDDLStatements int           `json:"destructiveDDLStatements"`
}

type postflightInfo struct {
	OK     bool              `json:"ok"`
	Checks []postflightCheck `json:"checks"`
}

// Report 是 stdout 单行 JSON（§9.3）。字段存在性由 omitempty 与场景装配共同
// 保证；连接不可达/旗标解析失败路径不产出报告（命令层 stderr + exit）。
type Report struct {
	Driver     string          `json:"driver"`
	Binary     binaryIdentity  `json:"binary"`
	Database   databaseState   `json:"database"`
	Plan       planInfo        `json:"plan"`
	Estimate   estimateInfo    `json:"estimate"`
	Executed   []executedStep  `json:"executed,omitempty"`
	Postflight *postflightInfo `json:"postflight,omitempty"`
	Rollback   string          `json:"rollback"`
	Result     string          `json:"result"`
}

// Result 取值与退出码映射（§9.2/§9.3）：planned/up-to-date/upgraded→0，
// blocked→3，failed→1 或 3。
const (
	ResultPlanned  = "planned"
	ResultUpToDate = "up-to-date"
	ResultUpgraded = "upgraded"
	ResultBlocked  = "blocked"
	ResultFailed   = "failed"
)

// Rollback 等级（§14）：本次运行已发生破坏性写 → backup-required。
const (
	RollbackBinaryOnly     = "binary-only"
	RollbackBackupRequired = "backup-required"
)

// ---------- 退出码（§9.2） ----------

const (
	ExitOK             = 0
	ExitRuntimeFailure = 1
	ExitUsage          = 2
	ExitNeedsHuman     = 3
)

// ---------- 选项与运行 ----------

type Options struct {
	PrimaryDSN         string
	Secret             string
	DryRun             bool
	StopWriteConfirmed bool
	BackupConfirmed    bool
	StepTimeout        time.Duration // 钳制 1m..4h（命令层钳制后传入）
	AppVersion         string
	Now                func() time.Time // 测试注入；nil 用 time.Now
}

// outcome 是 Run 的返回：报告与 stderr 摘要恒备（连接不可达时 report 为 nil，
// 命令层只输出 stderr 摘要），exitCode 按 §9.2。
type outcome struct {
	ExitCode int
	Report   *Report
	Stderr   string
}

// Run 执行 §9.4 序列（P1 形态：EnsureAll 单次）。
func Run(ctx context.Context, opts Options, steps []Step) outcome {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	steps = sortStepsByRevision(steps)
	if err := validateRegistry(steps); err != nil {
		return outcome{ExitCode: ExitRuntimeFailure, Stderr: err.Error()}
	}
	report := &Report{
		Driver: "postgres",
		Binary: binaryIdentity{
			Version:        opts.AppVersion,
			SchemaRevision: int64(contracts.SchemaRevision),
		},
		Plan: planInfo{
			Pending:                  []pendingStep{},
			Adopt:                    []adoptStep{},
			DestructiveDDLStatements: schema.PostgresDestructiveStatementCount(),
		},
		Estimate: estimateInfo{StructuralUpperBoundMinutes: 60},
		Rollback: RollbackBinaryOnly,
	}
	fail := func(result string, code int, message string, withReport bool) outcome {
		report.Result = result
		out := outcome{ExitCode: code, Stderr: message}
		if withReport {
			out.Report = report
		}
		return out
	}

	// 2. 连接 primary（不可达：无报告，stderr 摘要 + exit 1）。
	db, err := sql.Open("pgx", opts.PrimaryDSN)
	if err == nil {
		err = db.PingContext(ctx)
	}
	if err != nil {
		return outcome{ExitCode: ExitRuntimeFailure,
			Stderr: fmt.Sprintf("主库连接不可达: %v", err)}
	}
	defer db.Close()

	// 3. advisory 锁（§9.5）：会话级，全程持有；2s 快失败。
	unlock, err := acquireUpgradeLock(ctx, db)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	defer unlock()

	// 4. 读账本（表或行缺失按 0 处理；账本 DDL 的创建延迟到确认门禁之后——
	// §9.4-7 dry-run 与缺确认路径全程零写入）。
	state, err := ReadSchemaState(ctx, db)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	rows, err := ReadMigrations(ctx, db)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	dbRevision := state.Revision
	stepMax := int64(0)
	appliedSteps := int64(0)
	for revision, row := range rows {
		if revision > stepMax {
			stepMax = revision
		}
		if row.Status == "applied" {
			appliedSteps++
		}
	}
	report.Database = databaseState{
		SchemaRevision: dbRevision,
		AppliedSteps:   appliedSteps,
		Digest:         schema.PostgresSchemaDigest(),
	}
	env := Env{PrimaryDSN: opts.PrimaryDSN, Secret: opts.Secret}

	// 5. 方向校验：库领先于二进制（state 或账本任一）即 blocked。
	if dbRevision > int64(contracts.SchemaRevision) || stepMax > int64(contracts.SchemaRevision) {
		return fail(ResultBlocked, ExitNeedsHuman,
			"数据库结构版本高于本二进制（dbRevision="+fmtInt(dbRevision)+
				", stepMax="+fmtInt(stepMax)+", binary="+fmtInt(int64(contracts.SchemaRevision))+
				"）：请升级二进制后再执行 upgrade", true)
	}

	// 6. 计划（只读求值，不写账本）。
	pending := []stepWithRow{}
	for _, step := range steps {
		row, exists := rows[step.Revision]
		status := "absent"
		if exists {
			if row.Status == "applied" && row.ImplVersion >= step.ImplVersion {
				continue // 已完成
			}
			if row.Status == "running" {
				status = "running"
			} else {
				status = "outdated-impl"
			}
		}
		report.Plan.Pending = append(report.Plan.Pending, pendingStep{
			Revision: step.Revision, StepID: step.ID, Title: step.Title,
			Target: string(step.Target), Destructive: step.Destructive, Status: status,
		})
		pending = append(pending, stepWithRow{step: step})
	}
	needsApply := false
	adopted := map[int64]bool{}
	for _, item := range pending {
		satisfied, err := item.step.Satisfied(ctx, env)
		if err != nil {
			return fail(ResultFailed, ExitRuntimeFailure,
				fmt.Sprintf("步骤 %s Satisfied 求值失败: %v", item.step.ID, err), true)
		}
		if satisfied {
			adopted[item.step.Revision] = true
			report.Plan.Adopt = append(report.Plan.Adopt, adoptStep{
				Revision: item.step.Revision, StepID: item.step.ID, Reason: "satisfied"})
		} else {
			needsApply = true
		}
	}
	// §9.3：pending 与 adopt 互斥（pending 只列「将执行 Apply」的步骤）。
	filtered := report.Plan.Pending[:0]
	for _, item := range report.Plan.Pending {
		if !adopted[item.Revision] {
			filtered = append(filtered, item)
		}
	}
	report.Plan.Pending = filtered
	report.Estimate = estimateInfo{
		ApplySteps:                  needsApplyCount(pending, report.Plan.Adopt),
		ApplyUpperBoundMinutes:      needsApplyCount(pending, report.Plan.Adopt) * minutes(opts.StepTimeout),
		StructuralUpperBoundMinutes: 60,
		TotalUpperBoundMinutes:      needsApplyCount(pending, report.Plan.Adopt)*minutes(opts.StepTimeout) + 60,
	}

	// 7. dry-run：输出计划并退出（只读；库落后/领先均 blocked/3，供自查）。
	if opts.DryRun {
		if dbRevision < int64(contracts.SchemaRevision) || stepMax > int64(contracts.SchemaRevision) {
			return fail(ResultBlocked, ExitNeedsHuman, "存在待执行升级步骤（dry-run 预演）", true)
		}
		return fail(ResultPlanned, ExitOK, "", true)
	}

	// 8. 确认门禁（§9.1）：待 Apply 步骤或库落后 → 两确认 + secret 必填。
	if needsApply || dbRevision < int64(contracts.SchemaRevision) {
		if !opts.StopWriteConfirmed || !opts.BackupConfirmed {
			return fail(ResultBlocked, ExitUsage,
				"存在待执行升级步骤：需要 --stop-write-confirmed 与 --backup-confirmed（先停止 gateway/jobs 并完成可恢复备份）", true)
		}
		if opts.Secret == "" {
			return fail(ResultBlocked, ExitUsage,
				"存在待执行升级步骤：需要 --secret（或 JUHE_AI_SECRET）供 seed 与凭据类步骤使用", true)
		}
	}

	// 9. 已最新短路：只读核验（全步骤 Check + 语句计数），不写任何表。
	if !needsApply && dbRevision == int64(contracts.SchemaRevision) && stepMax <= int64(contracts.SchemaRevision) {
		post := verifyAllSteps(ctx, env, steps, opts.StepTimeout)
		report.Postflight = &post
		if !post.OK {
			return fail(ResultFailed, ExitNeedsHuman, "up-to-date 核验未通过（见报告 postflight）", true)
		}
		report.Result = ResultUpToDate
		return outcome{ExitCode: ExitOK, Report: report}
	}

	// 10. 结构收敛·加法相（建表/补列/补种；P2 起分相——§7.3）。
	// 加法相失败：破坏性 DDL 未执行，rollback 保持 binary-only（换回旧二进制即可）。
	additiveResult, err := schema.EnsurePostgresFiltered(ctx, db, schema.EnsureAdditive)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, "ensure-schema 加法相失败: "+err.Error(), true)
	}

	// 11. seed（幂等补种内置行；secret 已在第 8 步校验存在）。
	if _, err := schema.SeedPostgresDefaults(ctx, db, schema.SeedOptions{Secret: opts.Secret}); err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, "seed 失败: "+err.Error(), true)
	}

	// 12. 逐步骤（revision 升序）：Satisfied→adopt；否则 running→Apply→Verify→applied。
	// 账本 DDL 在此创建（确认门禁已通过；§9.4-7 dry-run/缺确认路径零写入）。
	if err := EnsureLedger(ctx, db); err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	for _, item := range pending {
		step := item.step
		satisfiedCtx, satisfiedCancel := context.WithTimeout(ctx, opts.StepTimeout)
		satisfied, err := step.Satisfied(satisfiedCtx, env)
		satisfiedCancel()
		if err != nil {
			return fail(ResultFailed, ExitRuntimeFailure,
				fmt.Sprintf("步骤 %s Satisfied 求值失败: %v", step.ID, err), true)
		}
		if satisfied {
			if err := MarkAdopted(ctx, db, step, opts.AppVersion, now()); err != nil {
				return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
			}
			report.Executed = append(report.Executed, executedStep{Revision: step.Revision, Status: "adopted"})
			continue
		}
		// 破坏性步骤即将产生不可逆写：rollback 口径先于执行翻转为
		// backup-required（§9.3 破坏性步骤的写 → backup-required；否则后续
		// 步骤失败时报告会误称「换回旧二进制即可」而旧二进制已无法启动）。
		if step.Destructive {
			report.Rollback = RollbackBackupRequired
		}
		started := now()
		if err := MarkRunning(ctx, db, step, opts.AppVersion, started); err != nil {
			return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
		}
		stepErr := executeStep(ctx, db, step, env, opts, report, started, now)
		if stepErr != nil {
			return fail(ResultFailed, exitCodeForStepError(stepErr), stepErr.Error(), true)
		}
	}

	// 13. 结构收敛·破坏相（删列/删表）：进入本相即 backup-required
	//（语句级口径：部分执行亦然，§14）。
	report.Rollback = RollbackBackupRequired
	destructiveResult, err := schema.EnsurePostgresFiltered(ctx, db, schema.EnsureDestructive)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, "ensure-schema 破坏相失败: "+err.Error(), true)
	}
	// 14. postflight：全步骤 Check + 语句计数 + 账本一致性。
	post := verifyAllSteps(ctx, env, steps, opts.StepTimeout)
	totalStatements, destructiveStatements := schema.PostgresStatementCounts()
	if destructiveStatements != report.Plan.DestructiveDDLStatements || totalStatements <= 0 {
		post.OK = false
		post.Checks = append(post.Checks, postflightCheck{Name: "statement-counts", OK: false})
	}
	// 执行侧完整性：两相实际执行条数之和必须等于静态清单总数（防相间过滤
	// 漏语句的 623/624 形态；设计 §9.4-14）。
	if additiveResult.StatementCount+destructiveResult.StatementCount != totalStatements {
		post.OK = false
		post.Checks = append(post.Checks, postflightCheck{Name: "executed-statement-counts", OK: false})
	}
	afterRows, err := ReadMigrations(ctx, db)
	if err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	for _, step := range steps {
		if row, exists := afterRows[step.Revision]; !exists || row.Status != "applied" {
			post.OK = false
			post.Checks = append(post.Checks, postflightCheck{Name: "ledger-" + step.ID, OK: false})
		}
	}
	report.Postflight = &post
	if !post.OK {
		return fail(ResultFailed, ExitNeedsHuman, "postflight 未就绪（见报告 postflight）", true)
	}

	// 15. 写 schema_state，输出报告。
	if err := WriteSchemaState(ctx, db, int64(contracts.SchemaRevision), schema.PostgresSchemaDigest(), opts.AppVersion, now()); err != nil {
		return fail(ResultFailed, ExitRuntimeFailure, err.Error(), true)
	}
	report.Database.SchemaRevision = int64(contracts.SchemaRevision)
	report.Result = ResultUpgraded
	return outcome{ExitCode: ExitOK, Report: report}
}

// stepWithRow 是计划求值的待执行步骤载体（行信息在 P1 仅供 pending 呈现）。
type stepWithRow struct {
	step Step
}

func needsApplyCount(pending []stepWithRow, adopted []adoptStep) int {
	count := len(pending) - len(adopted)
	if count < 0 {
		count = 0
	}
	return count
}

// verifyAllSteps 对全部步骤跑 Check（Satisfied 与 Verify 同源）。
func verifyAllSteps(ctx context.Context, env Env, steps []Step, stepTimeout time.Duration) postflightInfo {
	post := postflightInfo{OK: true, Checks: []postflightCheck{}}
	for _, step := range steps {
		verifyCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := step.Verify(verifyCtx, env)
		cancel()
		if err != nil {
			post.OK = false
			post.Checks = append(post.Checks, postflightCheck{Name: step.ID, OK: false})
			continue
		}
		post.Checks = append(post.Checks, postflightCheck{Name: step.ID, OK: true})
	}
	return post
}

// executeStep 执行单步：Apply（带超时）→ Verify；失败写 detail_json 并返回
// 错误（Apply 失败 → ExitRuntimeFailure；Verify 失败 → ExitNeedsHuman）。
func executeStep(ctx context.Context, db *sql.DB, step Step, env Env, opts Options, report *Report, started time.Time, now func() time.Time) error {
	applyCtx, cancel := context.WithTimeout(ctx, opts.StepTimeout)
	defer cancel()
	detail, applyErr := step.Apply(applyCtx, env)
	duration := now().Sub(started).Milliseconds()
	if applyErr != nil {
		recordErr := RecordStepFailure(ctx, db, step, detail, applyErr)
		report.Executed = append(report.Executed, executedStep{
			Revision: step.Revision, Status: "failed", DurationMs: duration, Error: applyErr.Error(),
		})
		if recordErr != nil {
			return fmt.Errorf("步骤 %s Apply 失败: %v；detail_json 写入亦失败: %w", step.ID, applyErr, recordErr)
		}
		return fmt.Errorf("步骤 %s Apply 失败: %w", step.ID, applyErr)
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, opts.StepTimeout)
	defer verifyCancel()
	if err := step.Verify(verifyCtx, env); err != nil {
		verifyErr := fmt.Errorf("%w: 步骤 %s 未通过: %v", errVerifyFailed, step.ID, err)
		recordErr := RecordStepFailure(ctx, db, step, detail, verifyErr)
		report.Executed = append(report.Executed, executedStep{
			Revision: step.Revision, Status: "failed", DurationMs: duration, Error: verifyErr.Error(),
		})
		if recordErr != nil {
			// 双 %w 保留 errVerifyFailed 与 recordErr 两条包装链（Go 1.20+），
			// 退出码分类不得因 detail_json 写入失败而退化（H4 复审修复）。
			return fmt.Errorf("%w；detail_json 写入亦失败: %w", verifyErr, recordErr)
		}
		return verifyErr
	}
	if err := MarkApplied(ctx, db, step, duration, opts.AppVersion, now()); err != nil {
		return fmt.Errorf("步骤 %s 账本写入失败: %w", step.ID, err)
	}
	report.Executed = append(report.Executed, executedStep{
		Revision: step.Revision, Status: "applied", DurationMs: duration,
	})
	return nil
}

// exitCodeForStepError 区分 Apply 失败（exit 1）与 Verify 失败（exit 3）。
func exitCodeForStepError(err error) int {
	if errors.Is(err, errVerifyFailed) {
		return ExitNeedsHuman
	}
	return ExitRuntimeFailure
}

// errVerifyFailed 是 Verify 失败的标记错误（errors.Is 语义）。
var errVerifyFailed = errors.New("verify failed")

func sortStepsByRevision(steps []Step) []Step {
	sorted := append([]Step(nil), steps...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Revision < sorted[j-1].Revision; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

// validateRegistry 钉住注册表不变量：revision 唯一且递增、ImplVersion≥1、
// 字段完整（§8.4-3：低序不得依赖高序产物由评审与 golden 保证，引擎只查形状）。
func validateRegistry(steps []Step) error {
	seen := map[int64]string{}
	for i, step := range steps {
		if step.Revision <= 0 {
			return fmt.Errorf("步骤 %s revision 必须为正", step.ID)
		}
		if step.ImplVersion < 1 {
			return fmt.Errorf("步骤 %s ImplVersion 必须 ≥1", step.ID)
		}
		if step.ID == "" || step.Title == "" || step.Satisfied == nil || step.Apply == nil || step.Verify == nil {
			return fmt.Errorf("步骤 #%d 字段不完整", i)
		}
		if other, dup := seen[step.Revision]; dup {
			return fmt.Errorf("revision %d 重复: %s / %s", step.Revision, other, step.ID)
		}
		seen[step.Revision] = step.ID
	}
	return nil
}

func minutes(d time.Duration) int {
	m := int(d / time.Minute)
	if m < 1 {
		m = 1
	}
	return m
}

func fmtInt(value int64) string {
	return fmt.Sprintf("%d", value)
}
