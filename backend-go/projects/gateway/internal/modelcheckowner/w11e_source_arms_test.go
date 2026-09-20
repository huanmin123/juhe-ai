package modelcheckowner

// w11e business_source 错误臂：NewBusinessTargetSource/CheckContract/Resolve/
// resolveAuthorizedTarget/buildRequest 的数据驱动与取消上下文失败臂，以及
// 凭据解密、代理客户端等纯函数的剩余分支。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func w11eOpenBusinessDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/w11e-business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("DDL 失败: %v", err)
		}
	}
	return db
}

// w11eSeedAccount 写入一个可解析的 openai api_key 账户并返回凭据信封。
func w11eSeedAccount(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	envelope := testCredentialEnvelope(t, "secret", fmt.Sprintf(`{"api_key":"key-%s","supported_endpoint_modes":["responses_sse"]}`, id))
	if _, err := db.Exec(`INSERT OR IGNORE INTO provider_protocol_profiles(id,enabled,base_url) VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES (?,'sys-1','group-1',1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES (?,'sys-1','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,?)`, id, envelope, "Account "+id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO model_quality_policies VALUES ('sys-1',4,'quick',1,82,'fallback',15)`); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func w11eNewSource(t *testing.T, db *sql.DB) *BusinessTargetSource {
	t.Helper()
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC) }
	return source
}

func TestW11ESourceConstructorAndFactoryArms(t *testing.T) {
	if _, err := NewBusinessTargetSource(nil, false, "secret"); err == nil || !strings.Contains(err.Error(), "credential secret") {
		t.Fatalf("nil 数据库必须拒绝: %v", err)
	}
	db := w11eOpenBusinessDB(t)
	if _, err := NewBusinessTargetSource(db, false, "  "); err == nil || !strings.Contains(err.Error(), "credential secret") {
		t.Fatalf("空 secret 必须拒绝: %v", err)
	}
	// OpenBusinessTargetSource 直接传播连接失败。
	if _, closeFn, err := OpenBusinessTargetSource(context.Background(), Config{Enabled: false}); err == nil || closeFn != nil {
		t.Fatalf("未启用配置必须失败: %v", err)
	}
	if _, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, CredentialSecret: "secret", StoreMode: "oracle"}); err == nil || !strings.Contains(err.Error(), "store mode is invalid") {
		t.Fatalf("非法存储模式必须拒绝: %v", err)
	}
	if _, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, CredentialSecret: "secret", StoreMode: "sqlite"}); err == nil || !strings.Contains(err.Error(), "SQLite path is required") {
		t.Fatalf("空 SQLite 路径必须拒绝: %v", err)
	}
	if _, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, CredentialSecret: "secret", StoreMode: "sqlite", BusinessDatabasePath: t.TempDir() + "/missing.db"}); err == nil {
		t.Fatal("CheckContract 失败必须让打开失败（缺表）")
	}
}

func TestW11ESourceComparisonResolverAndContractArms(t *testing.T) {
	var nilSource *BusinessTargetSource
	if nilSource.Resolver() != nil || nilSource.ComparisonResolver() != nil {
		t.Fatal("nil source 的解析器必须为 nil")
	}
	db := w11eOpenBusinessDB(t)
	w11eSeedAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	resolver := source.ComparisonResolver()
	if _, err := resolver(context.Background(), RunRequest{}); err == nil || !strings.Contains(err.Error(), "comparison target is not configured") {
		t.Fatalf("未启用可信对比必须拒绝: %v", err)
	}
	if _, err := resolver(context.Background(), RunRequest{TrustedComparison: true, TrustedComparisonAccountID: "acct-1"}); err == nil || !strings.Contains(err.Error(), "system account is not configured") {
		t.Fatalf("缺少可信对比租户必须拒绝: %v", err)
	}
	var nilContract *BusinessTargetSource
	if err := nilContract.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil source 契约必须失败: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.CheckContract(canceled); err == nil || !strings.Contains(err.Error(), "open J3b Business source contract") {
		t.Fatalf("canceled 契约事务必须失败: %v", err)
	}
}

func TestW11ESourceResolveValidationArms(t *testing.T) {
	var nilSource *BusinessTargetSource
	if _, err := nilSource.Resolve(context.Background(), RunRequest{}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil source 必须拒绝: %v", err)
	}
	db := w11eOpenBusinessDB(t)
	w11eSeedAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	base := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}
	for name, request := range map[string]RunRequest{
		"missing system account": {TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"},
		"wrong target type":      {SystemAccountID: "sys-1", TargetType: "group", TargetID: "acct-1", Model: "gpt-5.6-sol"},
		"missing target":         {SystemAccountID: "sys-1", TargetType: "account", Model: "gpt-5.6-sol"},
		"missing model":          {SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1"},
	} {
		if _, err := source.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "request is incomplete") {
			t.Fatalf("%s 必须拒绝: %v", name, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Resolve(canceled, base); err == nil || !strings.Contains(err.Error(), "open J3b Business target transaction") {
		t.Fatalf("canceled 事务必须失败: %v", err)
	}
	if _, err := source.Resolve(context.Background(), base); err != nil {
		t.Fatalf("基线必须可解析: %v", err)
	}
}

func TestW11ESourceResolveDataDrivenFailures(t *testing.T) {
	db := w11eOpenBusinessDB(t)
	w11eSeedAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}
	baseline := w11eAccountField(t, db, "credentials_encrypted")
	reset := func(t *testing.T) {
		t.Helper()
		w11eExec(t, db, `UPDATE accounts SET provider_code='openai',provider_protocol_profile_id='profile_openai_openai_v1',type='api_key',credentials_encrypted=?,dispatch_revision=7,status='active' WHERE id='acct-1'`, baseline)
	}
	assertResolveFailure := func(t *testing.T, want string) {
		t.Helper()
		if _, err := source.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("期望错误包含 %q: %v", want, err)
		}
	}
	// 供应商 profile 不存在。
	w11eExec(t, db, `UPDATE accounts SET provider_code='w11e-unknown' WHERE id='acct-1'`)
	assertResolveFailure(t, "provider profile does not support model")
	reset(t)
	// 凭据类型不支持。
	w11eExec(t, db, `UPDATE accounts SET type='w11e-weird' WHERE id='acct-1'`)
	assertResolveFailure(t, "credential type is unsupported")
	reset(t)
	// 凭据信封损坏。
	w11eExec(t, db, `UPDATE accounts SET credentials_encrypted='v1:not:base64:!' WHERE id='acct-1'`)
	assertResolveFailure(t, "envelope is invalid")
	reset(t)
	// dispatch revision 无效。
	w11eExec(t, db, `UPDATE accounts SET dispatch_revision=0 WHERE id='acct-1'`)
	assertResolveFailure(t, "dispatch revision is invalid")
	reset(t)
	// OAuth 凭据落在非 Codex 兼容 profile。
	w11eExec(t, db, `UPDATE accounts SET type='oauth',credentials_encrypted=? WHERE id='acct-1'`, testCredentialEnvelope(t, "secret", `{"access_token":"token","supported_endpoint_modes":["responses_sse"]}`))
	assertResolveFailure(t, "OAuth credential is incompatible with OpenAI provider profile")
	reset(t)
	// 模型映射表读取失败。
	w11eExec(t, db, `DROP TABLE account_model_mappings`)
	assertResolveFailure(t, "model mapping")
	w11eExec(t, db, businessSourceContractDDL()[8])
	// 支持模型表读取失败。
	w11eExec(t, db, `DROP TABLE account_supported_models`)
	assertResolveFailure(t, "supported models")
	w11eExec(t, db, businessSourceContractDDL()[7])
	// 配置了账户支持模型但未开放请求模型。
	w11eExec(t, db, `INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-terra')`)
	assertResolveFailure(t, "model restriction does not allow model")
	w11eExec(t, db, `DELETE FROM account_supported_models WHERE account_id='acct-1'`)
	if _, err := source.Resolve(context.Background(), request); err != nil {
		t.Fatalf("复位后必须可解析: %v", err)
	}
}

func w11eExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 %q 失败: %v", query, err)
	}
}

