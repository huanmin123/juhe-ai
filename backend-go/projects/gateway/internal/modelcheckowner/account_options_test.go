package modelcheckowner

import (
	"context"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestListAccountOptionsFiltersAndFreezesCatalog(t *testing.T) {
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range accountOptionsFixtureDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES ('g1','sys-1',1),('g2','sys-2', 1)`); err != nil {
		t.Fatal(err)
	}
	// v16 契约：探针身份 = (vendor_code, 健康检查连接 preset)；a5 的连接
	// preset 不在目录映射内，对应旧 profile_unknown 拒绝臂。
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,protocol_code,protocol_version,type,status,schedulable,health_check_connection_id,deleted_at) VALUES ('a1','sys-1','Alpha','gpt','gpt','openai','1','api_key','active', 1,'conn-a1',NULL),('a2','sys-1','Beta','gpt','gpt','openai','1','api_key','disabled', 1,'conn-a2',NULL),('a3','sys-2','Gamma','gpt','gpt','openai','1','api_key','active', 1,'conn-a3',NULL),('a4','sys-1','Custom','gpt','gpt','openai','1','custom','active', 1,'conn-a4',NULL),('a5','sys-1','Unsupported profile','gpt','gpt','openai','1','api_key','active', 1,'conn-a5',NULL)`)
	for _, row := range []string{
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a1','a1','gpt_openai_v1','https://options.invalid/v1', 1)`,
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a2','a2','gpt_openai_v1','https://options.invalid/v1', 1)`,
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a3','a3','gpt_openai_v1','https://options.invalid/v1', 1)`,
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a4','a4','gpt_openai_v1','https://options.invalid/v1', 1)`,
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a5','a5','preset_unknown','https://options.invalid/v1', 1)`,
	} {
		if _, err := db.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('a1','sys-1','g1',NULL, 1),('a2','sys-1','g1',NULL, 1),('a3','sys-2','g2',NULL, 1),('a4','sys-1','g1',NULL, 1),('a5','sys-1','g1',NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,scope,status,expires_at) VALUES ('authz-1','account','source-1','sys-2','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,protocol_code,protocol_version,type,status,schedulable,health_check_connection_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at) VALUES ('instance-1','sys-1','Shared','gpt','gpt','openai','1','api_key','active', 1,'conn-instance-1','authz-1','source-1',NULL),('source-1','sys-2','Source','gpt','gpt','openai','1','api_key','active', 1,'conn-source-1',NULL,NULL,NULL)`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-instance-1','instance-1','gpt_openai_v1','https://options.invalid/v1', 1),('conn-source-1','source-1','gpt_openai_v1','https://options.invalid/v1', 1)`)
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,group_id,enabled,system_account_id,account_authorization_id) VALUES ('instance-1','g1', 1,'sys-1','authz-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES ('g3','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,group_id,enabled,system_account_id,account_authorization_id) VALUES ('instance-1','g3', 1,'sys-1','authz-1')`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, "secret")
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
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range accountOptionsFixtureDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES ('g1','sys-1',1),('g2','sys-2', 1)`); err != nil {
		t.Fatal(err)
	}
	// v16 契约：账户身份 = (vendor_code, 健康检查连接 preset)。
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,protocol_code,protocol_version,type,status,schedulable,health_check_connection_id,health_check_endpoint_mode,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at) VALUES ('a1','sys-1','Alpha','gpt','gpt','openai','1','api_key','active', 1,'conn-a1','responses_json',NULL,NULL,NULL),('a2','sys-1','Beta','gpt','gpt','openai','1','api_key','active', 1,'conn-a2','messages_json',NULL,NULL,NULL),('a3','sys-1','Gamma','gpt','gpt','openai','1','api_key','active', 1,'conn-a3','responses_json',NULL,NULL,NULL),('source-1','sys-2','Source','gpt','gpt','openai','1','api_key','active', 1,'conn-source-1','responses_json',NULL,NULL,NULL),('a4','sys-1','Instance','gpt','gpt','openai','1','api_key','active', 1,'conn-a4','responses_json','authz-1','source-1',NULL)`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a1','a1','gpt_openai_v1','https://options.invalid/v1', 1),('conn-a2','a2','gpt_openai_v1','https://options.invalid/v1', 1),('conn-a3','a3','gpt_openai_v1','https://options.invalid/v1', 1),('conn-source-1','source-1','gpt_openai_v1','https://options.invalid/v1', 1),('conn-a4','a4','gpt_openai_v1','https://options.invalid/v1', 1)`)
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('a1','sys-1','g1',NULL, 1),('a2','sys-1','g1',NULL, 1),('a3','sys-1','g1',NULL, 1),('source-1','sys-2','g2',NULL, 1),('a4','sys-1','g1','authz-1', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,scope,status,expires_at) VALUES ('authz-1','account','source-1','sys-2','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	// v16 契约：支持模型行存 model_id，模型名经 models.public_model 解析。
	for _, pair := range [][2]string{{"a1", "gpt-5.6-sol"}, {"a1", "grok-4.6"}, {"a1", "deepseek-v4.1-flash"}, {"a1", "grok-4.5"}, {"a2", "deepseek-v4.1-flash"}, {"source-1", "grok-4.5"}} {
		seedMappingSupported(t, db, pair[0], pair[1])
	}
	source, err := NewBusinessTargetSource(db, "secret")
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
	// mode 分歧：实例不兼容而 source 兼容时，options 门必须跟实例 mode 走
	// （与 run 门 validateCredentialEndpointMode 同源），支持模型不得并入。
	if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode='messages_json' WHERE id='a4'`); err != nil {
		t.Fatal(err)
	}
	instanceIncompatible, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a4", Limit: 1})
	if err != nil || len(instanceIncompatible) != 1 {
		t.Fatalf("a4 incompatible option=%+v err=%v", instanceIncompatible, err)
	}
	if got := instanceIncompatible[0].ModelCheckModels; len(got) != 0 {
		t.Fatalf("a4 instance mode mismatch must keep catalog-only candidates: %v", got)
	}
	// 反向分歧：实例兼容而 source mode 为空时，仍按实例 mode 并入。
	if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode='responses_json' WHERE id='a4'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode=NULL WHERE id='source-1'`); err != nil {
		t.Fatal(err)
	}
	instanceSourceModeless, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a4", Limit: 1})
	if err != nil || len(instanceSourceModeless) != 1 {
		t.Fatalf("a4 source-modeless option=%+v err=%v", instanceSourceModeless, err)
	}
	if got := instanceSourceModeless[0].ModelCheckModels; strings.Join(got, "|") != strings.Join(wantInstance, "|") {
		t.Fatalf("a4 source mode must not gate the merge: %v", got)
	}
}

