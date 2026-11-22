package playground

// PostgreSQL 图像任务存储集成测试（设计 §3.4/§5.1，v1.2）：外部依赖未授权时
// pgtestkit 自动 SKIP；授权后覆盖状态机（Create/Claim 原子性/Complete/Fail）、
// 卡片归一（imageDeleted 读时判定、usage/costUsd 仅 completed 含已删）、参考图事务
// 绑定（归属校验失败整体回滚）、列表过滤（keyword/status 派生态/quality/游
// 标）、启动恢复（queued 返回 + running 置 failed）与手动删除（§3.4.6 v1.5：
// 终态任务行与产物字节同事务一并删、非终态拒绝、越权 404 口径）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func newPlaygroundImageJobsPG(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS juhe_business.playground_artifacts (
			system_account_id text NOT NULL,
			id text NOT NULL,
			kind text NOT NULL,
			mime_type text NOT NULL DEFAULT '',
			data bytea NOT NULL,
			job_id text NOT NULL DEFAULT '',
			created_at bigint NOT NULL,
			PRIMARY KEY (system_account_id, id))`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS juhe_business.playground_image_jobs (
			system_account_id text NOT NULL,
			id text NOT NULL,
			status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','failed')),
			account_id text NOT NULL,
			model_id text NOT NULL,
			prompt text NOT NULL,
			params jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(params::jsonb) = 'object'),
			result_artifact_ids jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(result_artifact_ids::jsonb) = 'array'),
			reference_artifact_ids jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(reference_artifact_ids::jsonb) = 'array'),
			input_tokens bigint NOT NULL DEFAULT 0,
			output_tokens bigint NOT NULL DEFAULT 0,
			total_tokens bigint NOT NULL DEFAULT 0,
			cost_usd double precision,
			error_message text NOT NULL DEFAULT '',
			images_expired boolean NOT NULL DEFAULT false,
			expires_at bigint NOT NULL DEFAULT 0,
			created_at bigint NOT NULL,
			updated_at bigint NOT NULL,
			completed_at bigint NOT NULL DEFAULT 0,
			PRIMARY KEY (system_account_id, id))`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_playground_image_jobs_account_created ON juhe_business.playground_image_jobs (system_account_id, created_at DESC)`)
		return err
	})
	// 参考图绑定依赖 artifacts 表；两者同 schema 一并初始化。
	return db
}

func newImageJobRecord(viewerID string, referenceIDs ...string) *ImageJobRecord {
	return &ImageJobRecord{
		ViewerID: viewerID, APIKeyID: playgroundKeyModelsFixtureID, ProviderCode: "gpt", ModelID: "gpt-image-2",
		Prompt: "一只赛博猫", AspectRatio: "1:1", Resolution: "1k", Quality: "standard",
		OutputFormat: "png", Size: "1024x1024", ReferenceIDs: referenceIDs,
	}
}

func seedReferenceArtifact(t *testing.T, db *sql.DB, viewerID, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,job_id,created_at) VALUES ($1,$2,'image_reference','image/png',$3,'',$4)`,
		viewerID, id, []byte("ref"), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestPGImageJobStoreCreateBindsReferences(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)

	refA, refB := "ref_aaa", "ref_bbb"
	seedReferenceArtifact(t, db, "sys_user_1", refA)
	seedReferenceArtifact(t, db, "sys_user_1", refB)
	created, err := store.Create(newImageJobRecord("sys_user_1", refA, refB))
	if err != nil || created.ID == "" || created.Status != imageJobStatusQueued || created.CreatedAt <= 0 {
		t.Fatalf("Create = %+v err=%v", created, err)
	}
	// 参考图绑定 job_id。
	for _, ref := range []string{refA, refB} {
		var bound string
		if err := db.QueryRow(`SELECT job_id FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND id=$2`,
			"sys_user_1", ref).Scan(&bound); err != nil || bound != created.ID {
			t.Fatalf("参考图 %s 绑定 = %q err=%v", ref, bound, err)
		}
	}
	// 任务行状态与 params（jsonb 无键序，按结构解析断言）。
	var status string
	var paramsRaw []byte
	if err := db.QueryRow(`SELECT status, params FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", created.ID).Scan(&status, &paramsRaw); err != nil || status != imageJobStatusQueued {
		t.Fatalf("任务行 = %q err=%v", status, err)
	}
	var params imageJobParams
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		t.Fatalf("params 解析失败: %v（%s）", err, paramsRaw)
	}
	if params.AspectRatio != "1:1" || params.Size != "1024x1024" || params.ReferenceCount != 2 {
		t.Fatalf("params 不符: %+v", params)
	}
	// v1.4 口径：Key 归属随 params.apiKeyId 落 jsonb（account_id 历史列恒空）。
	if params.APIKeyID != playgroundKeyModelsFixtureID || params.ProviderCode != "gpt" {
		t.Fatalf("params 缺 Key 归属: %+v", params)
	}
}

func TestPGImageJobStoreCreateRejectsMissingReferences(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)

	// 他户参考图（归属校验失败）。
	seedReferenceArtifact(t, db, "sys_user_2", "ref_other")
	_, err := store.Create(newImageJobRecord("sys_user_1", "ref_other"))
	if !errors.Is(err, errImageJobReferenceNotFound) {
		t.Fatalf("他户参考图必须拒绝，得到 %v", err)
	}
	// 不存在的参考图。
	_, err = store.Create(newImageJobRecord("sys_user_1", "ref_missing"))
	if !errors.Is(err, errImageJobReferenceNotFound) {
		t.Fatalf("缺失参考图必须拒绝，得到 %v", err)
	}
	// 整体回滚：任务行不得残留。
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1`, "sys_user_1").Scan(&count); err != nil || count != 0 {
		t.Fatalf("失败 Create 必须回滚，count=%d err=%v", count, err)
	}
	// 重复引用去重后仍可创建。
	seedReferenceArtifact(t, db, "sys_user_1", "ref_dup")
	created, err := store.Create(newImageJobRecord("sys_user_1", "ref_dup", "ref_dup"))
	if err != nil || len(created.ReferenceIDs) != 1 {
		t.Fatalf("重复引用应去重: %+v err=%v", created, err)
	}
}