// w11eSeedVariant 写入指定供应商/profile/端点模式/凭据的账户。
func w11eSeedVariant(t *testing.T, db *sql.DB, id, provider, profileID, mode, credentialJSON string) {
	t.Helper()
	w11eExec(t, db, `INSERT OR IGNORE INTO provider_protocol_profiles(id,enabled,base_url) VALUES (?,1,'https://w11e.invalid/v1')`, profileID)
	w11eExec(t, db, `INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES (?,'sys-1','group-1',1)`, id)
	credentialType := "api_key"
	if strings.Contains(credentialJSON, "access_token") {
		credentialType = "oauth"
	}
	w11eExec(t, db, `INSERT INTO accounts VALUES (?,'sys-1',?,?,'openai',?,3,7,'active',1,?,NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,?)`, id, provider, profileID, credentialType, mode, testCredentialEnvelope(t, "secret", credentialJSON), "Account "+id)
}

func w11eAccountField(t *testing.T, db *sql.DB, column string) any {
	t.Helper()
	var value any
	if err := db.QueryRow(`SELECT ` + column + ` FROM accounts WHERE id='acct-1'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestW11ESourceAuthorizedTargetArms(t *testing.T) {
	db := w11eOpenBusinessDB(t)
	envelope := w11eSeedAccount(t, db, "seed-acct")
	sourceEnvelope := testCredentialEnvelope(t, "secret", `{"api_key":"source-key","supported_endpoint_modes":["responses_sse"]}`)
	w11eExec(t, db, `INSERT INTO accounts VALUES ('acct-auth','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,6,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,'grant-1','source-1',NULL,'Authorized')`, envelope)
	w11eExec(t, db, `INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('acct-auth','sys-1','group-1','grant-1',1)`)
	w11eExec(t, db, `INSERT INTO resource_authorizations VALUES ('grant-1','account','source-1','sys-1','sys-1','use','active',NULL)`)
	w11eExec(t, db, `INSERT INTO accounts VALUES ('source-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,8,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Source')`, sourceEnvelope)
	source := w11eNewSource(t, db)
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-auth", Model: "gpt-5.6-sol"}
	if _, err := source.Resolve(context.Background(), request); err != nil {
		t.Fatalf("授权账户基线必须可解析: %v", err)
	}
	instanceBaseline := w11eAccountFields(t, db, "acct-auth")
	sourceBaseline := w11eAccountFields(t, db, "source-1")
	reset := func(t *testing.T) {
		t.Helper()
		w11eRestoreAccount(t, db, "acct-auth", instanceBaseline)
		w11eRestoreAccount(t, db, "source-1", sourceBaseline)
	}
	assertFailure := func(t *testing.T, want string) {
		t.Helper()
		if _, err := source.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("期望错误包含 %q: %v", want, err)
		}
	}
	// 实例状态不可用。
	w11eExec(t, db, `UPDATE accounts SET status='disabled' WHERE id='acct-auth'`)
	assertFailure(t, "authorized account is unavailable")
	reset(t)
	// 授权行状态失效。
	w11eExec(t, db, `UPDATE resource_authorizations SET status='revoked' WHERE id='grant-1'`)
	if _, err := source.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not exist or is outside scope") {
		t.Fatalf("失效授权必须 404: %v", err)
	}
	w11eExec(t, db, `UPDATE resource_authorizations SET status='active' WHERE id='grant-1'`)
	// 实例可用时间窗非法。
	w11eExec(t, db, `UPDATE accounts SET availability_schedule_json='{' WHERE id='acct-auth'`)
	assertFailure(t, "evaluate J3b authorized instance availability schedule")
	reset(t)
	// 实例在可用时间窗之外。
	w11eExec(t, db, `UPDATE accounts SET availability_schedule_json='{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}' WHERE id='acct-auth'`)
	assertFailure(t, "authorized instance account is outside availability schedule")
	reset(t)
	// 来源账户在可用时间窗之外。
	w11eExec(t, db, `UPDATE accounts SET availability_schedule_json='{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}' WHERE id='source-1'`)
	assertFailure(t, "authorized source account is outside availability schedule")
	reset(t)
	// 来源账户状态不可用。
	w11eExec(t, db, `UPDATE accounts SET status='cooldown' WHERE id='source-1'`)
	assertFailure(t, "authorized account is unavailable")
	reset(t)
	// 来源凭据损坏。
	w11eExec(t, db, `UPDATE accounts SET credentials_encrypted='v1:bad:bad:bad' WHERE id='source-1'`)
	assertFailure(t, "envelope is invalid")
	reset(t)
	// 来源 OAuth 与 OpenAI profile 不兼容。
	w11eExec(t, db, `UPDATE accounts SET type='oauth',credentials_encrypted=? WHERE id='source-1'`, testCredentialEnvelope(t, "secret", `{"access_token":"token","supported_endpoint_modes":["responses_sse"]}`))
	assertFailure(t, "OAuth credential is incompatible with OpenAI provider profile")
	reset(t)
	// 来源供应商 profile 缺失。
	w11eExec(t, db, `UPDATE accounts SET provider_code='w11e-unknown' WHERE id='source-1'`)
	assertFailure(t, "provider profile does not support model")
	reset(t)
	// 来源 dispatch revision 无效。
	w11eExec(t, db, `UPDATE accounts SET dispatch_revision=0 WHERE id='source-1'`)
	assertFailure(t, "source account dispatch revision is invalid")
	reset(t)
	w11eExec(t, db, `UPDATE accounts SET dispatch_revision=0 WHERE id='acct-auth'`)
	assertFailure(t, "account dispatch revision is invalid")
	reset(t)
	// 授权链查询失败（表被删，任一读取路径失败关闭）。
	w11eExec(t, db, `DROP TABLE resource_authorizations`)
	if _, err := source.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "read J3b Business") {
		t.Fatalf("表缺失必须失败关闭: %v", err)
	}
	w11eExec(t, db, businessSourceContractDDL()[5])
	w11eExec(t, db, `INSERT INTO resource_authorizations VALUES ('grant-1','account','source-1','sys-1','sys-1','use','active',NULL)`)
	if _, err := source.Resolve(context.Background(), request); err != nil {
		t.Fatalf("复位后授权账户必须可解析: %v", err)
	}
}

