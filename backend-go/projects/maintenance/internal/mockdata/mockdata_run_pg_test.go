package mockdata

// --mockdata 全流程的 PG 集成测试（pgtestkit 每测试独立库）：Run 在全新库上
// 完成 EnsurePostgres + SeedPostgresDefaults + 属主探测 + 清理 + business 域
// 造数 + 摘要，并验证重复执行的幂等收敛。真实 PG 由 pgtestkit 自动解析
// .local/project-resources/dev/env/shared.env；连接串内容不进入断言与日志。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// runMockdataForTest 在独立测试库上执行一次完整造数（固定时钟保证可复现）。
// chat 资产文件根固定到临时目录：造数域按 JUHE_AI_CHAT_ASSETS_ROOT 落盘，
// 测试不得把资产文件写到仓库工作目录。
func runMockdataForTest(t *testing.T, dsn string, now time.Time, summaryFile string) Report {
	t.Helper()
	t.Setenv("JUHE_AI_CHAT_ASSETS_ROOT", filepath.Join(t.TempDir(), "chat-assets"))
	report, err := Run(context.Background(), Options{
		DSN:           dsn,
		Days:          DefaultDays,
		DailyRequests: DefaultDailyRequests,
		Now:           now,
		SummaryFile:   summaryFile,
	})
	if err != nil {
		t.Fatalf("mockdata Run 失败: %v", err)
	}
	return report
}

// newMockdataTestDB 创建独立测试库（不做 prepare：Run 自带 ensure+seed），
// 返回连接串。
func newMockdataTestDB(t *testing.T) string {
	t.Helper()
	return pgtestkit.NewTestDatabase(t).DSN
}

// TestMockdataRunSeedsBusinessOnFreshDatabase 是第一波的核心验收：全新库一次
// Run 后 business 域计数齐全、报告结构完整、未接线域显式登记。
func TestMockdataRunSeedsBusinessOnFreshDatabase(t *testing.T) {
	dsn := newMockdataTestDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	report := runMockdataForTest(t, dsn, now, "")

	if report.Driver != "postgres" || report.Days != DefaultDays || report.DailyRequests != DefaultDailyRequests {
		t.Fatalf("report 参数回显不符: %+v", report)
	}
	if report.FinishedAt == "" || report.DurationMs < 0 {
		t.Fatalf("report 时间字段缺失: %+v", report)
	}
	// 域清单固定五个；business 与 chat_codex_model_check 已接线且计数非空
	//（其余域的接线状态由各自波次的交付与测试负责，这里不断言临时状态）。
	if len(report.Domains) != len(domainRegistry) {
		t.Fatalf("domains 数量 %d，注册表 %d", len(report.Domains), len(domainRegistry))
	}
	wired := map[string]bool{}
	for _, domain := range report.Domains {
		wired[domain.Name] = len(domain.Counts) > 0
	}
	if !wired[DomainBusiness] {
		t.Fatalf("business 域未产出计数: %+v", report.Domains)
	}
	if !wired[DomainChatCodexModelCheck] {
		t.Fatalf("chat_codex_model_check 域未产出计数: %+v", report.Domains)
	}
	// observability 域（可观测域波次）：maintenance 面照常产出；属主段缺失时
	// 跳过、不进 Counts（全新库无属主表）。
	if !wired[DomainObservability] {
		t.Fatalf("observability 域未产出计数: %+v", report.Domains)
	}
	obsCounts := map[string]int{}
	for _, domain := range report.Domains {
		if domain.Name == DomainObservability {
			obsCounts = domain.Counts
		}
	}
	for _, key := range []string{"publicApiLogs", "accountCleanupTargets", "apiKeyCleanupTargets"} {
		if obsCounts[key] == 0 {
			t.Fatalf("observability maintenance 面计数缺少 %s: %v", key, obsCounts)
		}
	}
	for _, key := range []string{"auditLogs", "operationLogs", "runtimeLogs"} {
		if _, present := obsCounts[key]; present {
			t.Fatalf("属主缺失时 observability 不应产出 %s: %v", key, obsCounts)
		}
	}
	// chat_codex_model_check 域的关键计数（属主段缺失时跳过，不进 Counts）。
	chatCounts := map[string]int{}
	for _, domain := range report.Domains {
		if domain.Name == DomainChatCodexModelCheck {
			chatCounts = domain.Counts
		}
	}
	for _, table := range []string{
		"chat_conversations", "chat_messages", "chat_assets", "chat_asset_references",
		"codex_context_sessions", "codex_context_responses", "codex_context_compacts",
	} {
		if chatCounts[table] == 0 {
			t.Fatalf("chat 域计数缺少 %s: %v", table, chatCounts)
		}
	}
	// 全新库无 juhe_j3b schema / juhe_jobs：j3b 与账户健康段按属主缺失跳过。
	for _, pending := range report.OwnerStoresPending {
		switch pending.Store {
		case "model_check":
			if chatCounts["model_check_runs"] != 0 {
				t.Fatalf("属主缺失时模型检测段不应产出: %v", chatCounts)
			}
		case "account_health":
			if chatCounts["account_health_outcomes"] != 0 {
				t.Fatalf("属主缺失时账户健康段不应产出: %v", chatCounts)
			}
		}
	}
	// 关键业务表计数。
	counts := report.Domains[0].Counts
	for _, table := range []string{
		"system_accounts", "groups", "super_group_members", "accounts", "account_connections",
		"account_endpoint_modes", "account_supported_models", "group_accounts", "route_strategies",
		"route_strategy_groups", "api_keys", "resource_authorizations", "resource_authorization_grants",
		"system_teams", "proxy_profiles", "announcements", "media_jobs", "account_test_tasks",
		"model_check_question_bank", "models",
	} {
		if counts[table] == 0 {
			t.Fatalf("business 计数缺少 %s: %v", table, counts)
		}
	}
	if counts["accounts"] < 25 {
		t.Fatalf("accounts 样本量不足: %d", counts["accounts"])
	}
	if report.Counts["accounts"] != counts["accounts"] {
		t.Fatalf("合并计数与域计数不一致: %v vs %v", report.Counts, counts)
	}
	// 属主存储第一波全部缺失（juhe_j3b / juhe_jobs 未建，gateway/jobs owner
	// 表未建）——pending 不构成失败，但要如实列出。
	if len(report.OwnerStoresPending) != len(ownerStores) {
		t.Fatalf("OwnerStoresPending = %+v, 期望 %d 项", report.OwnerStoresPending, len(ownerStores))
	}
	if report.SummaryPath != "" {
		t.Fatalf("未指定摘要路径时 SummaryPath 应为空: %q", report.SummaryPath)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("未预期警告: %v", report.Warnings)
	}
}

