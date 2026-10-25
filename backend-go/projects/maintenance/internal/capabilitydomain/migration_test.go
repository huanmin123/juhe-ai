// 迁移器集成测试（SQLite 真库，进程内直调 Run/Verify；PG 侧行为走
// internal/schema 的 opt-in live smoke 门禁
// TestPostgresCapabilityDomainLiveSmoke，环境变量
// JUHE_AI_CAPABILITY_DOMAIN_PG_SMOKE_URL 指向一次性隔离库时才运行）：
//   - 阶段状态机全组合（PlanMigration 纯函数）；
//   - 完整 m1 迁移 → m1_complete；重复执行只报告完成；
//   - m2-cutover 门禁（无记录/m1_running 拒绝，m1_complete 放行）与封存
//     （m2_finalized 拒绝重推，postflight 推导对账不适用）；
//   - 失败清单（缺省/空数组/null/非数组/未知模式/解密失败/源缺失）不猜域、
//     不写完成阶段，修复后同阶段恢复重跑；
//   - 授权实例继承（源在/源软删除/源缺失/源失败）与软删除行保域；
//   - 赋域 + 声明裁剪：域内行 credentials_encrypted 密文逐字节不变，混合
//     声明行被裁剪至落域 lane 且报告含脱敏明细（含被裁值建议归属域）；
//   - 并行单执行者（SQLite 条件更新：完成阶段恰好写入一次）；
//   - 列形状：缺列补齐、错误形状拒绝。

package capabilitydomain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const testSecret = "capability-domain-test-secret"

// openMigrationDB 打开一个完成 EnsurePostgres 的隔离测试库（pgtestkit，
// 每测试独立 database），并预置 fixtures 依赖的 system_accounts 父行。
func openMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		for _, seed := range []string{
			// fixtures 依赖的父行：system_accounts（accounts.system_account_id FK）
			// 与 vendors（accounts.vendor_code FK，fixtures 固定 'gpt'）。
			`INSERT INTO "juhe_business"."system_accounts"
				(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
				VALUES ('sys-1', 'sys-1', 'sys-1', NULL, 'user', 'active', 'hash', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')
				ON CONFLICT (id) DO NOTHING`,
			`INSERT INTO "juhe_business"."vendors"
				(id, code, name, default_supported_models_json, enabled, created_at, updated_at)
				VALUES ('vendor-gpt', 'gpt', 'gpt', '[]', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')
				ON CONFLICT (code) DO NOTHING`,
		} {
			if _, err := db.Exec(seed); err != nil {
				return err
			}
		}
		return nil
	})
	return db
}

// openLegacyDB 打开一个「capability_domain 加列前」形状的业务库（无阶段
// 记录路径依赖，只建迁移面最小表集）。
func openLegacyDB(t *testing.T, name string, capabilityLine string) *sql.DB {
	t.Helper()
	ddl := `
CREATE SCHEMA juhe_business;
CREATE TABLE juhe_business.accounts (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  vendor_code TEXT,
  connection_driver_code TEXT,
  protocol_code TEXT,
  protocol_version TEXT,
  name TEXT,
  type TEXT,
  credentials_encrypted TEXT NOT NULL,
  health_check_model TEXT,
  health_check_endpoint_mode TEXT,
  authorization_instance_source_account_id TEXT REFERENCES juhe_business.accounts(id),
  deleted_at TEXT,
  created_at TEXT NOT NULL DEFAULT '2026-10-01T00:00:00Z',
  updated_at TEXT NOT NULL DEFAULT '2026-10-01T00:00:00Z'
);
CREATE TABLE juhe_business.global_settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '2026-10-01T00:00:00Z');
`
	if capabilityLine != "" {
		ddl += "ALTER TABLE juhe_business.accounts ADD COLUMN " + capabilityLine + ";\n"
	}
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(ddl)
		return err
	})
	return db
}

// accountFixture 是测试账户造数说明。
type accountFixture struct {
	id string
	// modesJSON 是 supported_endpoint_modes 的 JSON 字面量：缺省传 ""（键不存在），
	// 其余原样嵌入凭据 JSON。
	modesJSON string
	// credentialsRaw 非空时直接作为 credentials_encrypted 落库（用于解密失败）。
	credentialsRaw string
	// sourceID 非空时该行是授权实例。
	sourceID string
	// deleted 非空时写入 deleted_at。
	deleted bool
}

func fixtureCredentials(t *testing.T, f accountFixture) string {
	t.Helper()
	if f.credentialsRaw != "" {
		return f.credentialsRaw
	}
	credentials := map[string]any{"api_key": "sk-" + f.id}
	if f.modesJSON != "" {
		var modes any
		if err := json.Unmarshal([]byte(f.modesJSON), &modes); err != nil {
			t.Fatalf("fixture %s modesJSON: %v", f.id, err)
		}
		credentials["supported_endpoint_modes"] = modes
	}
	sealed, err := accountcrypto.EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatalf("encrypt fixture %s: %v", f.id, err)
	}
	return sealed
}

