package modelcheckquestionbank

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestStore 打开本测试专属的临时 SQLite 业务库并建最小题库表
// （照 modelcheckowner business_quality_management_test.go 的
// t.TempDir()+mode=rwc 基建；表结构与 maintenance SQLite DDL 一致）。
func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/question-bank.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(testQuestionBankDDL); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

const testQuestionBankDDL = `CREATE TABLE model_check_question_bank (
      id TEXT PRIMARY KEY,
      title TEXT NOT NULL,
      title_norm TEXT NOT NULL,
      question_text TEXT NOT NULL,
      reference_answer TEXT NOT NULL,
      key_points_json TEXT,
      status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
      reject_reason TEXT,
      created_by TEXT NOT NULL,
      created_scope TEXT NOT NULL,
      reviewed_by TEXT,
      reviewed_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    )`

var (
	actorAlice = Actor{SystemAccountID: "sys-alice"}
	actorBob   = Actor{SystemAccountID: "sys-bob"}
)

func mustCreate(t *testing.T, store *Store, actor Actor, title, text string) Question {
	t.Helper()
	question, err := store.Create(context.Background(), actor, CreateQuestionInput{
		Title: title, QuestionText: text, ReferenceAnswer: "参考答案：" + title,
		KeyPoints: []string{"要点一"},
	})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return question
}

func statusErrorCode(t *testing.T, err error) *StatusError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	return status
}

func TestStoreCreateAndGet(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	question := mustCreate(t, store, actorAlice, "什么是模型降智？", "请解释模型降智的常见表现。")
	if question.ID == "" {
		t.Fatal("服务端必须生成 id")
	}
	if question.Status != StatusPending {
		t.Fatalf("新建题目 status = %q, want pending", question.Status)
	}
	if question.CreatedBy != actorAlice.SystemAccountID || question.CreatedScope != actorAlice.SystemAccountID {
		t.Fatalf("created_by/scope = %q/%q", question.CreatedBy, question.CreatedScope)
	}
	if len(question.KeyPoints) != 1 || question.KeyPoints[0] != "要点一" {
		t.Fatalf("key points = %v", question.KeyPoints)
	}
	loaded, err := store.GetByID(ctx, question.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if loaded.Title != question.Title || loaded.QuestionText != question.QuestionText || loaded.ReferenceAnswer != question.ReferenceAnswer {
		t.Fatalf("loaded question mismatch: %+v", loaded)
	}
	if loaded.TitleNorm != NormalizeText(loaded.Title) {
		t.Fatalf("title_norm = %q, want %q", loaded.TitleNorm, NormalizeText(loaded.Title))
	}
	if _, err := store.GetByID(ctx, "mcq-missing"); err == nil {
		t.Fatal("missing question must 404")
	} else if code := statusErrorCode(t, err); code.Status != statusNotFound {
		t.Fatalf("missing question status = %d", code.Status)
	}
}

func TestStoreCreateValidation(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		input  CreateQuestionInput
		status int
	}{
		{"空标题", CreateQuestionInput{Title: "  ", QuestionText: "题面", ReferenceAnswer: "答案"}, statusBadRequest},
		{"超长标题", CreateQuestionInput{Title: string(make([]rune, TitleMaxRunes+1)), QuestionText: "题面", ReferenceAnswer: "答案"}, statusBadRequest},
		{"空题面", CreateQuestionInput{Title: "标题", QuestionText: "", ReferenceAnswer: "答案"}, statusBadRequest},
		{"超长题面", CreateQuestionInput{Title: "标题", QuestionText: string(make([]rune, QuestionTextMaxRunes+1)), ReferenceAnswer: "答案"}, statusBadRequest},
		{"空答案", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: " "}, statusBadRequest},
		{"超长答案", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: string(make([]rune, ReferenceAnswerMaxRunes+1))}, statusBadRequest},
		{"要点超量", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: "答案", KeyPoints: make([]string, KeyPointsMaxCount+1)}, statusBadRequest},
		{"空要点", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: "答案", KeyPoints: []string{" "}}, statusBadRequest},
		{"超长要点", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: "答案", KeyPoints: []string{string(make([]rune, KeyPointMaxRunes+1))}}, statusBadRequest},
		{"缺操作者", CreateQuestionInput{Title: "标题", QuestionText: "题面", ReferenceAnswer: "答案"}, statusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actor := actorAlice
			if tc.name == "缺操作者" {
				actor = Actor{}
			}
			_, err := store.Create(ctx, actor, tc.input)
			if code := statusErrorCode(t, err); code.Status != tc.status {
				t.Fatalf("status = %d, want %d (err=%v)", code.Status, tc.status, err)
			}
		})
	}
}

