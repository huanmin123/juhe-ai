package accounts

// w13a test_store.go 未覆盖臂补齐：测试会话/任务存储的直接调用臂 —— 缺少
// 调用者上下文、会话过期/取消后的可用性断言、单任务约束、已完成任务的取消
// 与失败、过期清理。

import (
	"context"
	"testing"
)

func TestW13ATestStoreSessionArms(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	ctx := context.Background()

	// 缺少调用者上下文 → ValidationError（378-380）。
	if _, err := env.store.CreateTestSession(ctx, AccessScope{}); err == nil {
		t.Fatal("缺上下文应拒绝")
	}

	// 正常创建 → 心跳 → 结束；结束后再次结束/取消 → nil（settled 臂）。
	session, err := env.store.CreateTestSession(ctx, AccessScope{ViewerID: "w13a-user"})
	if err != nil || session == nil {
		t.Fatalf("创建会话失败：%v", err)
	}
	if _, err := env.store.HeartbeatTestSession(ctx, session.ID, &AccessScope{ViewerID: "w13a-user"}); err != nil {
		t.Fatalf("心跳失败：%v", err)
	}
	completed, err := env.store.CompleteTestSession(ctx, session.ID, &AccessScope{ViewerID: "w13a-user"})
	if err != nil || completed == nil {
		t.Fatalf("结束会话失败：%v", err)
	}
	// 重复结束幂等：再次返回已完成会话。
	again, err := env.store.CompleteTestSession(ctx, session.ID, &AccessScope{ViewerID: "w13a-user"})
	if err != nil || again == nil || again.Status != "completed" {
		t.Fatalf("重复结束应幂等返回：%v %v", again, err)
	}
	// 已结束会话取消：返回空任务列表的取消结果（幂等）。
	if canceled, err := env.store.CancelTestSession(ctx, session.ID, &AccessScope{ViewerID: "w13a-user"}, "已停止测试"); err != nil || canceled == nil {
		t.Fatalf("已结束会话取消应幂等：%v %v", canceled, err)
	}

	// 过期 running 会话：任务创建时被标记过期并拒绝（assertUsableTestSession）。
	expired, err := env.store.CreateTestSession(ctx, AccessScope{ViewerID: "w13a-user"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE account_test_sessions SET status = 'expired' WHERE id = ?`, expired.ID)
	if _, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-x", Access: AccessScope{ViewerID: "w13a-user"}, SessionID: expired.ID,
	}); err == nil {
		t.Fatal("过期会话应拒绝任务创建")
	}

	// 单任务约束：可用会话绑定一个任务后，第二个任务被拒绝。
	live, err := env.store.CreateTestSession(ctx, AccessScope{ViewerID: "w13a-user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-x", Access: AccessScope{ViewerID: "w13a-user"}, SessionID: live.ID,
	}); err != nil {
		t.Fatalf("首个任务应成功：%v", err)
	}
	if _, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-x", Access: AccessScope{ViewerID: "w13a-user"}, SessionID: live.ID,
	}); err == nil {
		t.Fatal("第二任务应被单任务约束拒绝")
	}

	// 缺失会话行 → GetTestSession nil（469-477）。
	if got, err := env.store.GetTestSession(ctx, "acctsess-w13a-none", &AccessScope{ViewerID: "w13a-user"}); err != nil || got != nil {
		t.Fatalf("缺失会话应返回 nil：%v %v", got, err)
	}
}

func TestW13ATestStoreTaskArms(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	ctx := context.Background()

	// queued 任务：取消成功（882-890）。
	task, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w13a-tt", AccountName: "w13a-tt", Access: AccessScope{ViewerID: "w13a-user2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := env.store.CancelTestTask(ctx, task.ID, &AccessScope{ViewerID: "w13a-user2"})
	if err != nil || canceled == nil {
		t.Fatalf("取消 queued 任务失败：%v", err)
	}
	// 已取消任务再次取消：幂等返回已取消状态。
	again, err := env.store.CancelTestTask(ctx, task.ID, &AccessScope{ViewerID: "w13a-user2"})
	if err != nil || again == nil || again.Status != "canceled" {
		t.Fatalf("重复取消应幂等返回：%v %v", again, err)
	}

	// 失败任务：FailTestTask 正常路径与缺失路径。
	task2, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w13a-tt2", Access: AccessScope{ViewerID: "w13a-user2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.FailTestTask(ctx, task2.ID, "w13a 失败消息"); err != nil {
		t.Fatalf("失败标记应成功：%v", err)
	}
	// 缺失任务失败标记：静默成功（UPDATE 0 行不报错）。
	if err := env.store.FailTestTask(ctx, "accttest-w13a-none", "x"); err != nil {
		t.Fatalf("缺失任务失败标记不应报错：%v", err)
	}

	// 列表：按 ids 读取（空 ids 返回空集）。
	if empty, err := env.store.ListTestTasks(ctx, nil, &AccessScope{ViewerID: "w13a-user2"}); err != nil || len(empty) != 0 {
		t.Fatalf("空 ids 应返回空集：%v %v", empty, err)
	}
	tasks, err := env.store.ListTestTasks(ctx, []string{task.ID, task2.ID}, &AccessScope{ViewerID: "w13a-user2"})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("任务列表应返回 2 条：%d %v", len(tasks), err)
	}

	// 过期清理：直接调用并断言不报错。
	if err := env.store.cleanupExpiredTestTasks(ctx); err != nil {
		t.Fatalf("过期清理失败：%v", err)
	}

	// GetTestTaskDetail 经会话详情读任务（483-498）。
	detailSession, tasksForSession, err := env.store.GetTestSessionDetail(ctx, "acctsess-w13a-none", &AccessScope{ViewerID: "w13a-user2"})
	if err != nil || detailSession != nil || tasksForSession != nil {
		t.Fatalf("缺失会话详情应返回 nil：%v %v %v", detailSession, tasksForSession, err)
	}
}