// TestMockdataRunWritesSummaryFile 验证 --mockdata-summary-file 的显式路径写出
// 与域接线快照内容。
func TestMockdataRunWritesSummaryFile(t *testing.T) {
	dsn := newMockdataTestDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	summaryPath := filepath.Join(t.TempDir(), "mockdata-summary.json")
	report := runMockdataForTest(t, dsn, now, summaryPath)
	if report.SummaryPath != summaryPath {
		t.Fatalf("SummaryPath = %q, want %q", report.SummaryPath, summaryPath)
	}
	raw, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("读摘要文件: %v", err)
	}
	var document summaryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("解析摘要: %v", err)
	}
	if document.Owner.Username != defaultAdminUsername {
		t.Fatalf("owner = %+v", document.Owner)
	}
	if len(document.MockUsers) == 0 || len(document.APIKeys) == 0 {
		t.Fatalf("摘要缺用户或 Key: users=%d keys=%d", len(document.MockUsers), len(document.APIKeys))
	}
	if document.MockUserPassword != MockUserPassword {
		t.Fatalf("mockUserPassword = %q", document.MockUserPassword)
	}
	wiredBusiness := false
	for _, domain := range document.Domains {
		if domain.Name == DomainBusiness && domain.Wired && len(domain.Counts) > 0 {
			wiredBusiness = true
		}
	}
	if !wiredBusiness {
		t.Fatalf("摘要域快照缺 business 接线: %+v", document.Domains)
	}
}

// TestMockdataRunIsIdempotent 重复执行不叠加、不报错：第二次 Run 的关键计数与
// 第一次一致（cleanup 先收敛上一批，确定性主键 upsert 兜底）。
func TestMockdataRunIsIdempotent(t *testing.T) {
	dsn := newMockdataTestDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := runMockdataForTest(t, dsn, now, "")
	second := runMockdataForTest(t, dsn, now.Add(2*time.Hour), "")
	for _, table := range []string{"accounts", "api_keys", "groups", "media_jobs", "system_accounts"} {
		if first.Domains[0].Counts[table] != second.Domains[0].Counts[table] {
			t.Fatalf("重复执行叠加了 %s: first=%d second=%d", table,
				first.Domains[0].Counts[table], second.Domains[0].Counts[table])
		}
	}
	// 第二次清理必须删掉第一批的行（清理标识收敛）。
	if len(second.Cleanup) == 0 {
		t.Fatalf("第二次运行没有清理记录: %+v", second.Cleanup)
	}
	if second.Cleanup[qualified(SchemaBusiness, "accounts")] == 0 {
		t.Fatalf("清理未命中 accounts: %v", second.Cleanup)
	}
}

// TestMockdataRunRejectsInvalidOptions 钉住参数边界：越界即错而不是钳制。
func TestMockdataRunRejectsInvalidOptions(t *testing.T) {
	dsn := "postgres://user@127.0.0.1:5432/db"
	cases := []struct {
		name    string
		options Options
		wantErr string
	}{
		{name: "days zero", options: Options{DSN: dsn, Days: 0, DailyRequests: 1}, wantErr: "days 必须在 1 到 90 之间"},
		{name: "days too large", options: Options{DSN: dsn, Days: 91, DailyRequests: 1}, wantErr: "days 必须在 1 到 90 之间"},
		{name: "daily requests zero", options: Options{DSN: dsn, Days: 1, DailyRequests: 0}, wantErr: "dailyRequests 必须在 1 到 500 之间"},
		{name: "daily requests too large", options: Options{DSN: dsn, Days: 1, DailyRequests: 501}, wantErr: "dailyRequests 必须在 1 到 500 之间"},
		{name: "missing dsn", options: Options{Days: 1, DailyRequests: 1}, wantErr: "需要 --dsn"},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			_, err := Run(context.Background(), item.options)
			if err == nil || !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, item.wantErr)
			}
		})
	}
}