func w11eAccountFields(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT provider_code,provider_protocol_profile_id,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,account_expires_at,cooldown_until,last_error_code,availability_schedule_json,credentials_encrypted,proxy_profile_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,name FROM accounts WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("账户 %s 不存在", id)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	for index, column := range columns {
		fields[column] = values[index]
	}
	return fields
}

func w11eRestoreAccount(t *testing.T, db *sql.DB, id string, fields map[string]any) {
	t.Helper()
	w11eExec(t, db, `UPDATE accounts SET provider_code=?,provider_protocol_profile_id=?,type=?,config_revision=?,dispatch_revision=?,status=?,schedulable=?,health_check_endpoint_mode=?,account_expires_at=?,cooldown_until=?,last_error_code=?,availability_schedule_json=?,credentials_encrypted=?,proxy_profile_id=?,authorization_instance_authorization_id=?,authorization_instance_source_account_id=?,deleted_at=?,name=? WHERE id=?`,
		fields["provider_code"], fields["provider_protocol_profile_id"], fields["type"], fields["config_revision"], fields["dispatch_revision"], fields["status"], fields["schedulable"], fields["health_check_endpoint_mode"], fields["account_expires_at"], fields["cooldown_until"], fields["last_error_code"], fields["availability_schedule_json"], fields["credentials_encrypted"], fields["proxy_profile_id"], fields["authorization_instance_authorization_id"], fields["authorization_instance_source_account_id"], fields["deleted_at"], fields["name"], id)
}

