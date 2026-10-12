package mockdata

// 媒体工作室样本域的验收测试（媒体工作室设计 §9）：在隔离的临时数据根上应用
// business schema + seed，跑 seedBusiness 后再跑 seedStudio，按可机器判定的
// 口径核对结果：
//  1. coverage.go 里归属 studio 域的三条关键状态断言必须全部满足（域已接线时
//     它们是硬门槛）；
//  2. 样本清单：资产三 kind + 上传来源行、任务八种 status × settlement 组合、
//     收据两 kind、账本与资产求和一致、purpose=studio Key（EnsureStudioKey
//     同构四域）；
//  3. 幂等：重复执行不报错、不叠加；无 schema/seed 的数据根上跳过而不是失败。
//
// 所有写入都落在 t.TempDir() 下：测试绝不触碰 .local 或仓库内既有数据目录。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// studioTestEnv 在 businessTestEnv 的基础上执行 seedStudio 并记账，返回 env、
// 域结果与 business 库句柄。
func studioTestEnv(t *testing.T, dir string) (*env, DomainResult, *sql.DB) {
	t.Helper()
	e, _, db := businessTestEnv(t, dir)
	result, err := seedStudio(context.Background(), e)
	if err != nil {
		t.Fatalf("seedStudio: %v", err)
	}
	e.recordDomainResult(result)
	return e, result, db
}

