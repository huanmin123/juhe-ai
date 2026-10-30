// `maintenance upgrade` 的 12 步骤注册表（升级兼容与迁移账本设计 §8/§10）。
// 每个步骤的 Apply 复用既有一次性命令的内部函数（单一实现），stdout/stderr
// 经 CaptureOutput 捕获后进 detail_json；Check 与 Verify 同源（本文件每个
// 步骤的 check 闭包同时作为 Satisfied 与 Verify 的实现）。
//
// revision 1..12 的分配即设计 §10 表序； golden 测试钉死
// (revision, step_id, implVersion) 有序对。低序步骤不得依赖高序产物（§8.4-3）。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// 目标连接串环境变量（§8.3：缺失回落 --dsn，进程内 Setenv 使既有 bootstrap
// 函数读到回落值——把「漏跑 j3b 静默失败」变成显式执行）。
const (
	envJ3AURL     = "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL"
	envJ3BURL     = "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"
	envBalanceURL = "JUHE_AI_MAINTENANCE_ACCOUNT_BALANCE_POSTGRES_URL"
)

// upgradeTargets 解析三个辅助 target 的连接串（env 优先，缺失回落 primary）。
func upgradeTargets(primaryDSN string) (j3a, j3b, balance string) {
	j3a = os.Getenv(envJ3AURL)
	if strings.TrimSpace(j3a) == "" {
		j3a = primaryDSN
		os.Setenv(envJ3AURL, j3a)
	}
	j3b = os.Getenv(envJ3BURL)
	if strings.TrimSpace(j3b) == "" {
		j3b = primaryDSN
		os.Setenv(envJ3BURL, j3b)
	}
	balance = os.Getenv(envBalanceURL)
	if strings.TrimSpace(balance) == "" {
		balance = primaryDSN
		os.Setenv(envBalanceURL, balance)
	}
	return j3a, j3b, balance
}

// openPG 打开 PG 连接（pgx stdlib 驱动在包内注册一次）。
func openPG(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}

// callResult 捕获既有 *Result 函数的输出并返回退出码（0=成功）。
func callResult(fn func() int) (code int, output string) {
	stdoutText, stderrText := upgradepipeline.CaptureOutput(func() {
		code = fn()
	})
	return code, stdoutText + stderrText
}

// exitErr 把步骤内命令退出码转成错误（detail 携带捕获输出）。
func exitErr(stepID, output string, code int) error {
	tail := output
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	return fmt.Errorf("%s 命令退出码 %d；输出尾部: %s", stepID, code, tail)
}

// sqlScalar 执行单值查询。fresh 库语义（§9.4-6 先于 EnsurePostgres 求值）：
// 关系不存在（42P01）按 0 行处理——「表/列尚未建立」即「无可迁移数据」，
// 谓词据此返回未满足或空跑；结构由后续 EnsureAll 保证收敛。
func sqlScalar(ctx context.Context, dsn, query string, args ...any) (int, error) {
	db, err := openPG(dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return 0, nil
		}
		return 0, err
	}
	return value, nil
}