func seedFixtures(t *testing.T, db *sql.DB, fixtures []accountFixture) {
	t.Helper()
	// 孤儿实例夹具（sourceID 指向不存在行）需要外键窗口：PG 无会话级外键
	// 开关，种子前临时解除自引用约束、种子后以 NOT VALID 重建（不校验存量，
	// 语义与 sqlite 关外键窗口一致）。
	const selfFK = "accounts_authorization_instance_source_account_id_fkey"
	if _, err := db.Exec(`ALTER TABLE "juhe_business"."accounts" DROP CONSTRAINT ` + selfFK); err != nil {
		t.Fatalf("drop self FK: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`ALTER TABLE "juhe_business"."accounts" ADD CONSTRAINT ` + selfFK +
			` FOREIGN KEY (authorization_instance_source_account_id) REFERENCES "juhe_business"."accounts"(id) NOT VALID`)
	})
	for _, f := range fixtures {
		deletedAt := "NULL"
		if f.deleted {
			deletedAt = "'2026-10-01T00:00:00Z'"
		}
		source := "NULL"
		if f.sourceID != "" {
			source = "'" + f.sourceID + "'"
		}
		// 目标 schema（设计 §2.1/§9.11）：无 provider_code /
		// provider_protocol_profile_id，身份落 vendor_code + driver。
		statement := `INSERT INTO "juhe_business"."accounts"
			(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
			 name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode,
			 authorization_instance_source_account_id, deleted_at, created_at, updated_at)
			VALUES ('` + f.id + `', 'sys-1', 'gpt', 'gpt', 'openai', 'v1',
			 '` + f.id + `', 'api_key', '` + fixtureCredentials(t, f) + `', 'model-x', 'chat_json',
			 ` + source + `, ` + deletedAt + `, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed account %s: %v", f.id, err)
		}
	}
}

// accountDomain 读单个账户的落库域。
func accountDomain(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var domain string
	if err := db.QueryRow(`SELECT capability_domain FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&domain); err != nil {
		t.Fatalf("read domain of %s: %v", id, err)
	}
	return domain
}

// storedRecord 读阶段记录（无记录返回 ok=false）。
func storedRecord(t *testing.T, db *sql.DB) (capdomain.Record, bool) {
	t.Helper()
	var valueJSON string
	err := db.QueryRow(`SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`, capdomain.MigrationSettingKey).Scan(&valueJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return capdomain.Record{}, false
	}
	if err != nil {
		t.Fatalf("read migration record: %v", err)
	}
	record, err := capdomain.ParseRecord(valueJSON)
	if err != nil {
		t.Fatalf("parse migration record: %v", err)
	}
	return record, true
}

func migrationOptions(target capdomain.Target) Options {
	return Options{Target: target, Secret: testSecret, Now: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)}
}

// TestPlanMigrationStateMachine 与 TestClaimTransitionRace 随阶段记录语义
// 一起移入共享包 stage_test.go（2026-10 M2 下沉）；本文件只保留迁移执行器
// 集成测试。

