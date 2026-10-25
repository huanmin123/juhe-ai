package main

// 本文件覆盖 --migrate-chat-account-only-binding 的进程内结果函数与 main()
// 派发分支（AI 问答会话账户唯一绑定设计 §4/§10）：用法错误出口与互斥分支。
// SQLite 文件端到端臂随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z
// B4）；该命令是一次性历史迁移（生产已收敛），PG 臂行为由 internal/
// chatbindingmigration 的实现契约与回滚 SQL 输出承载。os.Exit 出口已由
// runMaintenance 收敛为返回码（测试单进程纪律，docs/develop/后端测试分层规则.md）。

import "testing"

func TestChatAccountOnlyBindingMigrationResultUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want int
	}{
		{name: "缺 dsn", dsn: "", want: 2},
		{name: "非 PG URL", dsn: "sqlite://business.sqlite3", want: 2},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if code := chatAccountOnlyBindingMigrationResult(item.dsn); code != item.want {
				t.Fatalf("exit code = %d, want %d", code, item.want)
			}
		})
	}
}

func TestChatAccountOnlyBindingMigrationMainDispatchMutex(t *testing.T) {
	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-ensure-schema"}); code != 2 {
		t.Fatalf("mutex with ensure-schema exit code = %d, want 2", code)
	}
	if code := runMaintenance([]string{"-migrate-chat-account-only-binding", "-version"}); code != 2 {
		t.Fatalf("mutex with version exit code = %d, want 2", code)
	}
}