// TestListAccountOptionsTieredProtocolGateForCatalogScope 验证候选合并协议门
// 的两档语义：目录外的账户支持模型在账户 mode 属于协议一致性可检集合
// （chat_json/chat_sse/responses_json/responses_sse）时并入（chat_json 的
// openai 兼容账户可以看到自己的支持模型），images_json 等不可检形态仍排除；
// 目录内条目维持原协议族比对，responses_json 账户行为不回归。
func TestListAccountOptionsTieredProtocolGateForCatalogScope(t *testing.T) {
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range accountOptionsFixtureDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES ('g1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	// v16 契约：账户身份 = (vendor_code, 健康检查连接 preset)。
	w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,protocol_code,protocol_version,type,status,schedulable,health_check_connection_id,health_check_endpoint_mode,deleted_at) VALUES ('chat-acct','sys-1','Chat','gpt','gpt','openai','1','api_key','active', 1,'conn-chat','chat_json',NULL),('images-acct','sys-1','Images','gpt','gpt','openai','1','api_key','active', 1,'conn-images','images_json',NULL),('sse-acct','sys-1','SSE','gpt','gpt','openai','1','api_key','active', 1,'conn-sse','chat_sse',NULL),('responses-acct','sys-1','Responses','gpt','gpt','openai','1','api_key','active', 1,'conn-responses','responses_json',NULL)`)
	w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-chat','chat-acct','gpt_openai_v1','https://options.invalid/v1', 1),('conn-images','images-acct','gpt_openai_v1','https://options.invalid/v1', 1),('conn-sse','sse-acct','gpt_openai_v1','https://options.invalid/v1', 1),('conn-responses','responses-acct','gpt_openai_v1','https://options.invalid/v1', 1)`)
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('chat-acct','sys-1','g1',NULL, 1),('images-acct','sys-1','g1',NULL, 1),('sse-acct','sys-1','g1',NULL, 1),('responses-acct','sys-1','g1',NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"chat-acct", "gpt-5.6-sol"}, {"chat-acct", "deepseek-v4.1-flash"}, {"images-acct", "gpt-5.6-sol"}, {"images-acct", "deepseek-v4.1-flash"}, {"sse-acct", "grok-4.5"}, {"responses-acct", "deepseek-v4.1-flash"}} {
		seedMappingSupported(t, db, pair[0], pair[1])
	}
	source, err := NewBusinessTargetSource(db, "secret")
	if err != nil {
		t.Fatal(err)
	}
	option := func(accountID string) AccountOption {
		t.Helper()
		result, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: accountID, Limit: 1})
		if err != nil || len(result) != 1 {
			t.Fatalf("%s option=%+v err=%v", accountID, result, err)
		}
		return result[0]
	}
	// chat_json：目录外支持模型并入候选；目录内 gpt-5.6-sol 经目录前缀保留。
	chat := option("chat-acct")
	wantChat := []string{"gpt-5.6-sol", "deepseek-v4.1-flash"}
	if got := chat.ModelCheckModels; strings.Join(got, "|") != strings.Join(wantChat, "|") {
		t.Fatalf("chat-acct modelCheckModels=%v, want %v", got, wantChat)
	}
	// images_json：目录外不并入，目录前缀仅剩支持模型限制内的 gpt-5.6-sol。
	images := option("images-acct")
	wantImages := []string{"gpt-5.6-sol"}
	if got := images.ModelCheckModels; strings.Join(got, "|") != strings.Join(wantImages, "|") {
		t.Fatalf("images-acct modelCheckModels=%v, want %v", got, wantImages)
	}
	// chat_sse：目录外条目同样并入。
	sse := option("sse-acct")
	wantSSE := []string{"grok-4.5"}
	if got := sse.ModelCheckModels; strings.Join(got, "|") != strings.Join(wantSSE, "|") {
		t.Fatalf("sse-acct modelCheckModels=%v, want %v", got, wantSSE)
	}
	// responses_json：既有合并行为不回归。
	responses := option("responses-acct")
	wantResponses := []string{"deepseek-v4.1-flash"}
	if got := responses.ModelCheckModels; strings.Join(got, "|") != strings.Join(wantResponses, "|") {
		t.Fatalf("responses-acct modelCheckModels=%v, want %v", got, wantResponses)
	}
}