// TestRunM1EndToEnd 覆盖成功迁移主路径：失败清单阻断完成 → 修复后同阶段
// 恢复重跑收敛 m1_complete；多域推导、实例继承（源在/源软删除）、软删除
// 保域、域内行凭据不变（I1）、混合声明裁剪 + 报告明细、重复执行只报告完成。
func TestRunM1EndToEnd(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat-gpt", modesJSON: `["chat_json","chat_sse","responses_json","responses_sse"]`},
		{id: "acct-chat-xai", modesJSON: `["responses_json","responses_sse"]`},
		{id: "acct-chat-anthropic", modesJSON: `["messages_json","messages_sse","message_token_counting"]`},
		{id: "acct-chat-gemini", modesJSON: `["generate_content_json","generate_content_sse","count_tokens"]`},
		{id: "acct-chat-deleted", modesJSON: `["chat_json"]`, deleted: true},
		{id: "acct-mixed-volc", modesJSON: `["chat_json","video_create","audio_speech"]`},
		{id: "acct-video", modesJSON: `["video_create","video_get","video_content","video_cancel"]`},
		{id: "acct-video-deleted", modesJSON: `["video_create"]`, deleted: true},
		{id: "acct-audio", modesJSON: `["audio_speech"]`},
		{id: "acct-audio-job", modesJSON: `["audio_job_create","audio_job_get"]`},
		{id: "acct-image", modesJSON: `["images_json"]`},
		// 实例：源在 / 源软删除（仍继承）/ 源缺失（失败）。
		{id: "inst-of-chat", sourceID: "acct-chat-gpt", modesJSON: `["chat_json"]`},
		{id: "inst-of-deleted-source", sourceID: "acct-video-deleted"},
		{id: "inst-orphan", sourceID: "acct-not-exists", modesJSON: `["video_create"]`},
	})

	// 密文基线（I1）。
	credentialFingerprint := map[string]string{}
	for _, id := range []string{"acct-chat-gpt", "acct-video", "inst-of-chat"} {
		var sealed string
		if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatalf("read credentials of %s: %v", id, err)
		}
		credentialFingerprint[id] = sealed
	}

	// 首次执行：inst-orphan 源缺失 → 失败清单非空，不写完成阶段。
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("Run m1: %v", err)
	}
	if report.Ready() {
		t.Fatal("源缺失失败清单非空时报告不得 Ready")
	}
	if len(report.Failures) != 1 || report.Failures[0].AccountID != "inst-orphan" || report.Failures[0].Kind != capdomain.FailureInstanceNoSource {
		t.Fatalf("failures = %+v, want 仅 inst-orphan(instance_source_missing)", report.Failures)
	}
	if record, ok := storedRecord(t, db); !ok || record.Stage != capdomain.StageM1Running {
		t.Fatalf("失败后阶段必须保留 m1_running，got %+v ok=%v", record, ok)
	}
	if accountDomain(t, db, "acct-video") != "chat" {
		t.Fatal("失败清单非空时不得写入任何域（存量行保持 DEFAULT 'chat'）")
	}

	// I1：域内行凭据密文逐字节不变（失败路径零写入）。
	for id, before := range credentialFingerprint {
		var sealed string
		if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatalf("re-read credentials of %s: %v", id, err)
		}
		if sealed != before {
			t.Fatalf("I1 违规：%s 凭据密文在迁移中变化", id)
		}
	}

	// 修复：解除孤儿实例关联 → 同阶段恢复重跑收敛完成（全量重读当前数据）。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET authorization_instance_source_account_id = NULL WHERE id = 'inst-orphan'`); err != nil {
		t.Fatalf("detach orphan instance: %v", err)
	}
	report, err = Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("resume Run m1: %v", err)
	}
	if !report.Ready() || report.CompletedStage != capdomain.StageM1Complete {
		t.Fatalf("resume m1 not complete: ready=%v stage=%q failures=%+v", report.Ready(), report.CompletedStage, report.Failures)
	}
	if report.AlreadyComplete {
		t.Fatal("恢复重跑不应标记 alreadyComplete")
	}

	// 落库域断言（含软删除行与实例继承）。
	want := map[string]string{
		"acct-chat-gpt":          "chat",
		"acct-chat-xai":          "chat",
		"acct-chat-anthropic":    "chat",
		"acct-chat-gemini":       "chat",
		"acct-chat-deleted":      "chat",
		"acct-mixed-volc":        "chat",
		"acct-video":             "video",
		"acct-video-deleted":     "video",
		"acct-audio":             "audio",
		"acct-audio-job":         "audio",
		"acct-image":             "image",
		"inst-of-chat":           "chat",  // 继承源
		"inst-of-deleted-source": "video", // 源软删除但行存在 → 仍继承
		"inst-orphan":            "video", // 关联移除后按自身声明推导
	}
	for id, domain := range want {
		if got := accountDomain(t, db, id); got != domain {
			t.Fatalf("account %s domain = %q, want %q", id, got, domain)
		}
	}

	// m1_complete 重复执行只报告完成，不重算窗口期数据（即使凭据已改）。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'inst-orphan'`,
		fixtureCredentials(t, accountFixture{id: "inst-orphan", modesJSON: `["chat_json"]`})); err != nil {
		t.Fatalf("rewrite orphan credentials: %v", err)
	}
	report, err = Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("rerun m1 after complete: %v", err)
	}
	if !report.AlreadyComplete || report.CompletedStage != capdomain.StageM1Complete {
		t.Fatalf("m1_complete 重复执行应只报告完成: %+v", report)
	}
	if got := accountDomain(t, db, "inst-orphan"); got != "video" {
		t.Fatalf("已完成阶段不得重算窗口期数据：inst-orphan domain = %q, want video", got)
	}

	// 赋域 + 声明裁剪：混合行 acct-mixed-volc 裁剪至落域 lane（保留
	// chat_json，裁掉 video_create/audio_speech），报告含脱敏明细与建议归属
	// 域；纯域内行密文在写入轮次同样逐字节不变（I1）。
	if modes := storedModes(t, db, "acct-mixed-volc"); !unorderedModesEqual(modes, []string{"chat_json"}) {
		t.Fatalf("混合行应被裁剪至落域 lane：%v", modes)
	}
	if modes := storedModes(t, db, "acct-chat-gpt"); !unorderedModesEqual(modes, []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}) {
		t.Fatalf("域内行声明不得被裁剪改动：%v", modes)
	}
	for id, before := range credentialFingerprint {
		var sealed string
		if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatalf("re-read credentials of %s: %v", id, err)
		}
		if sealed != before {
			t.Fatalf("I1 违规：域内行 %s 凭据密文在迁移写入中变化", id)
		}
	}
	if len(report.Trimmed) != 0 {
		t.Fatalf("m1_complete 重复执行不得新增裁剪明细：%+v", report.Trimmed)
	}
}

// storedModes 解密库内凭据并返回 supported_endpoint_modes（裁剪断言用）。
func storedModes(t *testing.T, db *sql.DB, id string) []string {
	t.Helper()
	var sealed string
	if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
		t.Fatalf("read credentials of %s: %v", id, err)
	}
	var credentials map[string]any
	if err := accountcrypto.DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("decrypt credentials of %s: %v", id, err)
	}
	raw, _ := credentials["supported_endpoint_modes"].([]any)
	modes := []string{}
	for _, item := range raw {
		if text, ok := item.(string); ok {
			modes = append(modes, text)
		}
	}
	return modes
}