func TestStoreTitleAndSimilarityDeduplication(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	original := mustCreate(t, store, actorAlice, "什么是模型降智？", "请解释模型降智的常见表现与判断方法。")

	t.Run("标题归一化后重复拒绝", func(t *testing.T) {
		_, err := store.Create(ctx, actorBob, CreateQuestionInput{Title: "什么是模型降智", QuestionText: "完全不同的题目内容。", ReferenceAnswer: "答案"})
		code := statusErrorCode(t, err)
		if code.Status != statusConflict || code.Message != "标题与现有题目重复" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})

	t.Run("内容相似拒绝并提示最相似标题", func(t *testing.T) {
		_, err := store.Create(ctx, actorBob, CreateQuestionInput{
			Title:           "换一个标题",
			QuestionText:    "请解释模型降智的常见表现与判断方法说明",
			ReferenceAnswer: "答案",
		})
		code := statusErrorCode(t, err)
		if code.Status != statusConflict {
			t.Fatalf("status = %d, want 409", code.Status)
		}
		want := "题目内容与现有题目《什么是模型降智？》过于相似"
		if code.Message != want {
			t.Fatalf("message = %q, want %q", code.Message, want)
		}
	})

	t.Run("rejected 不占用标题", func(t *testing.T) {
		if _, err := store.Review(ctx, original.ID, Actor{SystemAccountID: "sys-admin"}, true, "reject", "题目不严谨"); err != nil {
			t.Fatalf("reject: %v", err)
		}
		recreated, err := store.Create(ctx, actorBob, CreateQuestionInput{Title: "什么是模型降智？", QuestionText: "全新的题目内容，与原题差异足够大，不构成相似证据。", ReferenceAnswer: "答案"})
		if err != nil {
			t.Fatalf("rejected title must be reusable: %v", err)
		}
		if recreated.Status != StatusPending {
			t.Fatalf("status = %q", recreated.Status)
		}
	})
}