// TestListAccountOptionsSingleConnectionPoolRegressions 复刻生产 Source 的
// MaxOpenConns(1) 物理文件所有者池：AccountID 路径在嵌套查询期间必须已释放
// 外层 rows，否则嵌套查询永远等不到连接，请求在池信号量上自死锁（2026-09-22
// 模型检测页 15s 超时的根因）。池上限一旦放开，本测试对死锁回归不再敏感。
func TestListAccountOptionsSingleConnectionPoolRegressions(t *testing.T) {
	db, err := openPGTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, ddl := range accountOptionsFixtureDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO groups(id,system_account_id,enabled) VALUES ('g1','sys-1',1),('g2','sys-2', 1)`,
		`INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,protocol_code,protocol_version,type,status,schedulable,health_check_connection_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at) VALUES ('a1','sys-1','Alpha','gpt','gpt','openai','1','api_key','active', 1,'conn-a1',NULL,NULL,NULL),('instance-1','sys-1','Shared','gpt','gpt','openai','1','api_key','active', 1,'conn-instance-1','authz-1','source-1',NULL),('source-1','sys-2','Source','gpt','gpt','openai','1','api_key','active', 1,'conn-source-1',NULL,NULL,NULL)`,
		`INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-a1','a1','gpt_openai_v1','https://options.invalid/v1', 1),('conn-instance-1','instance-1','gpt_openai_v1','https://options.invalid/v1', 1),('conn-source-1','source-1','gpt_openai_v1','https://options.invalid/v1', 1)`,
		`INSERT INTO group_accounts (account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('a1','sys-1','g1',NULL, 1)`,
		`INSERT INTO resource_authorizations(id,resource_type,resource_id,resource_owner_system_account_id,grantee_system_account_id,scope,status,expires_at) VALUES ('authz-1','account','source-1','sys-2','sys-1','use','active',NULL)`,
		`INSERT INTO group_accounts (account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('instance-1','sys-1','g1','authz-1', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	seedMappingSupported(t, db, "a1", "gpt-5.6-sol")
	seedMappingSupported(t, db, "source-1", "gpt-5.6-sol")
	source, err := NewBusinessTargetSource(db, "secret")
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	direct, err := source.ListAccountOptions(ctx, AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "a1", Limit: 1})
	if err != nil {
		t.Fatalf("accountId option must not deadlock the single-connection pool: %v", err)
	}
	if len(direct) != 1 || direct[0].ID != "a1" || len(direct[0].ModelCheckModels) == 0 {
		t.Fatalf("direct account option must keep modelCheckModels: %+v", direct)
	}
	authorized, err := source.ListAccountOptions(ctx, AccountOptionsQuery{SystemAccountID: "sys-1", Purpose: "run", AccountID: "instance-1", Limit: 1})
	if err != nil {
		t.Fatalf("authorized accountId option must not deadlock the single-connection pool: %v", err)
	}
	if len(authorized) != 1 || authorized[0].ID != "instance-1" || len(authorized[0].ModelCheckModels) == 0 {
		t.Fatalf("authorized account option must keep modelCheckModels: %+v", authorized)
	}
}

// accountOptionsFixtureDDL 是 options 测试族的 v16 契约 fixture DDL（设计 §8
// 去供应商化）：探针身份列 vendor_code/connection_driver_code +
// health_check_connection_id，连接与模型表按新 schema。
func accountOptionsFixtureDDL() []string {
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,vendor_code TEXT,connection_driver_code TEXT,protocol_code TEXT,protocol_version TEXT,type TEXT,status TEXT,schedulable INTEGER,health_check_connection_id TEXT,health_check_endpoint_mode TEXT,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,availability_schedule_json TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT)`,
		`CREATE TABLE account_connections (id TEXT PRIMARY KEY,account_id TEXT,connection_preset_code TEXT,base_url TEXT,enabled INTEGER)`,
		`CREATE TABLE models (id TEXT PRIMARY KEY,vendor_code TEXT,public_model TEXT,upstream_model TEXT,scope TEXT,system_account_id TEXT,status TEXT,catalog_visible BOOLEAN)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model_id TEXT,PRIMARY KEY (account_id, model_id))`,
		`CREATE TABLE account_entry_mappings (id TEXT PRIMARY KEY,account_id TEXT,source_model TEXT,source_endpoint_family TEXT,target_connection_id TEXT,target_model_id TEXT,target_endpoint_family TEXT,enabled INTEGER)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
	}
}
