package groups

import (
	"context"
	"testing"
)

// T34：分组模型参考候选来自分组内账户的真实 account_supported_models →
// models 行；标准组与超级分组（成员并集）都覆盖，vendor 为空不伪造 custom。
// 夹具走 pgkit_test.go 的 PG testEnv（组A 迁移）；models /
// account_supported_models 为读面放宽列集（生产全列见
// maintenance pg_schema_business_tables.go，读口只消费 id/vendor_code/
// public_model/status 与关联键）。
func TestListGroupModelOptions(t *testing.T) {
	env := newTestEnv(t)
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.models (id TEXT PRIMARY KEY, vendor_code TEXT, public_model TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (account_id TEXT NOT NULL, model_id TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (account_id, model_id))`,
	} {
		if _, err := env.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, group_kind, created_at, updated_at)
		VALUES ('g-std', 'owner', '标准组', 1, 0, 'personal', 'standard', '2026-01-01', '2026-01-01'),
		       ('g-super', 'owner', '超级组', 1, 0, 'personal', 'super', '2026-01-01', '2026-01-01'),
		       ('g-member', 'owner', '成员组', 1, 0, 'personal', 'standard', '2026-01-01', '2026-01-01')`)
	env.exec(t, `INSERT INTO super_group_members (id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
		VALUES ('sgm-1', 'g-super', 'g-member', 0, 1, '2026-01-01', '2026-01-01')`)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, deleted_at, created_at) VALUES
		('a-std', 'owner', NULL, '2026-01-01'), ('a-member', 'owner', NULL, '2026-01-01')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at) VALUES
		('owner', 'g-std', 'a-std', 1, '2026-01-01', '2026-01-01'),
		('owner', 'g-member', 'a-member', 1, '2026-01-01', '2026-01-01')`)
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, status, created_at, updated_at) VALUES
		('m-std', NULL, 'generic-model', 'active', '2026-01-01', '2026-01-01'),
		('m-member', 'gpt', 'gpt-model', 'active', '2026-01-01', '2026-01-01')`)
	env.exec(t, `INSERT INTO account_supported_models (account_id, model_id, created_at) VALUES
		('a-std', 'm-std', '2026-01-01'), ('a-member', 'm-member', '2026-01-01')`)

	store, err := NewStore(env.db, true, nil, nil, env.inval)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	access := AccessScope{ViewerID: "owner"}
	std, err := store.ListGroupModelOptions(ctx, "g-std", "", 50, access)
	if err != nil || len(std) != 1 || std[0].Name != "generic-model" {
		t.Fatalf("standard options = %+v err=%v", std, err)
	}
	super, err := store.ListGroupModelOptions(ctx, "g-super", "", 50, access)
	if err != nil || len(super) != 1 || super[0].Name != "gpt-model" {
		t.Fatalf("super options = %+v err=%v", super, err)
	}
	filtered, err := store.ListGroupModelOptions(ctx, "g-std", "generic", 50, access)
	if err != nil || len(filtered) != 1 {
		t.Fatalf("keyword options = %+v err=%v", filtered, err)
	}
}
