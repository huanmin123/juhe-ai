package modelcheckowner

// w11e 小文件错误臂：质量管理 CAS、模型上限、结果游标、健康时区、
// 配置加载、模型映射与证据聚合的剩余分支。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func w11eQualityDDL() []string {
	return []string{
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,action TEXT,state TEXT,recovery_due_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,revision INTEGER,next_run_at TEXT,created_at TEXT,updated_at TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,custom_question_ids TEXT,UNIQUE(system_account_id,account_id))`,
	}
}

func w11eQualityManager(t *testing.T) (*BusinessQualityManager, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/w11e-quality.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range w11eQualityDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("DDL 失败: %v", err)
		}
	}
	manager, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	return manager, db
}

func w11eInt(value int) *int          { return &value }
func w11eString(value string) *string { return &value }
func w11eBool(value bool) *bool       { return &value }

func TestW11EQualityManagerPolicyArms(t *testing.T) {
	if _, err := NewBusinessQualityManager(nil, false); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("nil 数据库必须拒绝: %v", err)
	}
	manager, db := w11eQualityManager(t)
	if _, err := manager.Policy(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "system account is required") {
		t.Fatalf("空系统账户必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Policy(canceled, "sys-1"); err == nil {
		t.Fatal("canceled 查询必须失败")
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-1',3,'quick',1,82,'fallback',15,'2026-09-16T10:00:00Z','2026-09-16T10:00:00Z',NULL)`); err != nil {
		t.Fatal(err)
	}
	view, err := manager.Policy(context.Background(), "sys-1")
	if err != nil || view.Revision != 3 || !view.ManualEnforcementEnabled {
		t.Fatalf("策略读取=%+v err=%v", view, err)
	}
	// 非法 patch。
	if _, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: -1}); err == nil || !strings.Contains(err.Error(), "revision 无效") {
		t.Fatalf("非法 revision 必须拒绝: %v", err)
	}
	if _, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: 3}); err == nil || !strings.Contains(err.Error(), "没有变化") {
		t.Fatalf("空 patch 必须拒绝: %v", err)
	}
	if _, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: 3, Profile: w11eString("medium")}); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("非法 profile 必须拒绝: %v", err)
	}
	if _, err := manager.PatchPolicy(canceled, "sys-1", QualityPolicyPatch{ExpectedRevision: 3, PenaltyThreshold: w11eInt(90)}); err == nil {
		t.Fatal("canceled 事务必须失败")
	}
	// revision 失配。
	if _, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: 2, PenaltyThreshold: w11eInt(90)}); err == nil || !strings.Contains(err.Error(), "已被其他操作修改") {
		t.Fatalf("revision 失配必须拒绝: %v", err)
	}
	// 字段更新：每个字段独立应用。
	updated, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: 3, Profile: w11eString("full"), ManualEnforcementEnabled: w11eBool(false), PenaltyThreshold: w11eInt(90), PenaltyAction: w11eString("disable"), RecoveryIntervalMinutes: w11eInt(30)})
	if err != nil || updated.Revision != 4 || updated.Profile != "full" || updated.ManualEnforcementEnabled || updated.PenaltyThreshold != 90 || updated.PenaltyAction != "disable" || updated.RecoveryIntervalMinutes != 30 {
		t.Fatalf("全字段更新=%+v err=%v", updated, err)
	}
	// 无变化 patch 直接提交。
	same, err := manager.PatchPolicy(context.Background(), "sys-1", QualityPolicyPatch{ExpectedRevision: 4, PenaltyThreshold: w11eInt(90)})
	if err != nil || same.Revision != 4 {
		t.Fatalf("无变化 patch=%+v err=%v", same, err)
	}
	// 策略行不存在时从默认值插入。
	inserted, err := manager.PatchPolicy(context.Background(), "sys-2", QualityPolicyPatch{ExpectedRevision: 0, PenaltyThreshold: w11eInt(75)})
	if err != nil || inserted.Revision != 1 || inserted.PenaltyThreshold != 75 {
		t.Fatalf("默认插入=%+v err=%v", inserted, err)
	}
	// 无变化且行不存在 → policyTx ErrNoRows 分支提交默认视图。
	empty, err := manager.PatchPolicy(context.Background(), "sys-3", QualityPolicyPatch{ExpectedRevision: 0, PenaltyThreshold: w11eInt(70)})
	if err != nil || empty.Revision != 0 || empty.PenaltyThreshold != 70 {
		t.Fatalf("默认视图=%+v err=%v", empty, err)
	}
}