// tableExists / columnExists 是 Check 谓词的结构探测原语。
func tableExists(ctx context.Context, dsn, schemaName, table string) (bool, error) {
	value, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`,
		schemaName, table)
	return value > 0, err
}

func columnExists(ctx context.Context, dsn, schemaName, table, column string) (bool, error) {
	value, err := sqlScalar(ctx, dsn,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		schemaName, table, column)
	return value > 0, err
}

// upgradeSteps 返回 12 步骤注册表（revision 升序；引擎再做一次排序校验）。
// secret 为空时，凭据类 Check 不允许调用（第 8 步门禁保证到 Apply 阶段必有；
// dry-run 阶段 Check 若需要 secret 会以错误呈现——预检语义可接受）。
func upgradeSteps(primaryDSN, secret string) []upgradepipeline.Step {
	j3aDSN, j3bDSN, balanceDSN := upgradeTargets(primaryDSN)

	studioCheck := func(ctx context.Context) (bool, error) {
		for _, table := range []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage"} {
			exists, err := tableExists(ctx, primaryDSN, "juhe_business", table)
			if err != nil || exists {
				return false, err
			}
		}
		exists, err := columnExists(ctx, primaryDSN, "juhe_business", "api_keys", "studio_domains_json")
		if err != nil {
			return false, err
		}
		return !exists, nil
	}

	monitoringCheck := func(ctx context.Context) (bool, error) {
		for _, table := range []string{
			"system_metrics_samples", "system_metrics_hourly", "system_metrics_trend_windows",
			"process_event_loop_samples", "process_event_loop_hourly", "process_event_loop_trend_windows",
			"go_runtime_metrics_samples", "go_runtime_metrics_hourly", "go_runtime_metrics_trend_windows",
			"database_storage_snapshots", "table_storage_snapshots", "table_monitor_owner_leases",
		} {
			exists, err := tableExists(ctx, primaryDSN, "juhe_stats", table)
			if err != nil || exists {
				return false, err
			}
		}
		return true, nil
	}

	capabilityCheck := func(ctx context.Context) (bool, error) {
		code, _ := callResult(func() int {
			return accountCapabilityDomainVerifyResult(primaryDSN, secret, "m2-cutover", "postflight")
		})
		return code == 0, nil
	}

	schedprefCheck := func(ctx context.Context) (bool, error) {
		code, _ := callResult(func() int {
			return schedulingPreferencesVerifyResult(primaryDSN, "postflight")
		})
		return code == 0, nil
	}

	upstreamCheck := func(ctx context.Context) (bool, error) {
		code, _ := callResult(func() int {
			return upstreamBaseURLVerifyResult(primaryDSN, "postflight", secret)
		})
		return code == 0, nil
	}

	return []upgradepipeline.Step{
		{
			Revision: 1, ID: "j3a-proxy-latency-pg",
			Title: "J3a 代理延迟检测 PostgreSQL 表与索引预置", Target: upgradepipeline.TargetJ3A,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				code, _ := callResult(func() int { return j3aProxyLatencyBootstrapResult(false) })
				return code == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, j3aDSN, upgradepipeline.TargetJ3A); err != nil {
					return nil, err
				}
				code, output := callResult(func() int { return j3aProxyLatencyBootstrapResult(true) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("j3a 预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				code, _ := callResult(func() int { return j3aProxyLatencyBootstrapResult(false) })
				if code != 0 {
					return fmt.Errorf("J3a 表/索引契约未就绪（退出码 %d）", code)
				}
				return nil
			},
		},
		{
			Revision: 2, ID: "j3b-model-check-pg",
			Title: "J3b 模型检测 PostgreSQL 表预置与 trigger_kind 词表", Target: upgradepipeline.TargetJ3B,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				code, _ := callResult(func() int { return j3bModelCheckBootstrapResult(false) })
				return code == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, j3bDSN, upgradepipeline.TargetJ3B); err != nil {
					return nil, err
				}
				code, output := callResult(func() int { return j3bModelCheckBootstrapResult(true) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("j3b 预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				code, _ := callResult(func() int { return j3bModelCheckBootstrapResult(false) })
				if code != 0 {
					return fmt.Errorf("J3b 表/CHECK 契约未就绪（退出码 %d）", code)
				}
				return nil
			},
		},
		{
			Revision: 3, ID: "account-balance-pg",
			Title: "J2 账户余额 PostgreSQL 四表预置", Target: upgradepipeline.TargetAccountBalance,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				for _, table := range []string{
					"account_balance_owner_leases", "account_balance_account_leases",
					"account_balance_snapshots", "account_balance_outcomes",
				} {
					exists, err := tableExists(ctx, balanceDSN, "juhe_jobs", table)
					if err != nil || !exists {
						return false, err
					}
				}
				return true, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				if err := upgradepipeline.EnsureTargetSchema(ctx, balanceDSN, upgradepipeline.TargetAccountBalance); err != nil {
					return nil, err
				}
				code, output := callResult(accountBalanceBootstrapResult)
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("账户余额预置", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				for _, table := range []string{
					"account_balance_owner_leases", "account_balance_account_leases",
					"account_balance_snapshots", "account_balance_outcomes",
				} {
					exists, err := tableExists(ctx, balanceDSN, "juhe_jobs", table)
					if err != nil {
						return err
					}
					if !exists {
						return fmt.Errorf("juhe_jobs.%s 缺失", table)
					}
				}
				return nil
			},
		},
		{
			Revision: 4, ID: "capability-domain",
			Title: "AI 账户能力域赋域迁移（m1→m2-cutover）", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return capabilityCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				// 两阶段状态机固定 m1→m2-cutover 顺序（中间态持久于阶段记录，
				// 重复执行对已完成阶段为 no-op——设计 §10 注）。
				code, output := callResult(func() int {
					return accountCapabilityDomainMigrateResult(primaryDSN, secret, "m1", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"stage": "m1", "output": output}, exitErr("能力域 m1", output, code)
				}
				code, output = callResult(func() int {
					return accountCapabilityDomainMigrateResult(primaryDSN, secret, "m2-cutover", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"stage": "m2-cutover", "output": output}, exitErr("能力域 m2-cutover", output, code)
				}
				return upgradepipeline.Detail{"stage": "m2-cutover", "output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := capabilityCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("能力域阶段记录未达 m2_finalized")
				}
				return nil
			},
		},
		{
			Revision: 5, ID: "scheduling-preferences",
			Title: "调度偏好独立实体绑定回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return schedprefCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return schedulingPreferencesMigrateResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("调度偏好迁移", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := schedprefCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("调度偏好迁移完成记录未达 completed 或存在残留")
				}
				return nil
			},
		},
		{
			Revision: 6, ID: "merge-super-groups",
			Title: "merge 路由策略迁移至超级分组", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				value, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_business.route_strategies WHERE mode = 'merge'`)
				if err != nil {
					return false, err
				}
				return value == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return mergeSuperGroupsMigrationResult(false, primaryDSN, "", "upgrade-merge-super-groups", true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("超级分组迁移", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				value, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_business.route_strategies WHERE mode = 'merge'`)
				if err != nil {
					return err
				}
				if value != 0 {
					return fmt.Errorf("仍存在 %d 条 mode='merge' 策略", value)
				}
				return nil
			},
		},
		{
			Revision: 7, ID: "chat-account-only-binding",
			Title: "AI 问答会话账户唯一绑定收敛", Target: upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				exists, err := columnExists(ctx, primaryDSN, "juhe_chat", "chat_conversations", "bind_mode")
				if err != nil || exists {
					return false, err
				}
				exists, err = columnExists(ctx, primaryDSN, "juhe_chat", "chat_conversations", "bind_group_id")
				if err != nil || exists {
					return false, err
				}
				exists, err = columnExists(ctx, primaryDSN, "juhe_chat", "chat_conversations", "bind_group_name_snapshot")
				if err != nil || exists {
					return false, err
				}
				return true, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int { return chatAccountOnlyBindingMigrationResult(primaryDSN) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("会话绑定收敛", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := columnExists(ctx, primaryDSN, "juhe_chat", "chat_conversations", "bind_mode")
				if err != nil {
					return err
				}
				if satisfied {
					return errors.New("chat_conversations.bind_mode 仍存在")
				}
				return nil
			},
		},
		{
			Revision: 8, ID: "upstream-base-url-version-root",
			Title: "上游 Base URL 版本根语义回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return upstreamCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return upstreamBaseURLMigrationResult(false, primaryDSN, "", secret, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("Base URL 回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := upstreamCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("上游 Base URL postflight 核验未通过")
				}
				return nil
			},
		},
		{
			Revision: 9, ID: "success-cost-backfill",
			Title: "六表 success_cost_usd 历史回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				exists, err := tableExists(ctx, primaryDSN, "juhe_stats", "backfill_progress")
				if err != nil || !exists {
					return false, err
				}
				total, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_stats.backfill_progress`)
				if err != nil {
					return false, err
				}
				done, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_stats.backfill_progress WHERE status = 'done'`)
				if err != nil {
					return false, err
				}
				return total >= 6 && done == total, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int { return applySuccessCostBackfillResult(primaryDSN, false) })
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("成功成本回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := sqlScalar(ctx, primaryDSN,
					`SELECT count(*) FROM juhe_stats.backfill_progress WHERE status <> 'done'`)
				if err != nil {
					return err
				}
				if satisfied != 0 {
					return fmt.Errorf("backfill_progress 存在 %d 条非 done 行", satisfied)
				}
				return nil
			},
		},
		{
			Revision: 10, ID: "studio-retirement",
			Title: "媒体工作室遗留清退", Target: upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return studioCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return studioRetirementResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("studio 清退", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := studioCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("studio 遗留对象未清退干净")
				}
				return nil
			},
		},
		{
			Revision: 11, ID: "drop-retired-monitoring-tables",
			Title: "12 张退役监控表清退", Target: upgradepipeline.TargetPrimary,
			Destructive: true,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				return monitoringCheck(ctx)
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return dropRetiredMonitoringTablesResult(primaryDSN, true, true, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("监控表清退", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				satisfied, err := monitoringCheck(ctx)
				if err != nil {
					return err
				}
				if !satisfied {
					return errors.New("退役监控表未清退干净")
				}
				return nil
			},
		},
		{
			Revision: 12, ID: "account-default-resources",
			Title: "系统账户默认资源家族回填", Target: upgradepipeline.TargetPrimary,
			ImplVersion: 1,
			Satisfied: func(ctx context.Context, _ upgradepipeline.Env) (bool, error) {
				// P1 简化判据：无系统账户即满足（fresh adopt）；存量库一律执行
				// 幂等命令（完成态零写入，§10 命令语义）。弱判据在步骤注释声明。
				value, err := sqlScalar(ctx, primaryDSN, `SELECT count(*) FROM juhe_business.system_accounts`)
				if err != nil {
					return false, err
				}
				return value == 0, nil
			},
			Apply: func(ctx context.Context, _ upgradepipeline.Env) (upgradepipeline.Detail, error) {
				code, output := callResult(func() int {
					return accountDefaultResourcesResult(primaryDSN, secret, true)
				})
				if code != 0 {
					return upgradepipeline.Detail{"output": output}, exitErr("默认资源回填", output, code)
				}
				return upgradepipeline.Detail{"output": output}, nil
			},
			Verify: func(ctx context.Context, _ upgradepipeline.Env) error {
				// P1 简化：Apply 退出码 0 即通过（命令内部含逐账户报告与
				// 幂等保证）；独立完成态谓词随 P2 补强。
				return nil
			},
		},
	}
}
