package mockdata

// business 域内容的 PG 集成断言：与覆盖断言和页面数据来源对齐的关键状态样本
// （账户状态、路由模式、授权状态、Key 运行态、模型 scope/status、题库、媒体
// 任务状态分布、授权实例）。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestBusinessDomainContent(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t).DSN
	report := runMockdataForTest(t, dsn, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), "")
	_ = report

	env, err := newEnv(Options{DSN: dsn, Days: 1, DailyRequests: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Close() }()
	ctx := context.Background()
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	b := qualified(SchemaBusiness, "accounts")
	// 账户状态两极：active 与不可调度状态族各有样本。
	if n := count("SELECT COUNT(*) FROM " + b + " WHERE status = 'active'"); n < 1 {
		t.Fatal("缺 active 账户样本")
	}
	if n := count("SELECT COUNT(*) FROM " + b + " WHERE status IN ('disabled','pending_test','error','rate_limited','temporary_unavailable','expired')"); n < 1 {
		t.Fatal("缺不可调度状态账户样本")
	}
	// 布尔列与 0/1 列的列型正确落库（capability_domain 四值、health 三字段补齐）。
	if n := count("SELECT COUNT(*) FROM " + b + " WHERE capability_domain = 'video'"); n < 1 {
		t.Fatal("缺 video capability 样本（acc_media）")
	}
	if n := count("SELECT COUNT(*) FROM " + b + " WHERE health_check_connection_id IS NOT NULL AND health_check_model IS NULL"); n != 0 {
		t.Fatalf("health 三字段补齐不一致: %d", n)
	}
	// 路由模式三种 + 分组绑定。
	if n := count("SELECT COUNT(DISTINCT mode) FROM " + qualified(SchemaBusiness, "route_strategies")); n < 3 {
		t.Fatalf("路由模式分布 %d < 3", n)
	}
	// 授权状态两极 + 团队授权来源。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "resource_authorizations") + " WHERE status = 'active'"); n < 1 {
		t.Fatal("缺 active 授权样本")
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "resource_authorizations") + " WHERE effective_source_type = 'team'"); n < 1 {
		t.Fatal("缺团队授权样本")
	}
	// Key 级运行态两种以上状态。
	if n := count("SELECT COUNT(DISTINCT status) FROM " + qualified(SchemaBusiness, "account_api_key_runtime_states")); n < 2 {
		t.Fatalf("Key 运行态状态分布 %d < 2", n)
	}
	// 模型目录：personal scope 与 draft/disabled 状态。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "models") + " WHERE scope = 'personal'"); n < 1 {
		t.Fatal("缺 personal 模型样本")
	}
	if n := count("SELECT COUNT(DISTINCT status) FROM " + qualified(SchemaBusiness, "models")); n < 2 {
		t.Fatalf("模型状态分布 %d < 2", n)
	}
	// 题库三态。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "model_check_question_bank") + " WHERE status = 'approved'"); n < 1 {
		t.Fatal("缺 approved 题目")
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "model_check_question_bank") + " WHERE status = 'rejected'"); n < 1 {
		t.Fatal("缺 rejected 题目")
	}
	// media_jobs：video 六状态、音频三 kind、owner 列与 Key 同源。
	if n := count("SELECT COUNT(DISTINCT status) FROM " + qualified(SchemaBusiness, "media_jobs") + " WHERE kind = 'video'"); n < 4 {
		t.Fatalf("video 状态分布 %d < 4", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "media_jobs") + " WHERE kind IN ('audio_speech','audio_transcription')"); n < 3 {
		t.Fatalf("音频任务样本 %d < 3", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "media_jobs") + " WHERE owner_system_account_id = ''"); n != 0 {
		t.Fatalf("media_jobs owner 列存在空串: %d", n)
	}
	// 授权实例：实例账户引用真实默认分组（sys_admin grantee 也必须有实例）。
	if n := count("SELECT COUNT(*) FROM " + b + " WHERE authorization_instance_source_account_id IS NOT NULL"); n < 1 {
		t.Fatal("缺授权实例账户")
	}
	if n := count(`SELECT COUNT(*) FROM ` + b + ` i
		LEFT JOIN ` + qualified(SchemaBusiness, "group_accounts") + ` ga ON ga.account_id = i.id
		WHERE i.authorization_instance_source_account_id IS NOT NULL AND ga.group_id IS NULL`); n != 0 {
		t.Fatalf("实例账户缺默认分组绑定: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM ` + b + ` i
		JOIN ` + qualified(SchemaBusiness, "group_accounts") + ` ga ON ga.account_id = i.id
		JOIN ` + qualified(SchemaBusiness, "groups") + ` g ON g.id = ga.group_id
		WHERE i.authorization_instance_source_account_id IS NOT NULL AND g.system_account_id <> i.system_account_id`); n != 0 {
		t.Fatalf("实例账户分组不属于被授权人: %d", n)
	}
	// 账户测试任务三态 + 会话链接。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "account_test_tasks")); n < 3 {
		t.Fatalf("账户测试任务样本 %d < 3", n)
	}
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "account_test_session_tasks")); n < 3 {
		t.Fatalf("测试会话链接样本 %d < 3", n)
	}
	// 触发器联动：system_accounts 插入应生成查看者健康行（PG 触发器链验证）。
	if n := count("SELECT COUNT(*) FROM " + qualified(SchemaBusiness, "account_list_availability_projection_viewer_health")); n < 1 {
		t.Fatal("缺查看者健康触发器行")
	}
	// 摘要与库内数据一致：main Key 的明文哈希能对上 key_hash。
	if n := count("SELECT COUNT(*) FROM "+qualified(SchemaBusiness, "api_keys")+" WHERE id = $1 AND key_hash = $2",
		CleanupIDPrefix+"key_main", secretHash("sk-"+CleanupTracePrefix+"main-"+repeatK(24))); n != 1 {
		t.Fatal("main Key 的 key_hash 与摘要明文不一致")
	}
}

// repeatK 生成 n 个 'k'（main Key 明文的尾段）。
func repeatK(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'k'
	}
	return string(out)
}