// unorderedModesEqual 判定两个声明集合无序相等。
func unorderedModesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[string]bool{}
	for _, mode := range right {
		seen[mode] = true
	}
	for _, mode := range left {
		if !seen[mode] {
			return false
		}
	}
	return true
}

// TestRunTrimsMixedDeclarationAndReports 覆盖混合声明裁剪主用例（D6 废除后
// 的严格不变式）：chat_json+images_json → chat 域、裁掉 images_json、报告
// 建议归属 image 域；授权实例行按最终落域同样裁剪；其余凭据键原样保留；
// 重复执行（m1_complete）零改动。
func TestRunTrimsMixedDeclarationAndReports(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-mixed-image", modesJSON: `["chat_json","images_json"]`},
		{id: "acct-chat-source", modesJSON: `["chat_json"]`},
		{id: "inst-of-chat", sourceID: "acct-chat-source", modesJSON: `["chat_json","images_json"]`},
	})
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("Run m1: %v", err)
	}
	if !report.Ready() || report.CompletedStage != capdomain.StageM1Complete {
		t.Fatalf("m1 not complete: %+v", report.Failures)
	}
	if accountDomain(t, db, "acct-mixed-image") != "chat" ||
		accountDomain(t, db, "inst-of-chat") != "chat" {
		t.Fatalf("混合行与实例行都应落 chat 域")
	}
	// 裁剪明细：源行与实例行各一条，账户 ID 排序稳定。
	if len(report.Trimmed) != 2 {
		t.Fatalf("报告应含 2 条裁剪明细：%+v", report.Trimmed)
	}
	first, second := report.Trimmed[0], report.Trimmed[1]
	if first.AccountID != "acct-mixed-image" || second.AccountID != "inst-of-chat" {
		t.Fatalf("裁剪明细应按账户 ID 排序：%+v", report.Trimmed)
	}
	for _, trimmed := range []TrimmedDeclaration{first, second} {
		if trimmed.Domain != "chat" || len(trimmed.Removed) != 1 {
			t.Fatalf("裁剪明细形状不符：%+v", trimmed)
		}
		if trimmed.Removed[0].Mode != "images_json" || trimmed.Removed[0].SuggestedDomain != "image" {
			t.Fatalf("被裁值应建议归属 image 域：%+v", trimmed.Removed[0])
		}
	}
	// 库内声明被裁剪至落域 lane；其余凭据键（api_key）原样保留。
	for _, id := range []string{"acct-mixed-image", "inst-of-chat"} {
		if modes := storedModes(t, db, id); !unorderedModesEqual(modes, []string{"chat_json"}) {
			t.Fatalf("%s 声明应被裁剪为 [chat_json]：%v", id, modes)
		}
		var apiKey string
		var sealed string
		if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		var credentials map[string]any
		if err := accountcrypto.DecryptJSON(testSecret, sealed, &credentials); err != nil {
			t.Fatal(err)
		}
		apiKey, _ = credentials["api_key"].(string)
		if apiKey != "sk-"+id {
			t.Fatalf("裁剪不得触碰其余凭据键：%s api_key = %q", id, apiKey)
		}
	}
	// m1_complete 重复执行：只报告完成，零改动（无新增裁剪明细）。
	rerun, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("rerun m1: %v", err)
	}
	if !rerun.AlreadyComplete || len(rerun.Trimmed) != 0 {
		t.Fatalf("重复执行不得重算：%+v", rerun)
	}
	if modes := storedModes(t, db, "acct-mixed-image"); !unorderedModesEqual(modes, []string{"chat_json"}) {
		t.Fatalf("重复执行不得再改声明：%v", modes)
	}
}

// TestRunM1FailureKinds 覆盖缺省/空数组/null/非数组/未知模式/解密失败的
// 失败清单路径：不猜域、不写完成阶段、不写任何域（全有或全无）。
func TestRunM1FailureKinds(t *testing.T) {
	cases := []struct {
		name     string
		fixture  accountFixture
		wantKind capdomain.FailureKind
	}{
		{"缺省 modes", accountFixture{id: "acct-bad", modesJSON: ""}, capdomain.FailureModesMissing},
		{"空数组", accountFixture{id: "acct-bad", modesJSON: `[]`}, capdomain.FailureModesEmpty},
		{"null", accountFixture{id: "acct-bad", modesJSON: `null`}, capdomain.FailureModesNull},
		{"非数组", accountFixture{id: "acct-bad", modesJSON: `"chat_json"`}, capdomain.FailureModesNotArray},
		{"未知模式", accountFixture{id: "acct-bad", modesJSON: `["video_render_3d"]`}, capdomain.FailureModesUnknown},
		{"解密失败", accountFixture{id: "acct-bad", credentialsRaw: "not-a-v1-envelope"}, capdomain.FailureCredentialsDecrypt},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openMigrationDB(t)
			seedFixtures(t, db, []accountFixture{
				{id: "acct-good", modesJSON: `["chat_json"]`},
				testCase.fixture,
			})
			report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if report.Ready() {
				t.Fatal("失败清单非空时报告不得 Ready")
			}
			if len(report.Failures) != 1 || report.Failures[0].Kind != testCase.wantKind || report.Failures[0].AccountID != "acct-bad" {
				t.Fatalf("failures = %+v, want acct-bad(%s)", report.Failures, testCase.wantKind)
			}
			if record, ok := storedRecord(t, db); !ok || record.Stage != capdomain.StageM1Running {
				t.Fatalf("失败后阶段必须保留 m1_running，got %+v", record)
			}
			// 全有或全无：好行也不落域（避免部分成功误读为完成）。
			if accountDomain(t, db, "acct-good") != "chat" {
				t.Fatal("失败清单非空时不得写入任何域（存量行保持 DEFAULT 'chat'）")
			}
		})
	}
}