func TestPGImageJobStoreClaimCompleteFail(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)

	seedReferenceArtifact(t, db, "sys_user_1", "ref_bind")
	created, err := store.Create(newImageJobRecord("sys_user_1", "ref_bind"))
	if err != nil {
		t.Fatal(err)
	}
	// Claim 原子性：第一次抢到，第二次必须失败（queued→running 单向）。
	first, err := store.Claim("sys_user_1", created.ID)
	if err != nil || !first {
		t.Fatalf("首 claim = %v err=%v", first, err)
	}
	second, err := store.Claim("sys_user_1", created.ID)
	if err != nil || second {
		t.Fatalf("重复 claim 必须失败: %v err=%v", second, err)
	}
	if _, err := store.Claim("sys_user_1", "no_such_job"); err != nil {
		t.Fatalf("不存在的任务 claim 只应返回 false: %v", err)
	}

	// Complete：usage/cost/expires_at=完成+7d/size 回写。
	cost := 0.0631
	if err := store.Complete("sys_user_1", created.ID, []string{"res_1"}, ImageJobUsage{InputTokens: 12, OutputTokens: 4096, TotalTokens: 4108}, &cost, "1024x1024"); err != nil {
		t.Fatal(err)
	}
	card, err := store.Get("sys_user_1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != imageJobStatusCompleted || card.ImageDeleted || len(card.Images) != 1 || card.Images[0].ID != "res_1" {
		t.Fatalf("completed 卡片不符: %+v", card)
	}
	if card.Images[0].URL != "/playground/artifacts/res_1/content" || len(card.References) == 0 {
		t.Fatalf("卡片 url/参考图不符: %+v", card)
	}
	if card.Usage == nil || card.Usage.TotalTokens != 4108 || card.CostUSD == nil || *card.CostUSD != cost {
		t.Fatalf("usage/cost 不符: %+v %+v", card.Usage, card.CostUSD)
	}
	if card.Size != "1024x1024" || card.CompletedAt <= 0 || card.ExpiresAt < card.CompletedAt {
		t.Fatalf("size/时间戳不符: %+v", card)
	}

	// Fail：error_message 落库、无图无 expires_at。
	failed, err := store.Create(newImageJobRecord("sys_user_1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_1", failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail("sys_user_1", failed.ID, "生图请求失败（上游 HTTP 429）: rate limited"); err != nil {
		t.Fatal(err)
	}
	card, err = store.Get("sys_user_1", failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != imageJobStatusFailed || card.Error == "" || len(card.Images) != 0 || card.Usage != nil || card.CostUSD != nil || card.ExpiresAt != 0 {
		t.Fatalf("failed 卡片不符: %+v", card)
	}
}

func TestPGImageJobStoreCardNormalizationAndExpiry(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)

	seedReferenceArtifact(t, db, "sys_user_1", "ref_live")
	created, err := store.Create(newImageJobRecord("sys_user_1", "ref_live"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_1", created.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("sys_user_1", created.ID, []string{"res_live"}, ImageJobUsage{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	card, err := store.Get("sys_user_1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 目录无价 → costUsd=null（不虚构金额）；usage 全 0 合法。
	if card.CostUSD != nil || card.Usage == nil || card.Usage.TotalTokens != 0 {
		t.Fatalf("无价卡片应为 costUsd=null: %+v", card)
	}

	// 读时判定：SQL 注入过期 expires_at（已过期未清理窗口）→ imageDeleted=true
	// 且 images/references 空数组；usage/costUsd 为历史记录价值仍透出（M1：
	// completed 含已删），expiresAt 收敛 0。
	pastExpired := "expired-past"
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
		(system_account_id,id,status,account_id,model_id,prompt,params,result_artifact_ids,reference_artifact_ids,cost_usd,input_tokens,output_tokens,total_tokens,expires_at,completed_at,created_at,updated_at)
		VALUES ('sys_user_1',$1,'completed','acc_1','gpt-image-2','旧图','{}','["res_old"]','["ref_old"]',0.5,10,20,30,$2,$3,$3,$3)`,
		pastExpired, now-1000, now-2000); err != nil {
		t.Fatal(err)
	}
	card, err = store.Get("sys_user_1", pastExpired)
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != imageJobStatusCompleted || !card.ImageDeleted || len(card.Images) != 0 || len(card.References) != 0 {
		t.Fatalf("过期读时判定不符: %+v", card)
	}
	if card.Usage == nil || card.Usage.TotalTokens != 30 || card.CostUSD == nil || *card.CostUSD != 0.5 {
		t.Fatalf("已删 completed 卡片应保留 usage/costUsd: %+v cost=%v", card.Usage, card.CostUSD)
	}
	if card.ExpiresAt != 0 {
		t.Fatalf("已删卡片 expiresAt 应收敛 0: %+v", card)
	}

	// images_expired 清理标记 → 同样收敛为已删。
	flagged := "expired-flagged"
	if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
		(system_account_id,id,status,account_id,model_id,prompt,params,result_artifact_ids,reference_artifact_ids,images_expired,completed_at,created_at,updated_at)
		VALUES ('sys_user_1',$1,'completed','acc_1','gpt-image-2','标记图','{}','["res_x"]','[]',true,$2,$2,$2)`,
		flagged, now-3000); err != nil {
		t.Fatal(err)
	}
	card, err = store.Get("sys_user_1", flagged)
	if err != nil || !card.ImageDeleted {
		t.Fatalf("images_expired 读时判定不符: %+v err=%v", card, err)
	}

	// 越权/不存在 → errImageJobNotFound（404 语义）。
	if _, err := store.Get("sys_user_2", created.ID); !errors.Is(err, errImageJobNotFound) {
		t.Fatalf("越权读取必须 not found，得到 %v", err)
	}
	if _, err := store.Get("sys_user_1", "no_such_job"); !errors.Is(err, errImageJobNotFound) {
		t.Fatalf("不存在读取必须 not found，得到 %v", err)
	}
}

func TestPGImageJobStoreListFiltersAndCursor(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)
	now := time.Now().UnixMilli()

	fixtures := []struct {
		id      string
		status  string
		prompt  string
		params  string
		expires int64
		flagged bool
		created int64
	}{
		{id: "job_a", status: "queued", prompt: "赛博猫在屋顶", params: `{"quality":"standard"}`, created: now - 1000},
		{id: "job_b", status: "running", prompt: "水彩风格赛博猫", params: `{}`, created: now - 2000},
		{id: "job_c", status: "completed", prompt: "油画赛博猫", params: `{"quality":"fine"}`, created: now - 3000},
		{id: "job_d", status: "completed", prompt: "已过期赛博猫", params: `{}`, expires: now - 1000, created: now - 4000},
		{id: "job_e", status: "completed", prompt: "标记删除赛博猫", params: `{}`, flagged: true, created: now - 5000},
		{id: "job_f", status: "failed", prompt: "失败赛博猫", params: `{"quality":"standard"}`, created: now - 6000},
	}
	for _, f := range fixtures {
		flagged := "false"
		if f.flagged {
			flagged = "true"
		}
		if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
			(system_account_id,id,status,account_id,model_id,prompt,params,cost_usd,images_expired,expires_at,created_at,updated_at)
			VALUES ('sys_user_1',$1,$2,'acc_1','gpt-image-2',$3,$4::jsonb,0.1,$5::boolean,$6,$7,$7)`,
			f.id, f.status, f.prompt, f.params, flagged, f.expires, f.created); err != nil {
			t.Fatal(err)
		}
	}

	// 无过滤：6 条 fixture（job_wild 尚未插入）；默认 limit=24 内取尽 →
	// nextCursor 缺省（无更多）。
	page, err := store.List("sys_user_1", ImageJobFilter{})
	if err != nil || len(page.Jobs) != 6 || page.NextCursor != 0 {
		t.Fatalf("无过滤列表 = %d 条 nextCursor=%d err=%v", len(page.Jobs), page.NextCursor, err)
	}
	// keyword ILIKE。
	page, err = store.List("sys_user_1", ImageJobFilter{Keyword: "水彩"})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_b" {
		t.Fatalf("keyword 过滤 = %+v err=%v", page, err)
	}
	// keyword 通配符字面匹配（M5）：%/_/\ 进 LIKE 前转义（ESCAPE '\'）——
	// 单独的「%」「_」只命中字面含该字符的提示词，不再通配全表。
	if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
		(system_account_id,id,status,account_id,model_id,prompt,params,created_at,updated_at)
		VALUES ('sys_user_1','job_wild','failed','acc_1','gpt-image-2','促销100%满意_原画','{}',$1,$1)`, now-5500); err != nil {
		t.Fatal(err)
	}
	page, err = store.List("sys_user_1", ImageJobFilter{Keyword: "%"})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_wild" {
		t.Fatalf("keyword 单 %% 应字面匹配: %+v err=%v", page, err)
	}
	page, err = store.List("sys_user_1", ImageJobFilter{Keyword: "_"})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_wild" {
		t.Fatalf("keyword 单 _ 应字面匹配: %+v err=%v", page, err)
	}
	// 常规关键词不受转义影响（6 条 fixture 均含「赛博猫」，job_wild 除外）。
	page, err = store.List("sys_user_1", ImageJobFilter{Keyword: "赛博猫"})
	if err != nil || len(page.Jobs) != 6 {
		t.Fatalf("keyword 常规匹配 = %d 条 err=%v", len(page.Jobs), err)
	}
	// completed 收敛为未删完成（expired 两例不计入）。
	page, err = store.List("sys_user_1", ImageJobFilter{Status: imageJobStatusCompleted})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_c" {
		t.Fatalf("completed 过滤 = %+v err=%v", page, err)
	}
	// expired 派生态（expires_at 已过 + images_expired）。
	page, err = store.List("sys_user_1", ImageJobFilter{Status: imageJobStatusExpired})
	if err != nil || len(page.Jobs) != 2 {
		t.Fatalf("expired 过滤 = %+v err=%v", page, err)
	}
	// active 派生态（queued+running）。
	page, err = store.List("sys_user_1", ImageJobFilter{Status: imageJobStatusActive})
	if err != nil || len(page.Jobs) != 2 {
		t.Fatalf("active 过滤 = %+v err=%v", page, err)
	}
	// quality 过滤。
	page, err = store.List("sys_user_1", ImageJobFilter{Quality: "standard"})
	if err != nil || len(page.Jobs) != 2 {
		t.Fatalf("quality 过滤 = %+v err=%v", page, err)
	}
	// before 游标（严格小于：游标取最早一行的 created_at 时应为空页）。
	page, err = store.List("sys_user_1", ImageJobFilter{Before: now - 6000})
	if err != nil || len(page.Jobs) != 0 {
		t.Fatalf("before 游标早于全部行应为空: %+v err=%v", page, err)
	}
	// limit 截断 + nextCursor。
	page, err = store.List("sys_user_1", ImageJobFilter{Limit: 2})
	if err != nil || len(page.Jobs) != 2 || page.NextCursor == 0 {
		t.Fatalf("limit=2 列表 = %d 条 nextCursor=%d err=%v", len(page.Jobs), page.NextCursor, err)
	}
	followUp, err := store.List("sys_user_1", ImageJobFilter{Limit: 100, Before: page.NextCursor})
	if err != nil || len(followUp.Jobs) != 5 {
		t.Fatalf("游标续页 = %d 条 err=%v", len(followUp.Jobs), err)
	}
	// apiKeyId 过滤（真实 Key 化修订：按 params jsonb 的创建时 Key 筛选；
	// 新行 account_id 历史列写空串）。
	if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
		(system_account_id,id,status,account_id,model_id,prompt,params,created_at,updated_at)
		VALUES ('sys_user_1','job_key1','completed','','gpt-image-2','Key 筛选图','{"apiKeyId":"key_1","providerCode":"gpt"}',$1,$1),
		       ('sys_user_1','job_key2','completed','','gpt-image-2','Key 筛选图二','{"apiKeyId":"key_2","providerCode":"gpt"}',$1,$1)`, now-6500); err != nil {
		t.Fatal(err)
	}
	page, err = store.List("sys_user_1", ImageJobFilter{APIKeyID: "key_1"})
	if err != nil || len(page.Jobs) != 1 || page.Jobs[0].JobID != "job_key1" {
		t.Fatalf("apiKeyId 过滤 = %+v err=%v", page, err)
	}
	if page, err = store.List("sys_user_1", ImageJobFilter{APIKeyID: "key_missing"}); err != nil || len(page.Jobs) != 0 {
		t.Fatalf("未知 apiKeyId 过滤应为空: %+v err=%v", page, err)
	}
	// status 词表外显式拒绝。
	if _, err := store.List("sys_user_1", ImageJobFilter{Status: "nope"}); !errors.Is(err, errImageJobFilterInvalid) {
		t.Fatalf("status 词表外必须拒绝，得到 %v", err)
	}
	// 越权：他户看不到。
	page, err = store.List("sys_user_2", ImageJobFilter{})
	if err != nil || len(page.Jobs) != 0 {
		t.Fatalf("越权列表应为空: %+v err=%v", page, err)
	}
}

// TestPGImageJobStoreDelete 手动删除（§3.4.6 v1.5）：completed（含参考图 +
// 结果图 artifact 绑定）→ Delete 后任务行与产物字节一并消失；queued/running
// → errImageJobNotDeletable 且行保留；跨 viewer/不存在 → errImageJobNotFound；
// failed → 删除成功。
func TestPGImageJobStoreDelete(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)

	seedReferenceArtifact(t, db, "sys_user_1", "ref_del")
	created, err := store.Create(newImageJobRecord("sys_user_1", "ref_del"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_1", created.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("sys_user_1", created.ID, []string{"res_del"}, ImageJobUsage{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	// 模拟执行器落图：一张 image_result artifact 绑定该任务。
	if _, err := db.Exec(`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,job_id,created_at) VALUES ($1,'res_del','image_result','image/png',$2,$3,$4)`,
		"sys_user_1", []byte("res"), created.ID, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("sys_user_1", created.ID); err != nil {
		t.Fatalf("completed 删除失败: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("任务行应已删除，count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND job_id=$2`,
		"sys_user_1", created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("产物字节（结果图+参考图）应一并删除，count=%d err=%v", count, err)
	}
	if _, err := store.Get("sys_user_1", created.ID); !errors.Is(err, errImageJobNotFound) {
		t.Fatalf("删除后读取应 not found，得到 %v", err)
	}

	// queued → errImageJobNotDeletable 且行仍在。
	queued, err := store.Create(newImageJobRecord("sys_user_1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("sys_user_1", queued.ID); !errors.Is(err, errImageJobNotDeletable) {
		t.Fatalf("queued 删除必须拒绝，得到 %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", queued.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("非终态拒绝后行应保留，count=%d err=%v", count, err)
	}
	// running 同口径拒绝。
	running, err := store.Create(newImageJobRecord("sys_user_1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_1", running.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("sys_user_1", running.ID); !errors.Is(err, errImageJobNotDeletable) {
		t.Fatalf("running 删除必须拒绝，得到 %v", err)
	}

	// 跨 viewer → errImageJobNotFound（他户行不受影响）。
	foreign, err := store.Create(newImageJobRecord("sys_user_2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_2", foreign.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("sys_user_2", foreign.ID, []string{"res_foreign"}, ImageJobUsage{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("sys_user_1", foreign.ID); !errors.Is(err, errImageJobNotFound) {
		t.Fatalf("越权删除必须 not found，得到 %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_2", foreign.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("他户行不得被删，count=%d err=%v", count, err)
	}
	if err := store.Delete("sys_user_1", "no_such_job"); !errors.Is(err, errImageJobNotFound) {
		t.Fatalf("不存在删除必须 not found，得到 %v", err)
	}

	// failed → 删除成功。
	failed, err := store.Create(newImageJobRecord("sys_user_1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim("sys_user_1", failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail("sys_user_1", failed.ID, "生图请求失败（上游 HTTP 429）: rate limited"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("sys_user_1", failed.ID); err != nil {
		t.Fatalf("failed 删除失败: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM `+playgroundImageJobsTable+` WHERE system_account_id=$1 AND id=$2`,
		"sys_user_1", failed.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed 任务行应已删除，count=%d err=%v", count, err)
	}
}

func TestPGImageJobStoreRecoverAtBoot(t *testing.T) {
	db := newPlaygroundImageJobsPG(t)
	store := NewPGImageJobStore(db)
	now := time.Now().UnixMilli()

	queuedRec, err := store.Create(newImageJobRecord("sys_user_1"))
	if err != nil {
		t.Fatal(err)
	}
	for id, status := range map[string]string{"run_1": "running", "run_2": "running"} {
		if _, err := db.Exec(`INSERT INTO `+playgroundImageJobsTable+`
			(system_account_id,id,status,account_id,model_id,prompt,created_at,updated_at)
			VALUES ('sys_user_2',$1,$2,'acc_x','gpt-image-2','中断图',$3,$3)`,
			id, status, now); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := store.RecoverAtBoot()
	if err != nil || len(queued) != 1 || queued[0].ID != queuedRec.ID || queued[0].Status != imageJobStatusQueued {
		t.Fatalf("恢复返回 queued 清单不符: %+v err=%v", queued, err)
	}
	// running 全部置 failed + error_message。
	rows, err := db.Query(`SELECT status, error_message FROM ` + playgroundImageJobsTable + ` WHERE system_account_id='sys_user_2'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var status, message string
		if err := rows.Scan(&status, &message); err != nil {
			t.Fatal(err)
		}
		seen++
		if status != imageJobStatusFailed || message != "网关重启，任务中断" {
			t.Fatalf("running 恢复不符: %s %q", status, message)
		}
	}
	if err := rows.Err(); err != nil || seen != 2 {
		t.Fatalf("恢复行数 = %d err=%v", seen, err)
	}
}