func TestW11EQualityManagerScheduleArms(t *testing.T) {
	manager, db := w11eQualityManager(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','Account','openai','profile_openai_openai_v1',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-sol')`); err != nil {
		t.Fatal(err)
	}
	// 非法输入。
	if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: " ", Model: "m", IntervalMinutes: 10, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空账户必须拒绝: %v", err)
	}
	if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct-1", Model: "gpt-5.6-sol", IntervalMinutes: 5, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "间隔") {
		t.Fatalf("非法间隔必须拒绝: %v", err)
	}
	// 账户不存在。
	if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "w11e-missing", Model: "gpt-5.6-sol", IntervalMinutes: 10, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "账户不存在") {
		t.Fatalf("缺账户必须拒绝: %v", err)
	}
	// 模型不受支持。
	if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct-1", Model: "w11e-model", IntervalMinutes: 10, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("不支持模型必须拒绝: %v", err)
	}
	created, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct-1", Model: "gpt-5.6-sol", IntervalMinutes: 30, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10})
	if err != nil || created.ID == "" {
		t.Fatalf("创建计划=%+v err=%v", created, err)
	}
	// 同账户重复。
	if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct-1", Model: "gpt-5.6-sol", IntervalMinutes: 30, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复计划必须拒绝: %v", err)
	}
	// 列表与分页边界。
	list, err := manager.ListSchedules(ctx, "sys-1", 0, 0)
	if err != nil || list.Total != 1 || list.Page != 1 || list.PageSize != 50 || list.HasMore {
		t.Fatalf("列表=%+v err=%v", list, err)
	}
	// 补充 last_run 字段与 enforcement 联接。
	if _, err := db.Exec(`UPDATE model_quality_schedules SET last_run_id='run-1',last_run_at='2026-09-16T09:00:00Z',last_run_status='completed' WHERE account_id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements VALUES ('acct-1','fallback','active','2026-09-16T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	list, err = manager.ListSchedules(ctx, "sys-1", 1, 1)
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("联接列表=%+v err=%v", list, err)
	}
	item := list.Items[0]
	if item.LastRunID == nil || *item.LastRunID != "run-1" || item.LastRunAt == nil || item.LastRunStatus == nil || item.CurrentEnforcementAction != "fallback" || item.CurrentEnforcementRecoveryDueAt != "2026-09-16T12:00:00Z" || item.AccountName != "Account" || item.ProviderCode != "openai" {
		t.Fatalf("联接字段=%+v", item)
	}
	// patch 校验。
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 0}); err == nil || !strings.Contains(err.Error(), "revision 无效") {
		t.Fatalf("非法 patch revision 必须拒绝: %v", err)
	}
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 1}); err == nil || !strings.Contains(err.Error(), "没有变化") {
		t.Fatalf("空 patch 必须拒绝: %v", err)
	}
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 1, Model: w11eString(" ")}); err == nil || !strings.Contains(err.Error(), "模型不能为空") {
		t.Fatalf("空模型必须拒绝: %v", err)
	}
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: w11eInt(5)}); err == nil || !strings.Contains(err.Error(), "间隔") {
		t.Fatalf("非法间隔必须拒绝: %v", err)
	}
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 1, Profile: w11eString("medium")}); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("非法 profile 必须拒绝: %v", err)
	}
	// revision 失配与不存在。
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 9, PenaltyThreshold: w11eInt(80)}); err == nil || !strings.Contains(err.Error(), "已变化") {
		t.Fatalf("revision 失配必须拒绝: %v", err)
	}
	if _, err := manager.PatchSchedule(ctx, "sys-1", "w11e-missing", QualitySchedulePatch{ExpectedRevision: 1, PenaltyThreshold: w11eInt(80)}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("缺计划必须拒绝: %v", err)
	}
	// 字段级更新 + 无变化短路（先放开 terra 模型）。
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	patched, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 1, Model: w11eString("gpt-5.6-terra"), IntervalMinutes: w11eInt(45), Profile: w11eString("full"), PenaltyThreshold: w11eInt(80), PenaltyAction: w11eString("disable"), RecoveryIntervalMinutes: w11eInt(20), Enabled: w11eBool(false)})
	if err != nil || patched.Revision != 2 || patched.Model != "gpt-5.6-terra" || patched.IntervalMinutes != 45 || patched.Profile != "full" || patched.PenaltyThreshold != 80 || patched.PenaltyAction != "disable" || patched.RecoveryIntervalMinutes != 20 || patched.Enabled {
		t.Fatalf("计划更新=%+v err=%v", patched, err)
	}
	unchanged, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 2, PenaltyThreshold: w11eInt(80)})
	if err != nil || unchanged.Revision != 2 {
		t.Fatalf("无变化 patch=%+v err=%v", unchanged, err)
	}
	// 更新到不受支持模型。
	if _, err := manager.PatchSchedule(ctx, "sys-1", item.ID, QualitySchedulePatch{ExpectedRevision: 2, Model: w11eString("w11e-unsupported")}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("不支持模型更新必须拒绝: %v", err)
	}
	// 删除与不存在删除。
	deleted, err := manager.DeleteSchedule(ctx, "sys-1", item.ID)
	if err != nil || !deleted {
		t.Fatalf("删除=%t err=%v", deleted, err)
	}
	deleted, err = manager.DeleteSchedule(ctx, "sys-1", item.ID)
	if err != nil || deleted {
		t.Fatalf("重复删除=%t err=%v", deleted, err)
	}
	// 表缺失时的查询错误。
	if _, err := db.Exec(`DROP TABLE model_quality_schedules`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ListSchedules(ctx, "sys-1", 1, 10); err == nil {
		t.Fatal("表缺失列表必须失败")
	}
}

