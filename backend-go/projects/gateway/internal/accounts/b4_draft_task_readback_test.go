package accounts

// B4① 存储迁移收尾回归：手动测试任务行存续独立于 accounts 行（与 media_jobs
// 「引用列不加外键：账户删除后任务行仍需存在」同构契约）。test-draft 草稿链落
// acctdraft_* 草稿 ID、物理无 accounts 行——任务读面必须 LEFT JOIN accounts 水
// 合 vendor（缺失行 providerCode 投影空串），任务可创建、可读回。钉住该契约，
// 防止读面被改回 INNER JOIN 使草稿测试全链 400（2026-10-23 收尾批实际回归）。

import (
	"context"
	"testing"
)

func TestB4DraftTaskReadbackWithoutAccountRow(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	ctx := context.Background()

	task, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID:   "acctdraft_b4readback",
		AccountName: "未保存草稿",
		Access:      AccessScope{ViewerID: "b4-draft-user"},
		Diagnostics: "full",
	})
	if err != nil {
		t.Fatalf("无账户行的草稿任务必须可创建（读面 JOIN 强度回归哨兵）：%v", err)
	}

	loaded, err := env.store.GetTestTask(ctx, task.ID, nil)
	if err != nil || loaded == nil {
		t.Fatalf("无账户行任务必须可读回：task=%+v err=%v", loaded, err)
	}
	if loaded.ProviderCode != "" {
		t.Fatalf("providerCode = %q, want 空串（无 accounts 水合来源）", loaded.ProviderCode)
	}
	if loaded.AccountID != "acctdraft_b4readback" || loaded.Status != "queued" {
		t.Fatalf("loaded = %+v", loaded)
	}

	listed, err := env.store.ListTestTasks(ctx, []string{task.ID}, nil)
	if err != nil || len(listed) != 1 || listed[0].ID != task.ID {
		t.Fatalf("ListTestTasks = %+v err=%v", listed, err)
	}
	if listed[0].ProviderCode != "" {
		t.Fatalf("列表 providerCode = %q, want 空串", listed[0].ProviderCode)
	}
}