func TestW11ESourceBuildRequestValidationArms(t *testing.T) {
	db := w11eOpenBusinessDB(t)
	w11eSeedAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	var nilSource *BusinessTargetSource
	if _, err := nilSource.BuildScopedRequest(context.Background(), ManagementScope{}, RunCommand{}); err == nil || !strings.Contains(err.Error(), "management scope is incomplete") {
		t.Fatalf("非法 scope 必须拒绝: %v", err)
	}
	if _, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-1", SelectedSystemAccountID: ""}, RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "management scope is incomplete") {
		t.Fatalf("非法 scope 必须拒绝: %v", err)
	}
	for name, command := range map[string]RunCommand{
		"missing model": {TargetID: "acct-1"},
		"missing id":    {Model: "gpt-5.6-sol"},
	} {
		if _, err := source.BuildRequest(context.Background(), "sys-1", command); err == nil || !strings.Contains(err.Error(), "request target is incomplete") {
			t.Fatalf("%s 必须拒绝: %v", name, err)
		}
	}
	// 空 TargetType 默认为 account。
	request, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || request.TargetType != "account" {
		t.Fatalf("默认 target type 必须是 account: request=%+v err=%v", request, err)
	}
	// 非法 profile。
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "medium"}); err == nil || !strings.Contains(err.Error(), "policy profile is invalid") {
		t.Fatalf("非法 profile 必须拒绝: %v", err)
	}
	// 可信对比缺少独立账户。
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol", TrustedComparison: true}); err == nil || !strings.Contains(err.Error(), "distinct account") {
		t.Fatalf("可信对比缺少账户必须拒绝: %v", err)
	}
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "acct-1"}); err == nil || !strings.Contains(err.Error(), "distinct account") {
		t.Fatalf("可信对比同账户必须拒绝: %v", err)
	}
	// 全局 scope 下可信对比账户不存在。
	if _, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-admin", AllSystemAccounts: true}, RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "w11e-missing"}); err == nil || !strings.Contains(err.Error(), "does not exist or is outside scope") {
		t.Fatalf("全局可信对比缺账户必须 404: %v", err)
	}
	// 可信对比账户供应商不同：gpt 与 openai 都支持 gpt-5.6-sol。
	w11eSeedVariant(t, db, "acct-gpt", "gpt", "profile_gpt_openai_v1", "responses_json", `{"access_token":"gpt-access","account_id":"w11e-chatgpt","supported_endpoint_modes":["responses_json","responses_sse"]}`)
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-1", Model: "gpt-5.6-sol", TrustedComparison: true, TrustedComparisonID: "acct-gpt"}); err == nil || !strings.Contains(err.Error(), "相同供应商") {
		t.Fatalf("跨供应商可信对比必须拒绝: %v", err)
	}
	// 可信对比账户协议不同：glm anthropic vs glm openai chat（模型 glm-5.2）。
	w11eSeedVariant(t, db, "acct-glm-a", "glm", "profile_glm_coding_anthropic_v1", "messages_json", `{"api_key":"glm-a","supported_endpoint_modes":["messages_json"]}`)
	w11eSeedVariant(t, db, "acct-glm-c", "glm", "profile_glm_coding_openai_v1", "chat_json", `{"api_key":"glm-c","supported_endpoint_modes":["chat_json"]}`)
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-glm-a", Model: "glm-5.2", TrustedComparison: true, TrustedComparisonID: "acct-glm-c"}); err == nil || !strings.Contains(err.Error(), "相同协议") {
		t.Fatalf("跨协议可信对比必须拒绝: %v", err)
	}
	// 同协议不同 profile：glm general vs glm coding。
	w11eSeedVariant(t, db, "acct-glm-g", "glm", "profile_glm_general_openai_v1", "chat_json", `{"api_key":"glm-g","supported_endpoint_modes":["chat_json"]}`)
	if _, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetID: "acct-glm-c", Model: "glm-5.2", TrustedComparison: true, TrustedComparisonID: "acct-glm-g"}); err == nil || !strings.Contains(err.Error(), "相同供应商协议配置") {
		t.Fatalf("跨 profile 可信对比必须拒绝: %v", err)
	}
	// 目标账户不存在的全局解析 404。
	if _, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-admin", AllSystemAccounts: true}, RunCommand{TargetID: "w11e-missing", Model: "gpt-5.6-sol"}); err == nil {
		var requestError *RequestError
		if !errors.As(err, &requestError) || requestError.StatusCode != http.StatusNotFound {
			t.Fatalf("全局缺账户必须 404: %v", err)
		}
	}
	// 目标属主为空。
	w11eExec(t, db, `UPDATE accounts SET system_account_id='' WHERE id='acct-1'`)
	if _, err := source.targetSystemAccountID(context.Background(), "acct-1"); err == nil || !strings.Contains(err.Error(), "owner is empty") {
		t.Fatalf("空属主必须拒绝: %v", err)
	}
	w11eExec(t, db, `UPDATE accounts SET system_account_id='sys-1' WHERE id='acct-1'`)
	// targetSystemAccountID 入参缺失。
	if _, err := source.targetSystemAccountID(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "global target is incomplete") {
		t.Fatalf("空目标必须拒绝: %v", err)
	}
	var nilForTarget *BusinessTargetSource
	if _, err := nilForTarget.targetSystemAccountID(context.Background(), "acct-1"); err == nil || !strings.Contains(err.Error(), "global target is incomplete") {
		t.Fatalf("nil source 全局目标必须拒绝: %v", err)
	}
	// 全局属主查询失败（表被删）。
	w11eExec(t, db, `DROP TABLE accounts`)
	if _, err := source.targetSystemAccountID(context.Background(), "acct-1"); err == nil || !strings.Contains(err.Error(), "global target owner") {
		t.Fatalf("全局属主查询失败必须传播: %v", err)
	}
}