// TestRunInstanceSourceFailed 覆盖源推导失败 → 实例列依赖失败。
func TestRunInstanceSourceFailed(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-source-bad", modesJSON: `["mystery_mode"]`},
		{id: "inst-of-bad", sourceID: "acct-source-bad", modesJSON: `["chat_json"]`},
	})
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Ready() {
		t.Fatal("源失败时报告不得 Ready")
	}
	found := false
	for _, failure := range report.Failures {
		if failure.AccountID == "inst-of-bad" && failure.Kind == capdomain.FailureInstanceSourceFailed {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures = %+v, want inst-of-bad(instance_source_failed)", report.Failures)
	}
}

// TestRunM1FailureThenRepairResume 覆盖「修复后在同阶段重跑」：失败保留
// m1_running，修复凭据后重跑收敛 m1_complete。
func TestRunM1FailureThenRepairResume(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-good", modesJSON: `["video_create"]`},
		{id: "acct-bad", modesJSON: `[]`},
	})
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 修复：补上合法声明。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'acct-bad'`,
		fixtureCredentials(t, accountFixture{id: "acct-bad", modesJSON: `["images_json"]`})); err != nil {
		t.Fatalf("repair: %v", err)
	}
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if !report.Ready() || report.CompletedStage != capdomain.StageM1Complete {
		t.Fatalf("resume not complete: %+v", report)
	}
	if accountDomain(t, db, "acct-bad") != "image" {
		t.Fatalf("repaired domain = %q, want image", accountDomain(t, db, "acct-bad"))
	}
}

// TestRunM2CutoverLifecycle 覆盖 M2 门禁：无记录拒绝、m1_running 拒绝、
// m1_complete 放行 → m2_finalized，封存后 migrate 拒绝、postflight 推导
// 对账不适用。
func TestRunM2CutoverLifecycle(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat", modesJSON: `["chat_json"]`},
		{id: "acct-video", modesJSON: `["video_create"]`},
		{id: "inst-of-chat", sourceID: "acct-chat"},
	})

	// 无记录：m2-cutover 拒绝。
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("无记录时 m2-cutover 必须拒绝，got err=%v", err)
	}
	// M1 先行。
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1: %v", err)
	}
	// m1_running 阶段拒绝 m2：手工把记录拨回 m1_running 模拟未完成 M1。
	if _, err := db.Exec(`UPDATE "juhe_business"."global_settings" SET value_json = $1 WHERE key = $2`,
		`{"stage":"m1_running","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`, capdomain.MigrationSettingKey); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("m1_running 时 m2-cutover 必须拒绝，got err=%v", err)
	}
	// 拨回 m1_complete 再进 M2。
	if _, err := db.Exec(`UPDATE "juhe_business"."global_settings" SET value_json = $1 WHERE key = $2`,
		`{"stage":"m1_complete","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`, capdomain.MigrationSettingKey); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover))
	if err != nil {
		t.Fatalf("m2-cutover: %v", err)
	}
	if !report.Ready() || report.CompletedStage != capdomain.StageM2Finalized {
		t.Fatalf("m2 not finalized: %+v", report)
	}
	// 封存后：任何 migrate 拒绝。
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err == nil {
		t.Fatal("m2_finalized 后 M1 必须拒绝")
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err == nil {
		t.Fatal("m2_finalized 后 m2-cutover 必须拒绝")
	}
	// 只读核验仍可执行，标记推导对账不适用。
	verify, err := Verify(context.Background(), db, Options{
		Target: capdomain.TargetM2Cutover, Check: CheckPostflight, Secret: testSecret,
		Now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("postflight after finalize: %v", err)
	}
	if !verify.Ready() || !verify.ReconcileSkipped {
		t.Fatalf("封存后 postflight 应跳过推导对账且通过: %+v", verify)
	}
}

// TestPostflightFinalizedInstanceChecks 覆盖封存后 postflight 仍执行的计划
// 承诺检查（按落库现值，推导对账不适用）：
//   - 未篡改路径 Ready（TestRunM2CutoverLifecycle 已覆盖，这里做篡改对照）；
//   - 实例落库域 ≠ 源落库域 → instance_domain_mismatch；
//   - 实例源行缺失 → instance_source_missing。
func TestPostflightFinalizedInstanceChecks(t *testing.T) {
	finalize := func(t *testing.T) *sql.DB {
		t.Helper()
		db := openMigrationDB(t)
		seedFixtures(t, db, []accountFixture{
			{id: "acct-chat", modesJSON: `["chat_json"]`},
			{id: "inst-of-chat", sourceID: "acct-chat"},
		})
		if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
			t.Fatalf("m1: %v", err)
		}
		if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover)); err != nil {
			t.Fatalf("m2-cutover: %v", err)
		}
		return db
	}
	finalizedPostflight := func(t *testing.T, db *sql.DB) VerifyReport {
		t.Helper()
		report, err := Verify(context.Background(), db, Options{
			Target: capdomain.TargetM2Cutover, Check: CheckPostflight, Secret: testSecret,
		})
		if err != nil {
			t.Fatalf("finalized postflight: %v", err)
		}
		return report
	}
	containsFailure := func(report VerifyReport, accountID string, kind capdomain.FailureKind) bool {
		for _, failure := range report.Failures {
			if failure.AccountID == accountID && failure.Kind == kind {
				return true
			}
		}
		return false
	}

	t.Run("未篡改路径通过", func(t *testing.T) {
		db := finalize(t)
		report := finalizedPostflight(t, db)
		if !report.Ready() || !report.ReconcileSkipped {
			t.Fatalf("未篡改封存库应通过且跳过推导对账: %+v", report)
		}
	})

	t.Run("实例落库域与源落库域不一致", func(t *testing.T) {
		db := finalize(t)
		if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET capability_domain = 'audio' WHERE id = 'inst-of-chat'`); err != nil {
			t.Fatal(err)
		}
		report := finalizedPostflight(t, db)
		if report.Ready() {
			t.Fatalf("实例与源落库域不一致必须失败: %+v", report)
		}
		if !containsFailure(report, "inst-of-chat", "instance_domain_mismatch") {
			t.Fatalf("failures 缺少 inst-of-chat(instance_domain_mismatch): %+v", report.Failures)
		}
	})

	t.Run("封存后源行缺失", func(t *testing.T) {
		db := finalize(t)
		// accounts.authorization_instance_source_account_id 自引用外键：
		// 造「源行不存在」的关联先临时解除该约束（PG 无会话级外键开关，
		// 用 ALTER DROP 后重建，语义与 sqlite 关外键窗口一致）。
		if _, err := db.Exec(`ALTER TABLE "juhe_business"."accounts" DROP CONSTRAINT IF EXISTS accounts_authorization_instance_source_account_id_fkey`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET authorization_instance_source_account_id = 'acct-gone' WHERE id = 'inst-of-chat'`); err != nil {
			t.Fatal(err)
		}
		// NOT VALID：PG 重建约束不校验存量违规行（与 sqlite 关外键窗口等价）。
		if _, err := db.Exec(`ALTER TABLE "juhe_business"."accounts" ADD CONSTRAINT accounts_authorization_instance_source_account_id_fkey FOREIGN KEY (authorization_instance_source_account_id) REFERENCES "juhe_business"."accounts"(id) NOT VALID`); err != nil {
			t.Fatal(err)
		}
		report := finalizedPostflight(t, db)
		if report.Ready() {
			t.Fatalf("封存后源行缺失必须失败: %+v", report)
		}
		if !containsFailure(report, "inst-of-chat", "instance_source_missing") {
			t.Fatalf("failures 缺少 inst-of-chat(instance_source_missing): %+v", report.Failures)
		}
	})
}