func TestW11EModelLimitsArms(t *testing.T) {
	if _, err := NewVersionedModelLimits(nil, false); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("nil 数据库必须拒绝: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/w11e-limits.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE provider_model_catalog (provider_code TEXT,model TEXT,status TEXT,catalog_visible INTEGER,context_window_tokens INTEGER,max_input_tokens INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_model_catalog VALUES ('openai','gpt-5.6-sol','active',1,200000,120000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_model_catalog VALUES ('openai','gpt-5.6-terra','active',1,200000,0)`); err != nil {
		t.Fatal(err)
	}
	limits, err := NewVersionedModelLimits(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if limits.Version() != "business-provider-model-catalog-v1" {
		t.Fatalf("版本=%q", limits.Version())
	}
	tokens, err := limits.MaxInputTokens("openai", "gpt-5.6-sol", modelcheckprofile.ProtocolOpenAIResponses)
	if err != nil || tokens != 120000 {
		t.Fatalf("max tokens=%d err=%v", tokens, err)
	}
	tokens, err = limits.MaxInputTokens("openai", "gpt-5.6-terra", modelcheckprofile.ProtocolOpenAIResponses)
	if err != nil || tokens != 200000 {
		t.Fatalf("回退窗口=%d err=%v", tokens, err)
	}
	var nilLimits *VersionedModelLimits
	if _, err := nilLimits.MaxInputTokens("openai", "m", modelcheckprofile.ProtocolOpenAIResponses); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("nil 查询必须拒绝: %v", err)
	}
	if _, err := limits.MaxInputTokens(" ", "m", modelcheckprofile.ProtocolOpenAIResponses); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("空 provider 必须拒绝: %v", err)
	}
	if _, err := limits.MaxInputTokens("openai", " ", modelcheckprofile.ProtocolOpenAIResponses); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("空模型必须拒绝: %v", err)
	}
	if _, err := limits.MaxInputTokens("openai", "w11e-missing", modelcheckprofile.ProtocolOpenAIResponses); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("缺模型必须拒绝: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO provider_model_catalog VALUES ('openai','gpt-5.6-bad','active',1,0,-5)`); err != nil {
		t.Fatal(err)
	}
	if _, err := limits.MaxInputTokens("openai", "gpt-5.6-bad", modelcheckprofile.ProtocolOpenAIResponses); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("非法快照必须拒绝: %v", err)
	}
}