func TestW11ESourcePolicyAndFenceArms(t *testing.T) {
	db := w11eOpenBusinessDB(t)
	w11eSeedAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	// 策略越界必须整体拒绝。
	for _, update := range []string{
		`UPDATE model_quality_policies SET penalty_threshold=30 WHERE system_account_id='sys-1'`,
		`UPDATE model_quality_policies SET penalty_threshold=101 WHERE system_account_id='sys-1'`,
		`UPDATE model_quality_policies SET recovery_interval_minutes=5 WHERE system_account_id='sys-1'`,
		`UPDATE model_quality_policies SET recovery_interval_minutes=10081 WHERE system_account_id='sys-1'`,
		`UPDATE model_quality_policies SET profile='medium' WHERE system_account_id='sys-1'`,
		`UPDATE model_quality_policies SET penalty_action='w11e-action' WHERE system_account_id='sys-1'`,
	} {
		w11eExec(t, db, update)
		if _, _, _, _, _, _, err := source.readPolicy(context.Background(), "sys-1"); err == nil || !strings.Contains(err.Error(), "quality policy is invalid") {
			t.Fatalf("非法策略必须拒绝（%s）: %v", update, err)
		}
		w11eExec(t, db, `UPDATE model_quality_policies SET penalty_threshold=82,recovery_interval_minutes=15,profile='quick',penalty_action='fallback' WHERE system_account_id='sys-1'`)
	}
	// 缺省策略行时返回内置默认值。
	if profile, revision, _, threshold, action, interval, err := source.readPolicy(context.Background(), "w11e-no-row"); err != nil || profile != "quick" || revision != "0" || threshold != 70 || action != "fallback" || interval != 10 {
		t.Fatalf("默认策略=%+v err=%v", profile, err)
	}
	// 策略表读取失败。
	w11eExec(t, db, `DROP TABLE model_quality_policies`)
	if _, _, _, _, _, _, err := source.readPolicy(context.Background(), "sys-1"); err == nil || !strings.Contains(err.Error(), "quality policy") {
		t.Fatalf("策略读取失败必须传播: %v", err)
	}
	w11eExec(t, db, businessSourceContractDDL()[6])
	// fence 入参不完整与事务取消。
	if _, err := source.readTargetFence(context.Background(), "  ", "acct-1", Target{}); err == nil || !strings.Contains(err.Error(), "fence request is incomplete") {
		t.Fatalf("fence 入参缺失必须拒绝: %v", err)
	}
	var nilFence *BusinessTargetSource
	if _, err := nilFence.readTargetFence(context.Background(), "sys-1", "acct-1", Target{}); err == nil || !strings.Contains(err.Error(), "fence request is incomplete") {
		t.Fatalf("nil source fence 必须拒绝: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.readTargetFence(canceled, "sys-1", "acct-1", Target{}); err == nil || !strings.Contains(err.Error(), "fence transaction") {
		t.Fatalf("canceled fence 必须失败: %v", err)
	}
}

func TestW11EDecryptCredentialHelpers(t *testing.T) {
	// 信封结构错误。
	for _, envelope := range []string{"", "v2:a:b:c", "v1:not-base64!:a:b", "v1:a:not-base64!:b", "v1::::"} {
		if _, err := decryptCredentialPlaintext("secret", envelope); err == nil || !strings.Contains(err.Error(), "envelope") {
			t.Fatalf("非法信封 %q 必须拒绝: %v", envelope, err)
		}
	}
	// 空白明文。
	if _, err := decryptCredentialPlaintext("secret", testCredentialEnvelope(t, "secret", "   ")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("空白明文必须拒绝: %v", err)
	}
	// decryptCredential：结构化 api_key、原始 token、JSON 缺 token 字段。
	apiKey := testCredentialEnvelope(t, "secret", `{"api_key":"key"}`)
	if token, err := decryptCredential("secret", apiKey); err != nil || token != "key" {
		t.Fatalf("api_key token=%q err=%v", token, err)
	}
	raw := testCredentialEnvelope(t, "secret", "raw-token")
	if token, err := decryptCredential("secret", raw); err != nil || token != "raw-token" {
		t.Fatalf("raw token=%q err=%v", token, err)
	}
	accessToken := testCredentialEnvelope(t, "secret", `{"access_token":"access"}`)
	if token, err := decryptCredential("secret", accessToken); err != nil || token != "access" {
		t.Fatalf("access_token token=%q err=%v", token, err)
	}
	noToken := testCredentialEnvelope(t, "secret", `{"metadata":"x"}`)
	if _, err := decryptCredential("secret", noToken); err == nil || !strings.Contains(err.Error(), "no supported token field") {
		t.Fatalf("缺 token 字段必须拒绝: %v", err)
	}
	// parseCredentialFields 分类。
	if _, structured, err := parseCredentialFields(""); err == nil || structured {
		t.Fatalf("空明文必须报错: %v", err)
	}
	if _, structured, err := parseCredentialFields("{invalid"); err == nil || !structured {
		t.Fatalf("非法 JSON 必须报错: %v", err)
	}
	if _, structured, err := parseCredentialFields("null"); err == nil || !structured {
		t.Fatalf("JSON null 必须报错: %v", err)
	}
	if _, structured, err := parseCredentialFields("42"); err == nil || !structured || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("JSON 数值必须报错: %v", err)
	}
	if _, structured, err := parseCredentialFields("[1,2]"); err == nil || !structured {
		t.Fatalf("JSON 数组必须报错: %v", err)
	}
	if _, _, err := parseCredentialFields(`"text"`); err == nil {
		t.Fatalf("JSON 字符串必须报错: %v", err)
	}
	if _, structured, err := parseCredentialFields("plain-token"); err != nil || structured {
		t.Fatalf("原始 token 不应视为结构化: %v", err)
	}
	// decryptAccountCredentialMaterial：非法 base_url 类型、account_id、quota、oauth_type、endpoint_modes。
	if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", `{"api_key":"k","base_url":42}`), "api_key"); err == nil {
		t.Fatal("base_url 非字符串必须拒绝")
	}
	if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", `{"access_token":"a","account_id":""}`), "oauth"); err == nil || !strings.Contains(err.Error(), "ChatGPT account ID") {
		t.Fatalf("空 account_id 必须拒绝: %v", err)
	}
	if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", `{"access_token":"a","client_secret":"s","quota_project_id":" "}`), "google_oauth"); err == nil || !strings.Contains(err.Error(), "quota_project_id") {
		t.Fatalf("空白 quota_project_id 必须拒绝: %v", err)
	}
	if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", `{"access_token":"a","client_secret":"s","oauth_type":" "}`), "google_oauth"); err == nil || !strings.Contains(err.Error(), "oauth_type") {
		t.Fatalf("空白 oauth_type 必须拒绝: %v", err)
	}
	if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", `{"access_token":"a","client_id":"c","quota_project_id":"qp","oauth_type":"google","supported_endpoint_modes":["responses_sse"]}`), "google_oauth"); err != nil {
		t.Fatalf("完整 google_oauth 必须可解析: %v", err)
	}
	for _, credential := range []string{
		`{"access_token":"a","supported_endpoint_modes":"responses_sse"}`,
		`{"access_token":"a","supported_endpoint_modes":[]}`,
		`{"access_token":"a","supported_endpoint_modes":[42]}`,
		`{"access_token":"a","supported_endpoint_modes":[" padded "]}`,
	} {
		if _, err := decryptAccountCredentialMaterial("secret", testCredentialEnvelope(t, "secret", credential), "oauth"); err == nil || !strings.Contains(err.Error(), "supported_endpoint_modes") {
			t.Fatalf("非法 endpoint modes 必须拒绝（%s）: %v", credential, err)
		}
	}
	// decryptCredentialStringField：非结构化、字段缺失、字段存在。
	if _, found, err := decryptCredentialStringField("secret", raw, "base_url"); err != nil || found {
		t.Fatalf("原始 token 无字段: %v", err)
	}
	if _, found, err := decryptCredentialStringField("secret", apiKey, "base_url"); err != nil || found {
		t.Fatalf("缺失字段必须未找到: %v", err)
	}
	if value, found, err := decryptCredentialStringField("secret", testCredentialEnvelope(t, "secret", `{"api_key":"k","oauth_type":"google"}`), "oauth_type"); err != nil || !found || value != "google" {
		t.Fatalf("字段读取=%q found=%t err=%v", value, found, err)
	}
	// decryptCredentialBaseURL：非结构化与缺失 base_url 均返回空。
	if value, err := decryptCredentialBaseURL("secret", raw); err != nil || value != "" {
		t.Fatalf("原始 token base_url=%q err=%v", value, err)
	}
	if value, err := decryptCredentialBaseURL("secret", apiKey); err != nil || value != "" {
		t.Fatalf("缺失 base_url=%q err=%v", value, err)
	}
}