func TestStoreUpdateFlow(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	question := mustCreate(t, store, actorAlice, "原始标题", "原始题目内容，足够独特。")
	admin := Actor{SystemAccountID: "sys-admin"}

	t.Run("非创建者且非管理员不可修改", func(t *testing.T) {
		_, err := store.Update(ctx, question.ID, actorBob, false, UpdateQuestionInput{Title: "新标题", QuestionText: "新内容", ReferenceAnswer: "新答案"})
		if code := statusErrorCode(t, err); code.Status != statusForbidden {
			t.Fatalf("status = %d", code.Status)
		}
	})

	t.Run("approved 锁定不可编辑", func(t *testing.T) {
		if _, err := store.Review(ctx, question.ID, admin, true, "approve", ""); err != nil {
			t.Fatalf("approve: %v", err)
		}
		_, err := store.Update(ctx, question.ID, actorAlice, false, UpdateQuestionInput{Title: "新标题", QuestionText: "新内容", ReferenceAnswer: "新答案"})
		if code := statusErrorCode(t, err); code.Status != statusConflict || code.Message != "已通过审核的题目不可编辑" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})

	t.Run("rejected 修改后回到 pending 并清空审核字段", func(t *testing.T) {
		// CAS 只允许 pending→rejected：驳回流走独立题目。
		revisable := mustCreate(t, store, actorAlice, "待修订题", "待修订的题目内容。")
		if _, err := store.Review(ctx, revisable.ID, admin, true, "reject", "口径过期，请更新"); err != nil {
			t.Fatalf("reject: %v", err)
		}
		updated, err := store.Update(ctx, revisable.ID, actorAlice, false, UpdateQuestionInput{
			Title: "更新后的标题", QuestionText: "更新后的题目内容，同样足够独特。", ReferenceAnswer: "更新后的答案", KeyPoints: []string{"新要点"},
		})
		if err != nil {
			t.Fatalf("update rejected question: %v", err)
		}
		if updated.Status != StatusPending {
			t.Fatalf("status = %q, want pending", updated.Status)
		}
		if updated.RejectReason != "" || updated.ReviewedBy != "" || updated.ReviewedAt != "" {
			t.Fatalf("审核字段未清空: %+v", updated)
		}
		if updated.Title != "更新后的标题" || updated.ReferenceAnswer != "更新后的答案" || len(updated.KeyPoints) != 1 {
			t.Fatalf("updated fields mismatch: %+v", updated)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, updated.CreatedAt)
		if err != nil {
			t.Fatalf("parse created_at %q: %v", updated.CreatedAt, err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updated.UpdatedAt)
		if err != nil {
			t.Fatalf("parse updated_at %q: %v", updated.UpdatedAt, err)
		}
		if updatedAt.Before(createdAt) {
			t.Fatalf("updated_at %q 早于 created_at %q", updated.UpdatedAt, updated.CreatedAt)
		}
	})

	t.Run("修改后重新查重且排除自身", func(t *testing.T) {
		other := mustCreate(t, store, actorAlice, "另一道题", "另一道题的内容，与前者明显不同。")
		// 改成与"更新后的标题"（pending）相同标题 → 409；保持自身标题不改 → 允许。
		_, err := store.Update(ctx, other.ID, actorAlice, false, UpdateQuestionInput{Title: "更新后的标题", QuestionText: "另一个内容。", ReferenceAnswer: "答案"})
		if code := statusErrorCode(t, err); code.Status != statusConflict || code.Message != "标题与现有题目重复" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
		kept, err := store.Update(ctx, other.ID, actorAlice, false, UpdateQuestionInput{Title: "另一道题", QuestionText: "另一道题的内容，与前者明显不同。", ReferenceAnswer: "保持原样"})
		if err != nil {
			t.Fatalf("update excluding self must pass: %v", err)
		}
		if kept.ID != other.ID || kept.Title != "另一道题" {
			t.Fatalf("kept = %+v", kept)
		}
	})

	t.Run("修改不存在的题目", func(t *testing.T) {
		_, err := store.Update(ctx, "mcq-missing", actorAlice, false, UpdateQuestionInput{Title: "标题", QuestionText: "内容", ReferenceAnswer: "答案"})
		if code := statusErrorCode(t, err); code.Status != statusNotFound {
			t.Fatalf("status = %d", code.Status)
		}
	})
}

func TestStoreDeletePermissions(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	own := mustCreate(t, store, actorAlice, "待删题", "创建者自己的待删题。")
	foreign := mustCreate(t, store, actorBob, "他人的题", "他人提交的题目。")
	admin := Actor{SystemAccountID: "sys-admin"}

	t.Run("创建者删除自己的 pending", func(t *testing.T) {
		if err := store.Delete(ctx, own.ID, actorAlice, false); err != nil {
			t.Fatalf("delete own pending: %v", err)
		}
		if _, err := store.GetByID(ctx, own.ID); err == nil {
			t.Fatal("deleted question must be gone")
		}
	})

	t.Run("创建者不能删他人题目", func(t *testing.T) {
		err := store.Delete(ctx, foreign.ID, actorAlice, false)
		if code := statusErrorCode(t, err); code.Status != statusForbidden {
			t.Fatalf("status = %d", code.Status)
		}
	})

	t.Run("创建者不能删 approved，管理员可以", func(t *testing.T) {
		if _, err := store.Review(ctx, foreign.ID, admin, true, "approve", ""); err != nil {
			t.Fatalf("approve: %v", err)
		}
		err := store.Delete(ctx, foreign.ID, actorBob, false)
		if code := statusErrorCode(t, err); code.Status != statusForbidden || code.Message != "已通过审核的题目仅管理员可删除" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
		if err := store.Delete(ctx, foreign.ID, admin, true); err != nil {
			t.Fatalf("admin delete approved: %v", err)
		}
	})

	t.Run("删除不存在的题目", func(t *testing.T) {
		err := store.Delete(ctx, "mcq-missing", admin, true)
		if code := statusErrorCode(t, err); code.Status != statusNotFound {
			t.Fatalf("status = %d", code.Status)
		}
	})
}

func TestStoreReviewCAS(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	question := mustCreate(t, store, actorAlice, "审核流题目", "用于审核流测试的题目。")
	admin := Actor{SystemAccountID: "sys-admin"}

	t.Run("自助实例不可审核由 handler 保证，store 校验动作", func(t *testing.T) {
		_, err := store.Review(ctx, question.ID, admin, true, "publish", "")
		if code := statusErrorCode(t, err); code.Status != statusBadRequest || code.Message != "审核动作无效" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})

	t.Run("驳回必填理由", func(t *testing.T) {
		_, err := store.Review(ctx, question.ID, admin, true, "reject", "   ")
		if code := statusErrorCode(t, err); code.Status != statusBadRequest || code.Message != "驳回理由不能为空" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})

	t.Run("approve 成功后再次 approve 被 CAS 拒绝", func(t *testing.T) {
		approved, err := store.Review(ctx, question.ID, admin, true, "approve", "")
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		if approved.Status != StatusApproved || approved.ReviewedBy != admin.SystemAccountID || approved.ReviewedAt == "" {
			t.Fatalf("approved = %+v", approved)
		}
		if approved.RejectReason != "" {
			t.Fatalf("approve 必须清空驳回理由, got %q", approved.RejectReason)
		}
		// approve 只允许 pending：approved/rejected → approve 一律冲突。
		_, err = store.Review(ctx, question.ID, admin, true, "approve", "")
		code := statusErrorCode(t, err)
		if code.Status != statusConflict || code.Message != "题目已被审核或不存在" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})

	t.Run("pending 双审只成功一次", func(t *testing.T) {
		second := mustCreate(t, store, actorAlice, "双审题目", "用于并发审核语义测试的题目。")
		reviewerA := Actor{SystemAccountID: "sys-admin-a"}
		reviewerB := Actor{SystemAccountID: "sys-admin-b"}
		if _, err := store.Review(ctx, second.ID, reviewerA, true, "approve", ""); err != nil {
			t.Fatalf("first review: %v", err)
		}
		_, err := store.Review(ctx, second.ID, reviewerB, true, "approve", "")
		if code := statusErrorCode(t, err); code.Status != statusConflict {
			t.Fatalf("status = %d", code.Status)
		}
	})

	t.Run("reject 记录理由与审核人", func(t *testing.T) {
		third := mustCreate(t, store, actorAlice, "驳回题目", "用于驳回路径测试的题目。")
		rejected, err := store.Review(ctx, third.ID, admin, true, "reject", "注入特征可疑")
		if err != nil {
			t.Fatalf("reject: %v", err)
		}
		if rejected.Status != StatusRejected || rejected.RejectReason != "注入特征可疑" || rejected.ReviewedBy != admin.SystemAccountID || rejected.ReviewedAt == "" {
			t.Fatalf("rejected = %+v", rejected)
		}
	})

	t.Run("审核不存在的题目按 CAS 冲突返回", func(t *testing.T) {
		_, err := store.Review(ctx, "mcq-missing", admin, true, "approve", "")
		code := statusErrorCode(t, err)
		if code.Status != statusConflict || code.Message != "题目已被审核或不存在" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
	})
}

func TestStoreReviewDelistApproved(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	question := mustCreate(t, store, actorAlice, "下架题目", "用于驳回下架测试的题目。")
	admin := Actor{SystemAccountID: "sys-admin"}

	t.Run("approved 仅管理员可 reject 下架", func(t *testing.T) {
		if _, err := store.Review(ctx, question.ID, admin, true, "approve", ""); err != nil {
			t.Fatalf("approve: %v", err)
		}
		// 非 admin 的 reject CAS 只匹配 pending，approved 行不受影响。
		_, err := store.Review(ctx, question.ID, actorBob, false, "reject", "越权下架")
		code := statusErrorCode(t, err)
		if code.Status != statusConflict || code.Message != "题目已被审核或不存在" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
		still, err := store.GetByID(ctx, question.ID)
		if err != nil || still.Status != StatusApproved {
			t.Fatalf("approved 题目状态被越权修改: %+v, %v", still, err)
		}
	})

	t.Run("admin reject 下架成功并记录理由与审核人", func(t *testing.T) {
		delist, err := store.Review(ctx, question.ID, admin, true, "reject", "口径过期，下架")
		if err != nil {
			t.Fatalf("delist: %v", err)
		}
		if delist.Status != StatusRejected || delist.RejectReason != "口径过期，下架" || delist.ReviewedBy != admin.SystemAccountID || delist.ReviewedAt == "" {
			t.Fatalf("delisted = %+v", delist)
		}
	})

	t.Run("下架后重复 reject 与 re-approve 均 CAS 冲突", func(t *testing.T) {
		_, err := store.Review(ctx, question.ID, admin, true, "reject", "再次下架")
		if code := statusErrorCode(t, err); code.Status != statusConflict {
			t.Fatalf("second reject status = %d", code.Status)
		}
		_, err = store.Review(ctx, question.ID, admin, true, "approve", "")
		if code := statusErrorCode(t, err); code.Status != statusConflict {
			t.Fatalf("re-approve status = %d", code.Status)
		}
	})

	t.Run("两名管理员并发下架只成功一次", func(t *testing.T) {
		race := mustCreate(t, store, actorAlice, "并发下架题", "用于并发下架语义测试的题目。")
		if _, err := store.Review(ctx, race.ID, admin, true, "approve", ""); err != nil {
			t.Fatalf("approve: %v", err)
		}
		adminA := Actor{SystemAccountID: "sys-admin-a"}
		adminB := Actor{SystemAccountID: "sys-admin-b"}
		if _, err := store.Review(ctx, race.ID, adminA, true, "reject", "A 先下架"); err != nil {
			t.Fatalf("first delist: %v", err)
		}
		_, err := store.Review(ctx, race.ID, adminB, true, "reject", "B 迟到的下架")
		if code := statusErrorCode(t, err); code.Status != statusConflict || code.Message != "题目已被审核或不存在" {
			t.Fatalf("status=%d message=%q", code.Status, code.Message)
		}
		// admin=true 的 reject 对 pending 题目仍走原路径。
		pending := mustCreate(t, store, actorAlice, "admin 驳回 pending", "admin 直接驳回待审题目。")
		rejected, err := store.Review(ctx, pending.ID, admin, true, "reject", "直接驳回")
		if err != nil || rejected.Status != StatusRejected {
			t.Fatalf("admin reject pending = %+v, %v", rejected, err)
		}
	})
}

func TestStoreListVisibilityAndFilters(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()
	alicePending := mustCreate(t, store, actorAlice, "甲的待审", "甲提交的待审题目。")
	aliceApproved := mustCreate(t, store, actorAlice, "甲的已过", "甲提交的已过题目。")
	bobApproved := mustCreate(t, store, actorBob, "乙的已过", "乙提交的已过题目。")
	bobRejected := mustCreate(t, store, actorBob, "乙的驳回", "乙提交的驳回题目。")
	admin := Actor{SystemAccountID: "sys-admin"}
	if _, err := store.Review(ctx, aliceApproved.ID, admin, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Review(ctx, bobApproved.ID, admin, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Review(ctx, bobRejected.ID, admin, true, "reject", "不通过"); err != nil {
		t.Fatal(err)
	}
	// 固化排序：created_at DESC。
	for _, tc := range []struct{ id, createdAt string }{
		{bobApproved.ID, "2026-01-03T00:00:00Z"},
		{alicePending.ID, "2026-01-02T00:00:00Z"},
		{aliceApproved.ID, "2026-01-01T00:00:00Z"},
		{bobRejected.ID, "2025-12-31T00:00:00Z"},
	} {
		if _, err := db.Exec(`UPDATE model_check_question_bank SET created_at = ? WHERE id = ?`, tc.createdAt, tc.id); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("管理员全量", func(t *testing.T) {
		items, total, err := store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 4 || len(items) != 4 {
			t.Fatalf("admin list = %d/%d, want 4/4", len(items), total)
		}
		if items[0].ID != bobApproved.ID || items[3].ID != bobRejected.ID {
			t.Fatalf("created_at DESC 排序错误: %v", items)
		}
	})

	t.Run("自助只见 approved 与本人提交", func(t *testing.T) {
		items, total, err := store.List(ctx, QuestionFilter{Actor: actorAlice, Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 || len(items) != 3 {
			t.Fatalf("alice list = %d/%d, want 3/3", len(items), total)
		}
		for _, item := range items {
			if item.ID == bobRejected.ID {
				t.Fatal("他人的 rejected 题目不应出现在自助列表")
			}
		}
	})

	t.Run("状态筛选", func(t *testing.T) {
		// 自助面 status=approved =（可见性 approved+本人）AND approved：
		// 全部 approved 题目（含乙的）都可见。
		items, total, err := store.List(ctx, QuestionFilter{Actor: actorAlice, Status: StatusApproved, Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(items) != 2 {
			t.Fatalf("alice approved list = %d/%d, want 2/2", len(items), total)
		}
		for _, item := range items {
			if item.Status != StatusApproved {
				t.Fatalf("approved 筛选混入 %q", item.Status)
			}
		}
		// 自助面 status=pending 只剩本人待审。
		items, total, err = store.List(ctx, QuestionFilter{Actor: actorAlice, Status: StatusPending, Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != alicePending.ID {
			t.Fatalf("alice pending list = %+v/%d", items, total)
		}
		_, _, err = store.List(ctx, QuestionFilter{Admin: true, Status: "draft", Page: 1, PageSize: 20})
		if code := statusErrorCode(t, err); code.Status != statusBadRequest {
			t.Fatalf("invalid status filter status = %d", code.Status)
		}
	})

	t.Run("关键字筛选", func(t *testing.T) {
		items, total, err := store.List(ctx, QuestionFilter{Admin: true, Keyword: "已过", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(items) != 2 {
			t.Fatalf("keyword list = %d/%d, want 2/2", len(items), total)
		}
		// LIKE 通配符必须按字面匹配。
		_, total, err = store.List(ctx, QuestionFilter{Admin: true, Keyword: "%", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 {
			t.Fatalf("literal %% must not match everything, got %d", total)
		}
	})

	t.Run("分页", func(t *testing.T) {
		items, total, err := store.List(ctx, QuestionFilter{Admin: true, Page: 2, PageSize: 3})
		if err != nil {
			t.Fatal(err)
		}
		if total != 4 || len(items) != 1 {
			t.Fatalf("page 2 size 3 = %d items, total %d, want 1/4", len(items), total)
		}
		if items[0].ID != bobRejected.ID {
			t.Fatalf("page 2 first = %q, want %q", items[0].ID, bobRejected.ID)
		}
	})
}

func TestStoreListByIDsAndOptions(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	pending := mustCreate(t, store, actorAlice, "待审题", "内容甲。")
	approvedA := mustCreate(t, store, actorAlice, "可选题目甲", "内容乙。")
	approvedB := mustCreate(t, store, actorBob, "可选题目乙", "内容丙。")
	rejected := mustCreate(t, store, actorBob, "驳回题目", "内容丁。")
	admin := Actor{SystemAccountID: "sys-admin"}
	for id, action := range map[string]string{approvedA.ID: "approve", approvedB.ID: "approve", rejected.ID: "reject"} {
		reason := ""
		if action == "reject" {
			reason = "不通过"
		}
		if _, err := store.Review(ctx, id, admin, true, action, reason); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("ListByIDs 过滤失效 id", func(t *testing.T) {
		items, err := store.ListByIDs(ctx, []string{pending.ID, approvedA.ID, approvedB.ID, rejected.ID, "mcq-missing", ""}, StatusApproved)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, item := range items {
			ids[item.ID] = true
		}
		if len(items) != 2 || !ids[approvedA.ID] || !ids[approvedB.ID] {
			t.Fatalf("ListByIDs approved = %v", ids)
		}
	})

	t.Run("ListByIDs 空入参", func(t *testing.T) {
		items, err := store.ListByIDs(ctx, nil, StatusApproved)
		if err != nil || len(items) != 0 {
			t.Fatalf("empty ids = %v, %v", items, err)
		}
	})

	t.Run("ListOptions 仅 approved 且支持关键字与 limit", func(t *testing.T) {
		options, err := store.ListOptions(ctx, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(options) != 2 {
			t.Fatalf("options = %v", options)
		}
		options, err = store.ListOptions(ctx, "可选题目", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(options) != 1 {
			t.Fatalf("limited options = %v", options)
		}
		if options[0].ID != approvedB.ID {
			t.Fatalf("newest-first option = %q, want %q", options[0].ID, approvedB.ID)
		}
		if options[0].Title != "可选题目乙" {
			t.Fatalf("option title = %q", options[0].Title)
		}
	})
}

func TestStoreBindAndTableDialect(t *testing.T) {
	store := &Store{postgres: true}
	if got := store.table(questionBankTable); got != "juhe_business."+questionBankTable {
		t.Fatalf("postgres table = %q", got)
	}
	if got := store.bind("SELECT a FROM t WHERE x=? AND y=?"); got != "SELECT a FROM t WHERE x=$1 AND y=$2" {
		t.Fatalf("postgres bind = %q", got)
	}
	sqliteStore := &Store{}
	if got := sqliteStore.table(questionBankTable); got != questionBankTable {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := sqliteStore.bind("SELECT a FROM t WHERE x=? AND y=?"); got != "SELECT a FROM t WHERE x=? AND y=?" {
		t.Fatalf("sqlite bind = %q", got)
	}
	if _, err := NewStore(nil, false); err == nil {
		t.Fatal("nil db must be rejected")
	}
}
