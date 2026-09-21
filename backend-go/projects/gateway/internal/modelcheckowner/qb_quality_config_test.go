package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
	_ "modernc.org/sqlite"
)

// fakeQuestionVerifier 是质量配置写入侧题库验证端口的 fake：只按
// approved 集合回答存在性，可注入读取失败。
type fakeQuestionVerifier struct {
	approved   map[string]bool
	err        error
	lastStatus string
	lastIDs    []string
}

func (f *fakeQuestionVerifier) ListByIDs(_ context.Context, ids []string, status string) ([]modelcheckquestionbank.Question, error) {
	f.lastStatus, f.lastIDs = status, append([]string(nil), ids...)
	if f.err != nil {
		return nil, f.err
	}
	questions := make([]modelcheckquestionbank.Question, 0, len(ids))
	for _, id := range ids {
		// 照 *Store.ListByIDs 的契约对入参 trim 后匹配。
		id = strings.TrimSpace(id)
		if f.approved[id] {
			questions = append(questions, modelcheckquestionbank.Question{ID: id, Status: modelcheckquestionbank.StatusApproved})
		}
	}
	return questions, nil
}

func qbQualityFixture(t *testing.T) (*BusinessQualityManager, *sql.DB, *fakeQuestionVerifier) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/quality.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,created_at TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,enabled INTEGER,revision INTEGER,next_run_at TEXT,created_at TEXT,updated_at TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,custom_question_ids TEXT,UNIQUE(system_account_id,account_id))`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,action TEXT,state TEXT,recovery_due_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct','sys','Primary account','openai','profile_openai_openai_v1',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	manager, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeQuestionVerifier{approved: map[string]bool{"q1": true, "q2": true, "q3": true}}
	manager.SetQuestionBankVerifier(verifier)
	return manager, db, verifier
}

func qbPtrString(v string) *string    { return &v }
func qbPtrIds(ids []string) *[]string { return &ids }
func qbIntPtr(v int) *int             { return &v }

// TestQBQualityPolicyCustomQuestionIds 覆盖 policy 写侧校验、PATCH diff 语义、
// 乐观锁与读取侧容错。
func TestQBQualityPolicyCustomQuestionIds(t *testing.T) {
	manager, db, verifier := qbQualityFixture(t)
	ctx := context.Background()

	// 默认（无行）policy：customQuestionIds 为空数组。
	policy, err := manager.Policy(ctx, "sys")
	if err != nil || policy.CustomQuestionIds == nil || len(policy.CustomQuestionIds) != 0 {
		t.Fatalf("default policy ids=%+v err=%v", policy.CustomQuestionIds, err)
	}

	// 写入两题：校验端口收到 approved 状态过滤；视图与落库一致。
	ids := []string{"q1", "q2"}
	policy, err = manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, CustomQuestionIds: &ids})
	if err != nil || len(policy.CustomQuestionIds) != 2 {
		t.Fatalf("patch ids policy=%+v err=%v", policy, err)
	}
	if verifier.lastStatus != modelcheckquestionbank.StatusApproved {
		t.Fatalf("verifier status=%s, want approved", verifier.lastStatus)
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT custom_question_ids FROM model_quality_policies WHERE system_account_id='sys'`).Scan(&stored); err != nil || !stored.Valid || stored.String != `["q1","q2"]` {
		t.Fatalf("stored=%q valid=%v err=%v", stored.String, stored.Valid, err)
	}
	reread, err := manager.Policy(ctx, "sys")
	if err != nil || len(reread.CustomQuestionIds) != 2 || reread.CustomQuestionIds[0] != "q1" {
		t.Fatalf("reread=%+v err=%v", reread, err)
	}

	// PATCH 缺省 = 不修改（revision 不前进、值保持）。
	unchanged, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 1, PenaltyThreshold: qbIntPtr(72)})
	if err != nil || len(unchanged.CustomQuestionIds) != 2 || unchanged.Revision != 2 {
		t.Fatalf("unchanged policy=%+v err=%v", unchanged, err)
	}

	// PATCH 空数组 = 清空（列落 NULL）。
	empty := []string{}
	cleared, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 2, CustomQuestionIds: &empty})
	if err != nil || len(cleared.CustomQuestionIds) != 0 {
		t.Fatalf("cleared policy=%+v err=%v", cleared, err)
	}
	if err := db.QueryRow(`SELECT custom_question_ids FROM model_quality_policies WHERE system_account_id='sys'`).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("cleared stored valid=%v err=%v", stored.Valid, err)
	}

	// 超限 / 未通过审核 / 端口失败 / 端口未装配，全部 fail-closed。
	four := []string{"q1", "q2", "q3", "q4"}
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 3, CustomQuestionIds: &four}); err == nil || !strings.Contains(err.Error(), "最多选择 3 个") {
		t.Fatalf("four ids err=%v", err)
	}
	disallowed := []string{"q1", "rejected-id"}
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 3, CustomQuestionIds: &disallowed}); err == nil || !strings.Contains(err.Error(), "题库题目不可用或未通过审核: rejected-id") {
		t.Fatalf("unapproved id err=%v", err)
	}
	verifier.err = errors.New("boom")
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 3, CustomQuestionIds: &disallowed}); err == nil || !strings.Contains(err.Error(), "题库题目不可用或未通过审核") {
		t.Fatalf("verifier failure err=%v", err)
	}
	verifier.err = nil
	manager.SetQuestionBankVerifier(nil)
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 3, CustomQuestionIds: &disallowed}); err == nil || !strings.Contains(err.Error(), "题库校验服务未装配") {
		t.Fatalf("nil verifier err=%v", err)
	}
	manager.SetQuestionBankVerifier(verifier)

	// 乐观锁语义不变：revision 过期必须拒绝。
	ids2 := []string{"q1"}
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 1, CustomQuestionIds: &ids2}); err == nil || !strings.Contains(err.Error(), "已被其他操作修改") {
		t.Fatalf("stale revision err=%v", err)
	}

	// 读取容错：NULL/空串/非法 JSON 一律归一为空数组。
	for _, raw := range []string{"NULL", "''", "'not-json'"} {
		query := `UPDATE model_quality_policies SET custom_question_ids=` + raw + ` WHERE system_account_id='sys'`
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
		view, err := manager.Policy(ctx, "sys")
		if err != nil || view.CustomQuestionIds == nil || len(view.CustomQuestionIds) != 0 {
			t.Fatalf("raw=%s view=%+v err=%v", raw, view.CustomQuestionIds, err)
		}
	}

	// 形状校验在归一化后执行：重复 id 去重后 ≤3 可通过，视图/落库去重。
	dup := []string{"q1", " q1 ", "q2", "q3"}
	manager.SetQuestionBankVerifier(verifier)
	saved, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 3, CustomQuestionIds: &dup})
	if err != nil || len(saved.CustomQuestionIds) != 3 {
		t.Fatalf("dup ids policy=%+v err=%v", saved.CustomQuestionIds, err)
	}
}

