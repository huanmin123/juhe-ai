package modelcheckowner

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestListAccountOptionsFiltersAndFreezesCatalog(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,type TEXT,status TEXT,schedulable INTEGER,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER)`,
		`CREATE TABLE group_accounts (account_id TEXT,group_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
		`ALTER TABLE accounts ADD COLUMN health_check_endpoint_mode TEXT`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1),('profile_unknown',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g1','sys-1',1),('g2','sys-2',1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		`INSERT INTO accounts VALUES ('a1','sys-1','Alpha','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		`INSERT INTO accounts VALUES ('a2','sys-1','Beta','openai','profile_openai_openai_v1','openai','1','api_key','disabled',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		`INSERT INTO accounts VALUES ('a3','sys-2','Gamma','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		`INSERT INTO accounts VALUES ('a4','sys-1','Custom','openai','profile_openai_openai_v1','openai','1','custom','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		`INSERT INTO accounts VALUES ('a5','sys-1','Unsupported profile','openai','profile_unknown','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
	} {
		if _, err := db.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('a1','g1',1),('a2','g1',1),('a3','g2',1),('a4','g1',1),('a5','g1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE group_accounts ADD COLUMN system_account_id TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE group_accounts ADD COLUMN account_authorization_id TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE group_accounts SET system_account_id=CASE WHEN group_id='g1' THEN 'sys-1' ELSE 'sys-2' END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations VALUES ('authz-1','account','source-1','sys-2','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('instance-1','sys-1','Shared','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,'authz-1','source-1',NULL,NULL),('source-1','sys-2','Source','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN availability_schedule_json TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,group_id,enabled,system_account_id,account_authorization_id) VALUES ('instance-1','g1',1,'sys-1','authz-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g3','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,group_id,enabled,system_account_id,account_authorization_id) VALUES ('instance-1','g3',1,'sys-1','authz-1')`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	items, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "a1" || items[1].ID != "instance-1" || len(items[0].ModelCheckModels) != 0 || len(items[1].ModelCheckModels) != 0 {
		t.Fatalf("run options=%+v", items)
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='a1'`, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"11:00","end":"13:00"}]}`); err != nil {
		t.Fatal(err)
	}
	allowedSchedule, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1})
	if err != nil || len(allowedSchedule) != 1 {
		t.Fatalf("schedule-allowed account must remain runnable: %+v err=%v", allowedSchedule, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='a1'`, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"13:00","end":"14:00"}]}`); err != nil {
		t.Fatal(err)
	}
	deniedSchedule, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1})
	if err != nil || len(deniedSchedule) != 0 {
		t.Fatalf("schedule-denied account must be hidden from run options: %+v err=%v", deniedSchedule, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='a1'`, `{`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1}); err == nil {
		t.Fatal("invalid availability schedule must fail closed")
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=NULL WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	direct, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1})
	if err != nil || len(direct) != 1 || len(direct[0].ModelCheckModels) == 0 {
		t.Fatalf("direct account option must include modelCheckModels: %+v err=%v", direct, err)
	}
	if _, err := db.Exec(`UPDATE groups SET system_account_id='sys-2' WHERE id IN ('g1','g3')`); err != nil {
		t.Fatal(err)
	}
	wrongGroupOwner, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 50})
	if err != nil || len(wrongGroupOwner) != 0 {
		t.Fatalf("foreign group owner must not expose options: %+v err=%v", wrongGroupOwner, err)
	}
	if _, err := db.Exec(`UPDATE groups SET system_account_id='sys-1' WHERE id IN ('g1','g3')`); err != nil {
		t.Fatal(err)
	}
	history, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "history", Keyword: "beta", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != "a2" {
		t.Fatalf("history options=%+v", history)
	}
	otherTenant, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-2", Purpose: "run", Limit: 50})
	if err != nil || len(otherTenant) != 1 || otherTenant[0].ID != "a3" {
		t.Fatalf("sys-2 options=%+v err=%v", otherTenant, err)
	}
	global, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{AllSystemAccounts: true, Purpose: "run", Limit: 50})
	if err != nil || len(global) != 2 || !containsAccountOption(global, "a1") || !containsAccountOption(global, "a3") || containsAccountOption(global, "instance-1") {
		t.Fatalf("global options=%+v err=%v", global, err)
	}
	globalInstance, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{AllSystemAccounts: true, Purpose: "run", AccountID: "instance-1", Limit: 1})
	if err != nil || len(globalInstance) != 0 {
		t.Fatalf("global options must not expose authorized instance: %+v err=%v", globalInstance, err)
	}
	globalHistory, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{AllSystemAccounts: true, Purpose: "history", Limit: 50})
	if err != nil || containsAccountOption(globalHistory, "instance-1") {
		t.Fatalf("global history must not expose authorized instance: %+v err=%v", globalHistory, err)
	}
	crossTenant, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a3", Limit: 1})
	if err != nil || len(crossTenant) != 0 {
		t.Fatalf("cross-tenant account option must be hidden: %+v err=%v", crossTenant, err)
	}
	authorizedRun, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 50})
	if err != nil || len(authorizedRun) != 2 || authorizedRun[1].ID != "instance-1" {
		t.Fatalf("authorized run option must require matching grant and binding: %+v err=%v", authorizedRun, err)
	}
	if _, err := db.Exec(`UPDATE resource_authorizations SET scope='read' WHERE id='authz-1'`); err != nil {
		t.Fatal(err)
	}
	wrongScope, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 50})
	if err != nil || containsAccountOption(wrongScope, "instance-1") {
		t.Fatalf("non-use authorization must stay hidden: %+v err=%v", wrongScope, err)
	}
	if _, err := db.Exec(`UPDATE resource_authorizations SET scope='use' WHERE id='authz-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at='2000-01-01T00:00:00Z' WHERE id='authz-1'`); err != nil {
		t.Fatal(err)
	}
	expiredRun, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 50})
	if err != nil || len(expiredRun) != 1 || expiredRun[0].ID != "a1" {
		t.Fatalf("expired authorization must not be runnable: %+v err=%v", expiredRun, err)
	}
	expiredHistory, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "history", Limit: 50})
	if err != nil || !containsAccountOption(expiredHistory, "instance-1") {
		t.Fatalf("history may retain expired authorization visibility: %+v err=%v", expiredHistory, err)
	}
	if _, err := db.Exec(`UPDATE resource_authorizations SET expires_at=NULL WHERE id='authz-1'`); err != nil {
		t.Fatal(err)
	}
	authorizedSchedule, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "schedule", Limit: 50})
	if err != nil || len(authorizedSchedule) != 1 || authorizedSchedule[0].ID != "a1" {
		t.Fatalf("schedule options must remain owner-only: %+v err=%v", authorizedSchedule, err)
	}
	limited, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].ID != "a1" {
		t.Fatalf("limit must apply after owner+authorized merge: %+v err=%v", limited, err)
	}
	if _, err := db.Exec(`UPDATE group_accounts SET account_authorization_id='wrong' WHERE account_id='instance-1'`); err != nil {
		t.Fatal(err)
	}
	wrongBinding, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "instance-1", Limit: 1})
	if err != nil || len(wrongBinding) != 0 {
		t.Fatalf("mismatched authorization binding must stay hidden: %+v err=%v", wrongBinding, err)
	}
	if _, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "run", Limit: 50}); err == nil {
		t.Fatal("unscoped account options must fail closed")
	}
	options := source.ModelCheckOptions()
	if options.DefaultModel != "gpt-5.6-sol" || options.DefaultProfile != "quick" || len(options.SupportedModels) == 0 {
		t.Fatalf("catalog=%+v", options)
	}
}