func TestW11EBuildProxyClientArms(t *testing.T) {
	valid := sql.NullString{String: "proxy-1", Valid: true}
	// 缺 profile 直接返回 nil client。
	if client, err := buildProxyClient("secret", sql.NullString{}, sql.NullBool{}, sql.NullString{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{}); err != nil || client != nil {
		t.Fatalf("无代理引用必须返回 nil client: %v", err)
	}
	// 禁用/端口非法。
	if _, err := buildProxyClient("secret", valid, sql.NullBool{Bool: false, Valid: true}, sql.NullString{String: "http", Valid: true}, sql.NullString{String: "proxy.example", Valid: true}, sql.NullInt64{Int64: 0, Valid: true}, sql.NullString{}, sql.NullString{}); err == nil || !strings.Contains(err.Error(), "proxy profile is unavailable") {
		t.Fatalf("禁用代理必须拒绝: %v", err)
	}
	if _, err := buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "http", Valid: true}, sql.NullString{String: "proxy.example", Valid: true}, sql.NullInt64{Int64: 70000, Valid: true}, sql.NullString{}, sql.NullString{}); err == nil || !strings.Contains(err.Error(), "proxy profile is unavailable") {
		t.Fatalf("端口越界必须拒绝: %v", err)
	}
	// 协议不支持。
	if _, err := buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "ftp", Valid: true}, sql.NullString{String: "proxy.example", Valid: true}, sql.NullInt64{Int64: 8080, Valid: true}, sql.NullString{}, sql.NullString{}); err == nil || !strings.Contains(err.Error(), "proxy protocol is unsupported") {
		t.Fatalf("非法协议必须拒绝: %v", err)
	}
	// 用户名缺密码。
	if _, err := buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "http", Valid: true}, sql.NullString{String: "proxy.example", Valid: true}, sql.NullInt64{Int64: 8080, Valid: true}, sql.NullString{String: "user", Valid: true}, sql.NullString{}); err == nil || !strings.Contains(err.Error(), "proxy password is unavailable") {
		t.Fatalf("缺密码必须拒绝: %v", err)
	}
	// 密码信封损坏。
	if _, err := buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "http", Valid: true}, sql.NullString{String: "proxy.example", Valid: true}, sql.NullInt64{Int64: 8080, Valid: true}, sql.NullString{String: "user", Valid: true}, sql.NullString{String: "v1:bad:bad:bad", Valid: true}); err == nil || !strings.Contains(err.Error(), "proxy password is unavailable") {
		t.Fatalf("损坏密码必须拒绝: %v", err)
	}
	// socks5 映射为 socks5h。
	client, err := buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "socks5", Valid: true}, sql.NullString{String: "127.0.0.1", Valid: true}, sql.NullInt64{Int64: 1080, Valid: true}, sql.NullString{}, sql.NullString{})
	if err != nil || client == nil {
		t.Fatalf("socks5 代理必须可构建: %v", err)
	}
	// 带密码的 http 代理。
	password := testCredentialEnvelope(t, "secret", `{"password":"pw"}`)
	client, err = buildProxyClient("secret", valid, sql.NullBool{Bool: true, Valid: true}, sql.NullString{String: "http", Valid: true}, sql.NullString{String: "127.0.0.1", Valid: true}, sql.NullInt64{Int64: 8080, Valid: true}, sql.NullString{String: "user", Valid: true}, sql.NullString{String: password, Valid: true})
	if err != nil || client == nil {
		t.Fatalf("带密码代理必须可构建: %v", err)
	}
}