// TestQBQualitySchedulesCustomQuestionIds 覆盖定时检查的同样语义。
func TestQBQualitySchedulesCustomQuestionIds(t *testing.T) {
	manager, db, verifier := qbQualityFixture(t)
	ctx := context.Background()

	created, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10, CustomQuestionIds: []string{"q1", "q2"}})
	if err != nil || len(created.CustomQuestionIds) != 2 {
		t.Fatalf("created schedule=%+v err=%v", created, err)
	}
	// 未通过审核的 id 在创建时拒绝（fail-closed）。
	bad := QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10, CustomQuestionIds: []string{"rejected-id"}}
	if _, err := manager.CreateSchedule(ctx, "sys", bad); err == nil || !strings.Contains(err.Error(), "题库题目不可用或未通过审核: rejected-id") {
		t.Fatalf("create unapproved err=%v", err)
	}

	// PATCH：显式数组替换，空数组清空，缺省不改。
	verifier.approved["q9"] = true
	patched, err := manager.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: 1, CustomQuestionIds: qbPtrIds([]string{"q9"})})
	if err != nil || len(patched.CustomQuestionIds) != 1 || patched.CustomQuestionIds[0] != "q9" {
		t.Fatalf("patched schedule=%+v err=%v", patched, err)
	}
	empty := []string{}
	cleared, err := manager.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: 2, CustomQuestionIds: &empty})
	if err != nil || len(cleared.CustomQuestionIds) != 0 {
		t.Fatalf("cleared schedule=%+v err=%v", cleared, err)
	}
	unchanged, err := manager.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: 3, Model: qbPtrString("gpt-5.6-terra")})
	if err != nil || len(unchanged.CustomQuestionIds) != 0 {
		t.Fatalf("unchanged schedule=%+v err=%v", unchanged, err)
	}

	// 列表读取回显（scanQualitySchedule 的列归一）。
	list, err := manager.PatchSchedule(ctx, "sys", created.ID, QualitySchedulePatch{ExpectedRevision: 4, CustomQuestionIds: qbPtrIds([]string{"q1"})})
	if err != nil || len(list.CustomQuestionIds) != 1 {
		t.Fatalf("re-patch schedule=%+v err=%v", list, err)
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT custom_question_ids FROM model_quality_schedules WHERE id=?`, created.ID).Scan(&stored); err != nil || !stored.Valid || stored.String != `["q1"]` {
		t.Fatalf("stored=%q valid=%v err=%v", stored.String, stored.Valid, err)
	}
}