func containsAccountOption(items []AccountOption, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// TestListAccountOptionsMergesAccountSupportedModels 验证定点模型候选为
// 目录可检模型 ∪ 账户支持模型：目录模型顺序与解析不变，目录外的账户支持
// 模型去重后按字典序追加；协议不兼容或无支持行的账户保持原行为。
func TestListAccountOptionsMergesAccountSupportedModels(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/merge-options.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,type TEXT,status TEXT,schedulable INTEGER,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT,availability_schedule_json TEXT,health_check_endpoint_mode TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g1','sys-1',1),('g2','sys-2',1)`); err != nil {
		t.Fatal(err)
	}
	// a1: 目录与支持模型有交集（gpt-5.6-sol 重复登记），支持模型含目录外条目。
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('a1','sys-1','Alpha','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'responses_json')`); err != nil {
		t.Fatal(err)
	}
	// a2: health_check_endpoint_mode 协议族不兼容（messages_json），支持模型不得并入。
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('a2','sys-1','Beta','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'messages_json')`); err != nil {
		t.Fatal(err)
	}
	// a3: 无支持模型行，必须保持纯目录候选。
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('a3','sys-1','Gamma','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'responses_json')`); err != nil {
		t.Fatal(err)
	}
	// source-1: 被 a4 实例授权引用的物理账户，mode 与支持模型齐全。
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('source-1','sys-2','Source','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'responses_json')`); err != nil {
		t.Fatal(err)
	}
	// a4: 授权实例。
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('a4','sys-1','Instance','openai','profile_openai_openai_v1','openai','1','api_key','active',1,NULL,NULL,NULL,'authz-1','source-1',NULL,NULL,'responses_json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('a1','sys-1','g1',NULL,1),('a2','sys-1','g1',NULL,1),('a3','sys-1','g1',NULL,1),('source-1','sys-2','g2',NULL,1),('a4','sys-1','g1','authz-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations VALUES ('authz-1','account','source-1','sys-2','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('a1','gpt-5.6-sol'),('a1','grok-4.6'),('a1','deepseek-v4.1-flash'),('a1','grok-4.5'),('a2','deepseek-v4.1-flash'),('source-1','grok-4.5')`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1})
	if err != nil || len(merged) != 1 {
		t.Fatalf("a1 option=%+v err=%v", merged, err)
	}
	wantA1 := []string{"gpt-5.6-sol", "deepseek-v4.1-flash", "grok-4.5", "grok-4.6"}
	if got := merged[0].ModelCheckModels; strings.Join(got, "|") != strings.Join(wantA1, "|") {
		t.Fatalf("a1 modelCheckModels=%v, want %v", got, wantA1)
	}
	incompatible, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a2", Limit: 1})
	if err != nil || len(incompatible) != 1 {
		t.Fatalf("a2 option=%+v err=%v", incompatible, err)
	}
	if got := incompatible[0].ModelCheckModels; len(got) != 0 {
		t.Fatalf("a2 modelCheckModels=%v, want catalog-only (empty after account restriction)", got)
	}
	catalogOnly, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a3", Limit: 1})
	if err != nil || len(catalogOnly) != 1 {
		t.Fatalf("a3 option=%+v err=%v", catalogOnly, err)
	}
	wantCatalog := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"}
	if got := catalogOnly[0].ModelCheckModels; strings.Join(got, "|") != strings.Join(wantCatalog, "|") {
		t.Fatalf("a3 modelCheckModels=%v, want catalog %v", got, wantCatalog)
	}
	instance, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a4", Limit: 1})
	if err != nil || len(instance) != 1 {
		t.Fatalf("a4 option=%+v err=%v", instance, err)
	}
	// source-1 的支持模型限制（仅 grok-4.5）同时约束目录候选：目录模型
	// gpt-5.6-sol 不在限制内被滤除，仅剩支持模型并入的 grok-4.5。
	wantInstance := []string{"grok-4.5"}
	if got := instance[0].ModelCheckModels; strings.Join(got, "|") != strings.Join(wantInstance, "|") {
		t.Fatalf("a4 modelCheckModels=%v, want source-merged %v", got, wantInstance)
	}
}