func TestW11ESourceSmallHelpers(t *testing.T) {
	// nowUTC 的 nil source 分支。
	var nilSource *BusinessTargetSource
	if nilSource.nowUTC().IsZero() {
		t.Fatal("nil source nowUTC 必须退回真实时钟")
	}
	// fenceValue 类型分支。
	if got := fenceValue(nil); got != "<null>" {
		t.Fatalf("null fence=%q", got)
	}
	if got := fenceValue([]byte{0xde, 0xad}); got != "bytes:dead" {
		t.Fatalf("bytes fence=%q", got)
	}
	if got := fenceValue(42); got != "int:42" {
		t.Fatalf("default fence=%q", got)
	}
	// sameStringSet 空白去重与差异。
	if !sameStringSet([]string{"  ", "a"}, []string{"a"}) {
		t.Fatal("空白成员必须忽略")
	}
	if sameStringSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("集合差异必须发现")
	}
	// sameHeaderValues 差异分支。
	left, right := http.Header{}, http.Header{}
	left.Set("X-A", "1")
	right.Set("X-A", "1")
	right.Set("X-B", "2")
	if sameHeaderValues(left, right) {
		t.Fatal("header 集合差异必须发现")
	}
	if !sameHeaderValues(left, left) {
		t.Fatal("相同 header 必须相等")
	}
	// accountUnavailableAt 的零值与非法时间。
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	if !accountUnavailableAt("0001-01-01T00:00:00Z", "", "", now, false) {
		t.Fatal("零值过期时间必须视为不可用")
	}
	if !accountUnavailableAt("not-a-time", "", "", now, false) {
		t.Fatal("非法过期时间必须失败关闭")
	}
	if accountUnavailableAt("", "", "", now, false) {
		t.Fatal("空值必须视为可用")
	}
	// modelcheckprofile.NormalizeToken 已由上游覆盖；此处确认 protocol 序列化可用。
	if string(modelcheckprofile.ProtocolOpenAIResponses) == "" {
		t.Fatal("协议常量不能为空")
	}
}