func TestW11EOutcomeCursorAndHealthTimeArms(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.ListCommittedOutcomes(context.Background(), OutcomeCursor{}, 10); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("nil store 必须拒绝: %v", err)
	}
	store := newRuntimeTestStore(t)
	defer store.Close()
	if _, err := store.ListCommittedOutcomes(context.Background(), OutcomeCursor{}, 0); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("非法 limit 必须拒绝: %v", err)
	}
	if _, err := store.ListCommittedOutcomes(context.Background(), OutcomeCursor{OutcomeID: "x"}, 10); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("不完整游标必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListCommittedOutcomes(canceled, OutcomeCursor{}, 10); err == nil {
		t.Fatal("canceled 查询必须失败")
	}
	// 健康时区。
	if _, err := LoadBusinessHealthStatHour(context.Background(), nil, false); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil 数据库必须拒绝: %v", err)
	}
	path := t.TempDir() + "/w11e-settings.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT,key TEXT,value_json TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBusinessHealthStatHour(context.Background(), db, false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("缺设置必须拒绝: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin','usageStatsTimezone','not-json','2026-09-16T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBusinessHealthStatHour(context.Background(), db, false); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("非法 JSON 必须拒绝: %v", err)
	}
	if _, err := db.Exec(`UPDATE system_settings SET value_json='"Asia/Shanghai"' WHERE key='usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	formatter, err := LoadBusinessHealthStatHour(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	hour, err := formatter(time.Date(2026, 9, 16, 3, 30, 0, 0, time.UTC))
	if err != nil || hour != "2026-09-16T11" {
		t.Fatalf("上海时区小时=%q err=%v", hour, err)
	}
	if _, err := NewHealthStatHourFunc("  "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("空时区必须拒绝: %v", err)
	}
	if _, err := NewHealthStatHourFunc("Not/AZone"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("非法时区必须拒绝: %v", err)
	}
	valid, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := valid(time.Time{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("零值时间必须拒绝: %v", err)
	}
	unconfigured := &Store{}
	if _, err := unconfigured.formatHealthStatHour(time.Now()); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("未配置时区必须拒绝: %v", err)
	}
	if validNodeHealthStatHour("2026-09-16T10") != true || validNodeHealthStatHour("2026-9-16T10") || validNodeHealthStatHour("garbage") {
		t.Fatal("Node 健康小时键校验错误")
	}
}

func TestW11ELoadConfigArms(t *testing.T) {
	// 2026-09-21 起无总开关：空 env 也返回 Enabled=true 的自动认领配置。
	cfgEmpty, err := LoadConfig(func(string) string { return "" })
	if err != nil || !cfgEmpty.Enabled || !cfgEmpty.AutoClaimed {
		t.Fatalf("空 env 必须得到默认常驻配置: cfg=%+v err=%v", cfgEmpty, err)
	}
	getenv := func(overrides map[string]string) func(string) string {
		return func(key string) string {
			if value, ok := overrides[key]; ok {
				return value
			}
			return ""
		}
	}
	// 2026-09-20 零配置自动认领：owner/instance/store/PG URL 都有默认或回退，
	// 仅显式非法 owner 仍拒绝。
	if _, err := LoadConfig(getenv(map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "jobs"})); err == nil || !strings.Contains(err.Error(), "OWNER") {
		t.Fatalf("非法 owner 必须拒绝: %v", err)
	}
	zero := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_DATABASE_DRIVER": "postgres", "JUHE_AI_POSTGRES_URL": "postgres://w11e.invalid/main"}
	cfg, err := LoadConfig(getenv(zero))
	if err != nil || !cfg.AutoClaimed {
		t.Fatalf("零配置 postgres 必须自动认领: cfg=%+v err=%v", cfg, err)
	}
	if cfg.StoreMode != "postgres" || cfg.PostgresURL != "postgres://w11e.invalid/main" || cfg.BusinessPostgresURL != "postgres://w11e.invalid/main" {
		t.Fatalf("postgres URL 回退=%+v", cfg)
	}
	explicit := map[string]string{
		"JUHE_AI_J3B_ENABLED":               "true",
		"JUHE_AI_J3B_STORE":                 "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":          "postgres://w11e.invalid/db",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://w11e.invalid/business",
	}
	cfgExplicit, err := LoadConfig(getenv(explicit))
	if err != nil {
		t.Fatalf("显式 PG URL 必须优先: %v", err)
	}
	if cfgExplicit.PostgresURL != "postgres://w11e.invalid/db" || cfgExplicit.BusinessPostgresURL != "postgres://w11e.invalid/business" {
		t.Fatalf("显式 PG URL=%+v", cfgExplicit)
	}
}

func TestW11EModelMappingAndEvidenceArms(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/w11e-mapping.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	profile, _ := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	// db nil 或模型不受 profile 支持时返回空解析。
	if resolution, err := resolveConfiguredUpstreamModelMapping(context.Background(), nil, false, "acct", profile, "gpt-5.6-sol"); err != nil || resolution.UpstreamModel != "" {
		t.Fatalf("nil db 解析=%+v err=%v", resolution, err)
	}
	if resolution, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct", profile, "w11e-unsupported"); err != nil || resolution.UpstreamModel != "" {
		t.Fatalf("不支持模型解析=%+v err=%v", resolution, err)
	}
	// 映射行缺 family。
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','','',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol"); err == nil || !strings.Contains(err.Error(), "family is missing") {
		t.Fatalf("缺 family 必须拒绝: %v", err)
	}
	// 映射查询表缺失。
	if _, err := db.Exec(`DROP TABLE account_model_mappings`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfiguredUpstreamModelMapping(context.Background(), db, false, "acct-1", profile, "gpt-5.6-sol"); err == nil || !strings.Contains(err.Error(), "model mapping") {
		t.Fatalf("表缺失必须失败: %v", err)
	}
	// 证据聚合边臂（使用契约 family 名）。
	items := []map[string]any{
		{"kind": "behavior_probe", "status": "passed", "evidence": map[string]any{}},
		{"kind": "juice", "status": "skipped", "evidence": map[string]any{"partial": true}},
		{"kind": "identity_observation", "status": "passed", "evidence": map[string]any{"completedProbeCount": 1.0, "requiredProbeCount": 2.0}},
		{"kind": "token_integrity", "status": "passed", "evidence": map[string]any{}, "score": 15, "maxScore": 10},
	}
	aggregate := AggregateEvidence(items)
	if aggregate.Formed || len(aggregate.Partial) != 1 || len(aggregate.Invalid) != 2 {
		t.Fatalf("聚合=%+v", aggregate)
	}
	if aggregate.MaxScore != 40 || aggregate.Score > 40 {
		t.Fatalf("分数钳制错误: %+v", aggregate)
	}
	if evidenceIncomplete(map[string]any{"requestFailure": true}) != true {
		t.Fatal("requestFailure 必须判为不完整")
	}
	if evidenceIncomplete(map[string]any{"evidenceInsufficient": true}) != true {
		t.Fatal("evidenceInsufficient 必须判为不完整")
	}
	if evidenceIncomplete(map[string]any{"excludedFromScoring": true}) != true {
		t.Fatal("excludedFromScoring 必须判为不完整")
	}
	if evidenceIncomplete("not-a-map") {
		t.Fatal("非 map 不得判为不完整")
	}
	if evidenceIncomplete(map[string]any{}) {
		t.Fatal("空 map 不得判为不完整")
	}
}