// TestSeedStudioSatisfiesCoverageAssertions 逐条核对 coverage.go 中归属 studio
// 域的关键状态断言：域已接线时它们就是硬门槛。
func TestSeedStudioSatisfiesCoverageAssertions(t *testing.T) {
	e, result, db := studioTestEnv(t, businessTestRoot(t))
	if len(result.Counts) == 0 {
		t.Fatal("studio 域必须返回非空 Counts，否则覆盖报告会把它当成未接线")
	}
	assertions := 0
	for _, assertion := range coverageAssertions() {
		if assertion.Domain != DomainStudio {
			continue
		}
		assertions++
		if assertion.Store != StoreBusiness {
			t.Fatalf("studio 断言应指向 %s：%s", StoreBusiness, assertion.Name)
		}
		if got := businessCount(t, db, assertion.Query); got < assertion.Min {
			t.Errorf("%s: 命中 %d 行，要求至少 %d 行", assertion.Name, got, assertion.Min)
		}
	}
	if assertions != 3 {
		t.Fatalf("studio 域断言数 = %d, want 3（媒体工作室设计 §9 的三条覆盖断言）", assertions)
	}
	// 同一个 env 上跑覆盖校验：域已接线，studio 断言必须进入评估而不是 notCovered。
	report, err := VerifyCoverage(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.NotCovered {
		if strings.Contains(item, "域 "+DomainStudio) {
			t.Errorf("studio 已接线但断言仍被标记未覆盖: %s", item)
		}
	}
}

// TestSeedStudioSamples 核对契约 §9 的样本清单与配额账本一致性。
func TestSeedStudioSamples(t *testing.T) {
	_, result, db := studioTestEnv(t, businessTestRoot(t))
	if result.Counts["studio_assets"] != 4 {
		t.Fatalf("studio_assets count = %d, want 4（三 kind + 上传来源行）", result.Counts["studio_assets"])
	}
	if result.Counts["studio_tasks"] != 9 {
		t.Fatalf("studio_tasks count = %d, want 9（契约八组合 + video settled 载体）", result.Counts["studio_tasks"])
	}
	if result.Counts["studio_operations"] != 2 {
		t.Fatalf("studio_operations count = %d, want 2（speech + transcription）", result.Counts["studio_operations"])
	}
	if result.Counts["studio_user_asset_usage"] != 1 {
		t.Fatalf("studio_user_asset_usage count = %d, want 1", result.Counts["studio_user_asset_usage"])
	}

	// 资产：三 kind 齐全；image 含宽高 + source_task 回填；audio 是 TTS 产物
	//（params 含 voice/speed，无任务行）；video 含 duration_ms；上传行
	// source_task 为 NULL。
	if got := businessCount(t, db, "SELECT COUNT(DISTINCT kind) FROM studio_assets"); got != 3 {
		t.Fatalf("asset kinds = %d, want 3", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_assets WHERE id = 'stasset_mockdata_image' AND width = 1024 AND height = 1024 AND source_task_id = 'stask_mockdata_image_completed'"); got != 1 {
		t.Fatalf("image asset sample = %d, want 1", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_assets WHERE id = 'stasset_mockdata_audio' AND source_task_id IS NULL AND duration_ms IS NULL AND params_json LIKE '%voice%' AND params_json LIKE '%speed%'"); got != 1 {
		t.Fatalf("audio asset sample = %d, want 1", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_assets WHERE id = 'stasset_mockdata_video' AND duration_ms = 4000 AND source_task_id = 'stask_mockdata_video_completed'"); got != 1 {
		t.Fatalf("video asset sample = %d, want 1", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_assets WHERE id = 'stasset_mockdata_upload' AND source_task_id IS NULL"); got != 1 {
		t.Fatalf("upload asset sample = %d, want 1", got)
	}
	// params_json 全部是合法 JSON 对象。
	for _, id := range []string{"stasset_mockdata_image", "stasset_mockdata_audio", "stasset_mockdata_video", "stasset_mockdata_upload"} {
		var raw string
		if err := db.QueryRow(`SELECT params_json FROM studio_assets WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("%s params_json 不是合法 JSON 对象: %v（%s）", id, err, raw)
		}
	}

	// 任务：八种 status × settlement 组合齐全（+ video settled 载体行）。
	for _, combo := range []struct{ status, settlement string }{
		{"queued", "pending_settled"},
		{"in_progress", "pending_settled"},
		{"completed", "settled"},
		{"failed", "settled"},
		{"cancelled", "settled"},
		{"expired", "settled"},
		{"completed", "pending_settled"},
		{"completed", "failed"},
	} {
		got := businessCount(t, db, "SELECT COUNT(*) FROM studio_tasks WHERE status = '"+combo.status+"' AND settlement = '"+combo.settlement+"'")
		if got < 1 {
			t.Fatalf("组合 %s+%s 缺样本行", combo.status, combo.settlement)
		}
	}
	if got := businessCount(t, db, "SELECT COUNT(DISTINCT status) FROM studio_tasks"); got != 6 {
		t.Fatalf("task statuses = %d, want 6", got)
	}
	// completed+settled 行带 asset_id；未结算产物行不带 asset_id。
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_tasks WHERE status = 'completed' AND settlement = 'settled' AND asset_id IS NULL"); got != 0 {
		t.Fatalf("settled completed tasks must carry asset_id, got %d rows without", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_tasks WHERE settlement = 'pending_settled' AND asset_id IS NOT NULL"); got != 0 {
		t.Fatalf("pending_settled tasks must not carry asset_id, got %d rows with", got)
	}
	// 已受理行的亲和三元组指向真实存在的 media_jobs 样本行（或按回落语义留空）。
	if got := businessCount(t, db, `SELECT COUNT(*) FROM studio_tasks t
      WHERE t.upstream_job_id IS NOT NULL AND NOT EXISTS
        (SELECT 1 FROM media_jobs m WHERE m.id = t.upstream_job_id AND m.account_id = t.account_id AND m.provider_code = t.provider_code)`); got != 0 {
		t.Fatalf("%d task rows reference non-existent or mismatched media_jobs rows", got)
	}
	// failed+settled 带脱敏 error；completed+failed 是结算终局样本。
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_tasks WHERE status = 'failed' AND settlement = 'settled' AND error_json LIKE '%studio_dispatch_exhausted%'"); got != 1 {
		t.Fatalf("failed+settled error sample = %d, want 1", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_tasks WHERE status = 'completed' AND settlement = 'failed' AND lease_attempt_count >= 5 AND error_json LIKE '%studio_settle_failed%'"); got != 1 {
		t.Fatalf("settle terminal-failure sample = %d, want 1", got)
	}

	// 收据：两 kind 各 1 行，speech 带 asset_id，transcription 带文本结果。
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_operations WHERE kind = 'audio_speech' AND asset_id = 'stasset_mockdata_audio'"); got != 1 {
		t.Fatalf("speech receipt = %d, want 1", got)
	}
	var transcription string
	if err := db.QueryRow(`SELECT result_json FROM studio_operations WHERE kind = 'audio_transcription'`).Scan(&transcription); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(transcription), &decoded); err != nil {
		t.Fatalf("transcription result_json 不是合法 JSON: %v", err)
	}
	if _, ok := decoded["text"]; !ok {
		t.Fatalf("transcription result_json 缺少 text: %s", transcription)
	}

	// 账本一致：bytes 求和 + 行数与资产行完全一致（配额账本一致）。
	if got := businessCount(t, db, `SELECT COUNT(*) FROM studio_user_asset_usage l
      WHERE l.asset_bytes <> (SELECT COALESCE(SUM(a.bytes), 0) FROM studio_assets a WHERE a.system_account_id = l.system_account_id)
        OR l.asset_count <> (SELECT COUNT(*) FROM studio_assets a WHERE a.system_account_id = l.system_account_id)`); got != 0 {
		t.Fatalf("%d ledger rows inconsistent with studio_assets", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM studio_user_asset_usage"); got != 1 {
		t.Fatalf("ledger rows = %d, want 1（admin 样本归属）", got)
	}

	// purpose=studio Key（EnsureStudioKey 同构）：purpose、四域授权、专属
	// 路由策略、状态 active。
	var domainsJSON string
	if err := db.QueryRow(`SELECT studio_domains_json FROM api_keys WHERE purpose = 'studio'`).Scan(&domainsJSON); err != nil {
		t.Fatalf("studio key missing: %v", err)
	}
	var domains []string
	if err := json.Unmarshal([]byte(domainsJSON), &domains); err != nil {
		t.Fatalf("studio_domains_json 不是合法 JSON 数组: %v", err)
	}
	if strings.Join(domains, ",") != "text,image,audio,video" {
		t.Fatalf("studio domains = %v, want [text image audio video]", domains)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM api_keys WHERE purpose = 'studio' AND status = 'active'"); got != 1 {
		t.Fatalf("active studio keys = %d, want 1", got)
	}
	if got := businessCount(t, db, "SELECT COUNT(*) FROM api_keys k WHERE k.purpose = 'studio' AND EXISTS (SELECT 1 FROM route_strategies r WHERE r.id = k.route_strategy_id)"); got != 1 {
		t.Fatalf("studio key route strategy binding = %d, want 1", got)
	}
}

// TestSeedStudioIdempotent 验证重复执行收敛：计数不变、行数不变。
func TestSeedStudioIdempotent(t *testing.T) {
	dir := businessTestRoot(t)
	_, first, db := studioTestEnv(t, dir)
	tables := []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage", "api_keys", "route_strategies"}
	snapshot := map[string]int{}
	for _, table := range tables {
		snapshot[table] = businessCount(t, db, "SELECT COUNT(*) FROM "+table)
	}
	_, second, dbAfter := studioTestEnv(t, dir)
	for _, key := range []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage", "api_keys"} {
		if first.Counts[key] != second.Counts[key] {
			t.Fatalf("counts[%s] = %d then %d，重复执行必须收敛", key, first.Counts[key], second.Counts[key])
		}
	}
	for _, table := range tables {
		if after := businessCount(t, dbAfter, "SELECT COUNT(*) FROM "+table); after != snapshot[table] {
			t.Fatalf("%s rows changed on rerun: %d -> %d", table, snapshot[table], after)
		}
	}
}

// TestSeedStudioSkipsOnEmptyRoot 验证无 schema/seed 的数据根上跳过而不是失败
// （Counts 为空 ⇒ 覆盖报告按未接线呈现）。
func TestSeedStudioSkipsOnEmptyRoot(t *testing.T) {
	paths, err := ResolvePaths(t.TempDir(), "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() {
		if err := e.Close(); err != nil {
			t.Errorf("关闭 mockdata 存储: %v", err)
		}
	}()
	result, err := seedStudio(context.Background(), e)
	if err != nil {
		t.Fatalf("seedStudio on empty root: %v", err)
	}
	if result.Name != DomainStudio || len(result.Counts) != 0 {
		t.Fatalf("result = %+v, want unwired studio domain", result)
	}
}