// TestM2CutoverRecomputesStaleRows 覆盖 M2 停写全量重算语义：窗口期把视频
// 账户凭据改成对话声明后，m1_complete 重复执行不重算，m2-cutover 重算落 chat。
func TestM2CutoverRecomputesStaleRows(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-flip", modesJSON: `["video_create"]`},
		{id: "acct-still", modesJSON: `["chat_json"]`},
	})
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1: %v", err)
	}
	if accountDomain(t, db, "acct-flip") != "video" {
		t.Fatalf("m1 后应为 video，got %q", accountDomain(t, db, "acct-flip"))
	}
	// 窗口期修改（M1 已完成，不会重算）。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'acct-flip'`,
		fixtureCredentials(t, accountFixture{id: "acct-flip", modesJSON: `["chat_json"]`})); err != nil {
		t.Fatalf("window edit: %v", err)
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1 rerun: %v", err)
	}
	if accountDomain(t, db, "acct-flip") != "video" {
		t.Fatalf("m1_complete 重复执行不得重算，got %q", accountDomain(t, db, "acct-flip"))
	}
	report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM2Cutover))
	if err != nil {
		t.Fatalf("m2: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("m2 not ready: %+v", report.Failures)
	}
	if accountDomain(t, db, "acct-flip") != "chat" {
		t.Fatalf("M2 全量重算应落 chat，got %q", accountDomain(t, db, "acct-flip"))
	}
}

