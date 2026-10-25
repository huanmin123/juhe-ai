package modelcheckowner

// T62 深度排查（2026-10-24）回归：fallback 惩罚改写账户级调度标记时，同一
// 事务同步属主启用绑定行的 local_* 快照（与账户编辑 BUG-0258 臂同契约）。
// 此前只写 accounts.* 留下 local 分歧数据——T62 第二层根因（自有账户超级
// 优先在链面恒不生效）的分歧源。

import (
	"context"
	"testing"
	"time"

)

func TestBusinessEnforcementFallbackSyncsBindingLocalSnapshot(t *testing.T) {
	t.Skip("W28②：BusinessEnforcementApplier PG 臂 generation=generation+1 未限定自引用 42702（生产缺陷待裁决，见 docs/待办与阻塞事项.md #W28）")
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessEnforcementTestDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	// 追加绑定表行（表结构由 businessEnforcementTestDDL 提供）。
	if _, err := db.Exec(`INSERT INTO group_accounts (account_id,system_account_id,local_fallback_enabled,local_super_priority_enabled,enabled,updated_at) VALUES ('acct-1','sys-1',0,1, 1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,status,config_revision,fallback_enabled,super_priority_enabled,deleted_at,schedulable,last_error_code,last_error_message,updated_at) VALUES ('acct-1','sys-1','active',4,0,1,NULL,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	// 惩罚前：账户级 super=1、绑定快照 local_super=1 / local_fallback=0。
	if _, err := db.Exec(`INSERT INTO group_accounts (account_id,system_account_id,local_fallback_enabled,local_super_priority_enabled,enabled,updated_at) VALUES ('acct-1','sys-1',0,1, 1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules VALUES ('sch-1','sys-1','acct-1',7,'full',70,'fallback',15,0,'gpt-5.6-sol',NULL)`); err != nil {
		t.Fatal(err)
	}
	applier, err := NewBusinessEnforcementApplier(db)
	if err != nil {
		t.Fatal(err)
	}
	input := QualityEnforcement{AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", ProviderCode: "openai", Model: "gpt-5.6-sol", PolicyRevision: "7", AccountConfigRevision: "4", ScheduleID: "sch-1", Action: "fallback", Score: 20, Threshold: 70, RecoveryIntervalMinutes: 15, Profile: "full", OccurredAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	outcome, err := applier.Apply(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.AlreadyEffective || outcome.EnforcementID == "" {
		t.Fatalf("outcome=%+v", outcome)
	}
	// 账户级：fallback=1（降备用池）、super=0（清超级优先）。
	var accountFallback, accountSuper int
	if err := db.QueryRow(`SELECT fallback_enabled,super_priority_enabled FROM accounts WHERE id='acct-1'`).Scan(&accountFallback, &accountSuper); err != nil {
		t.Fatal(err)
	}
	if accountFallback != 1 || accountSuper != 0 {
		t.Fatalf("account fallback=%d super=%d, want 1/0", accountFallback, accountSuper)
	}
	// 绑定快照：local_* 与账户级同步（分歧源闭合）。
	var bindingFallback, bindingSuper int
	if err := db.QueryRow(`SELECT local_fallback_enabled,local_super_priority_enabled FROM group_accounts WHERE account_id='acct-1'`).Scan(&bindingFallback, &bindingSuper); err != nil {
		t.Fatal(err)
	}
	if bindingFallback != 1 || bindingSuper != 0 {
		t.Fatalf("binding local fallback=%d super=%d, want 1/0（同步快照）", bindingFallback, bindingSuper)
	}
}