// TestParallelSingleExecutor 覆盖并行单执行者：两个并发首次 Run 在 SQLite
// 条件更新下串行化——完成阶段（m1_running → m1_complete 跃迁）恰好被一个
// 执行者写入；另一执行者或以并行错误放弃，或观察到 alreadyComplete。
func TestParallelSingleExecutor(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat", modesJSON: `["chat_json"]`},
	})
	var (
		wg            sync.WaitGroup
		mu            sync.Mutex
		runErrors     []error
		completedRuns int
		alreadyRun    int
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				runErrors = append(runErrors, err)
				return
			}
			if report.AlreadyComplete {
				alreadyRun++
				return
			}
			if report.Ready() {
				completedRuns++
			}
		}()
	}
	wg.Wait()
	// 不变式：完成跃迁恰好执行一次；并发竞争者的失败（若有）必须以并行/
	// 阶段竞争错误显式报告，而不是静默成功。
	if completedRuns != 1 {
		t.Fatalf("完成跃迁应恰好一次：completed=%d already=%d errs=%v", completedRuns, alreadyRun, runErrors)
	}
	for _, err := range runErrors {
		if !strings.Contains(err.Error(), "并行") && !strings.Contains(err.Error(), "完成阶段条件更新未命中") {
			t.Fatalf("并发竞争者的错误应指明单执行者约束：%v", err)
		}
	}
	if record, ok := storedRecord(t, db); !ok || record.Stage != capdomain.StageM1Complete {
		t.Fatalf("完成阶段应恰好写入一次，got %+v", record)
	}
	if accountDomain(t, db, "acct-chat") != "chat" {
		t.Fatal("迁移后域应为 chat")
	}
}

// TestRunCorruptRecordRejected 覆盖阶段记录损坏：stage 枚举外/ruleVersion
// 不符原样报错，不执行迁移。
func TestRunCorruptRecordRejected(t *testing.T) {
	cases := []struct {
		name      string
		valueJSON string
	}{
		{"stage 枚举外", `{"stage":"m3_running","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`},
		{"ruleVersion 不符", `{"stage":"m1_running","ruleVersion":2,"updatedAt":"2026-10-12T08:00:00Z"}`},
		{"非 JSON", `not-json`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openMigrationDB(t)
			seedFixtures(t, db, []accountFixture{{id: "acct-chat", modesJSON: `["chat_json"]`}})
			if _, err := db.Exec(`INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`, capdomain.MigrationSettingKey, testCase.valueJSON); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err == nil {
				t.Fatal("损坏记录必须拒绝执行")
			}
		})
	}
}

// TestRunAddsMissingColumn 覆盖执行协议「加列并校验形状」：存量库缺列时
// 迁移命令补列（存量行落 'chat'），形状错误时原样拒绝。
func TestRunAddsMissingColumn(t *testing.T) {
	legacyDB := openLegacyDB(t, "legacy.sqlite3", "")
	seedFixtures(t, legacyDB, []accountFixture{{id: "acct-chat", modesJSON: `["chat_json"]`}})
	report, err := Run(context.Background(), legacyDB, migrationOptions(capdomain.TargetM1))
	if err != nil {
		t.Fatalf("Run over legacy db: %v", err)
	}
	if !report.Ready() || report.CompletedStage != capdomain.StageM1Complete {
		t.Fatalf("legacy run not complete: %+v", report)
	}
	if accountDomain(t, legacyDB, "acct-chat") != "chat" {
		t.Fatal("补列后存量行应落 'chat'")
	}

	brokenDB := openLegacyDB(t, "broken.sqlite3", "capability_domain TEXT")
	seedFixtures(t, brokenDB, []accountFixture{{id: "acct-chat", modesJSON: `["chat_json"]`}})
	if _, err := Run(context.Background(), brokenDB, migrationOptions(capdomain.TargetM1)); err == nil || !strings.Contains(err.Error(), "形状不符") {
		t.Fatalf("错误形状必须原样报错，got err=%v", err)
	}
}

// TestVerifyPreflightAndPostflight 覆盖只读核验两模式：
// preflight 允许缺列（预计赋域 N 个账户是正常结果）；postflight 要求列存在。
func TestVerifyPreflightAndPostflight(t *testing.T) {
	db := openLegacyDB(t, "legacy-verify.sqlite3", "")
	seedFixtures(t, db, []accountFixture{
		{id: "acct-chat", modesJSON: `["chat_json"]`},
		{id: "acct-bad", modesJSON: `[]`},
		{id: "inst-of-chat", sourceID: "acct-chat"},
	})
	preflight, err := Verify(context.Background(), db, Options{
		Target: capdomain.TargetM1, Check: CheckPreflight, Secret: testSecret,
		Now: time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if preflight.ColumnPresent {
		t.Fatal("preflight fixture 应缺列")
	}
	if preflight.PendingMigration != 3 {
		t.Fatalf("preflight pending = %d, want 3（预计需要赋域 N 个账户是正常结果）", preflight.PendingMigration)
	}
	if len(preflight.Failures) != 1 || preflight.Failures[0].Kind != capdomain.FailureModesEmpty {
		t.Fatalf("preflight failures = %+v", preflight.Failures)
	}
	// postflight 缺列必须失败。
	if _, err := Verify(context.Background(), db, Options{
		Target: capdomain.TargetM1, Check: CheckPostflight, Secret: testSecret,
	}); err == nil || !strings.Contains(err.Error(), "postflight") {
		t.Fatalf("缺列 postflight 必须报错，got %v", err)
	}
	// 修复失败声明后迁移，postflight 才能通过（失败清单清零是门禁）。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'acct-bad'`,
		fixtureCredentials(t, accountFixture{id: "acct-bad", modesJSON: `["chat_json"]`})); err != nil {
		t.Fatalf("repair acct-bad: %v", err)
	}
	if _, err := Run(context.Background(), db, migrationOptions(capdomain.TargetM1)); err != nil {
		t.Fatalf("m1: %v", err)
	}
	postflight, err := Verify(context.Background(), db, Options{
		Target: capdomain.TargetM1, Check: CheckPostflight, Secret: testSecret,
	})
	if err != nil {
		t.Fatalf("postflight: %v", err)
	}
	if !postflight.Ready() || !postflight.ColumnPresent {
		t.Fatalf("postflight not ready: %+v", postflight)
	}
	// 篡改一个域值 → postflight 报不一致。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET capability_domain = 'video' WHERE id = 'acct-chat'`); err != nil {
		t.Fatal(err)
	}
	postflight, err = Verify(context.Background(), db, Options{
		Target: capdomain.TargetM1, Check: CheckPostflight, Secret: testSecret,
	})
	if err != nil {
		t.Fatalf("postflight after tamper: %v", err)
	}
	if postflight.Ready() || len(postflight.Failures) == 0 {
		t.Fatalf("篡改后 postflight 必须失败: %+v", postflight)
	}
}

// TestVerifyPreflightWithColumn 覆盖列已存在时的 preflight：差异只作为
// 待迁移项计数，不作为失败。
func TestVerifyPreflightWithColumn(t *testing.T) {
	db := openMigrationDB(t)
	seedFixtures(t, db, []accountFixture{
		{id: "acct-video", modesJSON: `["video_create"]`},
		{id: "acct-chat", modesJSON: `["chat_json"]`},
	})
	// 手工把两行拨到默认 'chat'，模拟「加列后未迁移」的中间状态。
	report, err := Verify(context.Background(), db, Options{
		Target: capdomain.TargetM1, Check: CheckPreflight, Secret: testSecret,
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("待迁移差异不是失败: %+v", report.Failures)
	}
	if report.PendingMigration != 1 || report.AlreadyMatching != 1 {
		t.Fatalf("pending=%d matching=%d, want 1/1", report.PendingMigration, report.AlreadyMatching)
	}
	if report.DomainsExpected["video"] != 1 || report.DomainsExpected["chat"] != 1 {
		t.Fatalf("domainsExpected = %+v", report.DomainsExpected)
	}
}

// TestSealFreshInstall 覆盖全新空库自动封存（PLAN §全新空库自动封存）：
// 空库（无记录且 accounts 无行）按序写 m2_finalized 且幂等；accounts 非空
// 的库一律不自动写记录；记录已存在直接跳过（真实切换必须显式跑迁移命令）。
func TestSealFreshInstall(t *testing.T) {
	t.Run("空库自动封存且幂等", func(t *testing.T) {
		db := openMigrationDB(t)
		sealed, err := SealFreshInstall(context.Background(), db)
		if err != nil {
			t.Fatalf("seal fresh install: %v", err)
		}
		if !sealed {
			t.Fatal("空库首次封存必须返回 sealed=true")
		}
		record, ok := storedRecord(t, db)
		if !ok || record.Stage != capdomain.StageM2Finalized || record.RuleVersion != capdomain.RuleVersion {
			t.Fatalf("空库封存后阶段记录应为 m2_finalized，got %+v ok=%v", record, ok)
		}
		// 重复执行（ensure/bootstrap 幂等重入）：跳过且不改写记录。
		sealedAgain, err := SealFreshInstall(context.Background(), db)
		if err != nil {
			t.Fatalf("re-seal: %v", err)
		}
		if sealedAgain {
			t.Fatal("记录已存在时重复封存必须返回 sealed=false")
		}
		if record2, ok := storedRecord(t, db); !ok || record2.UpdatedAt != record.UpdatedAt {
			t.Fatalf("重复封存不得改写已有记录：%+v vs %+v", record2, record)
		}
	})
	t.Run("非空库不自动写记录", func(t *testing.T) {
		db := openMigrationDB(t)
		seedFixtures(t, db, []accountFixture{{id: "acct-chat", modesJSON: `["chat_json"]`}})
		sealed, err := SealFreshInstall(context.Background(), db)
		if err != nil {
			t.Fatalf("seal non-empty db: %v", err)
		}
		if sealed {
			t.Fatal("accounts 非空的库不得自动写阶段记录")
		}
		if _, ok := storedRecord(t, db); ok {
			t.Fatal("非空库不得出现阶段记录（真实切换必须显式跑迁移命令）")
		}
		if domain := accountDomain(t, db, "acct-chat"); domain != "chat" {
			t.Fatalf("非空库域值不得被改动，got %q", domain)
		}
	})
}
