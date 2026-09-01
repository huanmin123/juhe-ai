package modelcheckowner

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

func TestBusinessTargetSourceReadsScopedActiveAccount(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,type TEXT,config_revision INTEGER,dispatch_revision INTEGER,status TEXT,schedulable INTEGER,health_check_endpoint_mode TEXT,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,credentials_encrypted TEXT,proxy_profile_id TEXT,availability_schedule_json TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT,name TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,provider_code TEXT,enabled INTEGER,protocol_code TEXT,base_url TEXT)`,
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY,enabled INTEGER,type TEXT,host TEXT,port INTEGER,username TEXT,password_encrypted TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-1","supported_endpoint_modes":["responses_sse"]}`)
	sourceEnvelope := testCredentialEnvelope(t, "secret", `{"api_key":"source-key","supported_endpoint_modes":["responses_sse","interactions_json"]}`)
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1','openai',1,'openai_responses','https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-1','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Account 1')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-2','sys-2','openai','profile_openai_openai_v1','openai','api_key',3,2,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Account 2')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-2','sys-2',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-2','sys-2','group-2',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-3','sys-1','openai','profile_openai_openai_v1','openai','api_key',4,9,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Account 3')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-3','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys-1',4,'quick',1,82,'fallback',15)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	globalRequest, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-admin", AllSystemAccounts: true}, RunCommand{TargetType: "account", TargetID: "acct-2", Model: "gpt-5.6-sol"})
	if err != nil || globalRequest.SystemAccountID != "sys-2" || globalRequest.ActorSystemAccountID != "sys-admin" {
		t.Fatalf("global request must preserve target owner and administrator actor: request=%+v err=%v", globalRequest, err)
	}
	globalComparison, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-admin", AllSystemAccounts: true}, RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "full", TrustedComparison: true, TrustedComparisonID: "acct-2"})
	if err != nil || globalComparison.SystemAccountID != "sys-1" || globalComparison.TrustedComparisonSystemAccountID != "sys-2" {
		t.Fatalf("global comparison must independently freeze both account owners: request=%+v err=%v", globalComparison, err)
	}
	globalComparisonTarget, err := source.ComparisonResolver()(context.Background(), globalComparison)
	if err != nil || globalComparisonTarget.ConfigRevision != "3" || globalComparisonTarget.DispatchRevision != 2 {
		t.Fatalf("global comparison must resolve in its own target tenant: target=%+v err=%v", globalComparisonTarget, err)
	}
	selectedRequest, err := source.BuildScopedRequest(context.Background(), ManagementScope{ActorSystemAccountID: "sys-admin", SelectedSystemAccountID: "sys-2"}, RunCommand{TargetType: "account", TargetID: "acct-2", Model: "gpt-5.6-sol"})
	if err != nil || selectedRequest.SystemAccountID != "sys-2" || selectedRequest.ActorSystemAccountID != "sys-admin" {
		t.Fatalf("selected request must preserve target owner and administrator actor: request=%+v err=%v", selectedRequest, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='acct-1'`, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"11:00","end":"13:00"}]}`); err != nil {
		t.Fatal(err)
	}
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "https://example.invalid/v1" || target.TargetName != "Account 1" || target.TargetOwnerSystemAccountID != "sys-1" || target.GroupID != "group-1" || target.DispatchRevision != 7 || target.Headers.Get("Authorization") != "Bearer key-1" || target.EndpointMode != "responses_sse" || !sameStringSet(target.SupportedEndpointModes, []string{"responses_sse"}) {
		t.Fatalf("target=%+v headers=%v", target, target.Headers)
	}
	if !target.OwnPhysicalAccount {
		t.Fatalf("physical account target must retain immutable physical-account fact: %+v", target)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','chat_completions',1)`); err != nil {
		t.Fatal(err)
	}
	mappedOwner, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || mappedOwner.UpstreamModel != "gpt-5.6-terra" || mappedOwner.SourceEndpointFamily != modelcheckprofile.EndpointResponses || mappedOwner.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions || mappedOwner.UpstreamProtocol != modelcheckprofile.ProtocolOpenAIChat || mappedOwner.UpstreamEndpointMode != modelcheckprofile.EndpointModeChatSSE {
		t.Fatalf("owner Responses to Chat mapping must freeze family and request shape: target=%+v err=%v", mappedOwner, err)
	}
	if _, err := db.Exec(`DELETE FROM account_model_mappings WHERE account_id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM account_supported_models WHERE account_id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='acct-1'`, `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[7],"start":"13:00","end":"14:00"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("schedule-denied account must reject target resolution")
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=? WHERE id='acct-1'`, `{`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("invalid availability schedule must reject target resolution")
	}
	if _, err := db.Exec(`UPDATE accounts SET availability_schedule_json=NULL WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE groups SET system_account_id='sys-2' WHERE id='group-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("foreign group owner must reject target resolution")
	}
	if _, err := db.Exec(`UPDATE groups SET system_account_id='sys-1' WHERE id='group-1'`); err != nil {
		t.Fatal(err)
	}
	for _, update := range []string{
		`UPDATE accounts SET account_expires_at='2000-01-01T00:00:00Z' WHERE id='acct-1'`,
		`UPDATE accounts SET account_expires_at=NULL,cooldown_until='2099-01-01T00:00:00Z' WHERE id='acct-1'`,
		`UPDATE accounts SET cooldown_until='not-a-timestamp' WHERE id='acct-1'`,
		`UPDATE accounts SET cooldown_until=NULL,last_error_code='account_expired' WHERE id='acct-1'`,
	} {
		if _, err := db.Exec(update); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("account availability fence must reject update %q", update)
		}
	}
	if _, err := db.Exec(`UPDATE accounts SET last_error_code=NULL WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-authorized','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,6,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,'authorization-1',NULL,NULL,'Authorized Account')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-authorized','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations VALUES ('authorization-1','account','source-account-1','sys-1','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('source-account-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,8,'active',1,'responses_json',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Source Account')`, sourceEnvelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET authorization_instance_source_account_id='source-account-1' WHERE id='acct-authorized'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE group_accounts SET account_authorization_id='authorization-1' WHERE account_id='acct-authorized'`); err != nil {
		t.Fatal(err)
	}
	authorizedRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol"})
	if err != nil || authorizedRequest.OwnPhysicalAccount || authorizedRequest.TargetID != "acct-authorized" {
		t.Fatalf("authorized account must resolve through source account: request=%+v err=%v", authorizedRequest, err)
	}
	nowCalls := 0
	source.now = func() time.Time {
		nowCalls++
		return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	}
	authorizedTarget, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol"})
	if nowCalls != 1 {
		t.Fatalf("authorized target availability must use one frozen clock value, calls=%d", nowCalls)
	}
	if err != nil || authorizedTarget.TargetName != "Authorized Account" || authorizedTarget.TargetOwnerSystemAccountID != "sys-1" || authorizedTarget.GroupID != "group-1" || authorizedTarget.ProviderCode != "openai" || authorizedTarget.Headers.Get("Authorization") != "Bearer source-key" || authorizedTarget.OwnPhysicalAccount || authorizedTarget.ConfigRevision != "3" || authorizedTarget.SourceConfigRevision != "3" || authorizedTarget.DispatchRevision != 6 || authorizedTarget.SourceDispatchRevision != 8 || authorizedTarget.CredentialSourceAccountID != "source-account-1" || authorizedTarget.EndpointMode != "responses_sse" || !sameStringSet(authorizedTarget.SupportedEndpointModes, []string{"responses_sse", "interactions_json"}) {
		t.Fatalf("authorized target must use source credentials: target=%+v err=%v", authorizedTarget, err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models(account_id,model) VALUES ('source-account-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('source-account-1','gpt-5.6-sol','responses','gpt-5.6-terra','chat_completions',1)`); err != nil {
		t.Fatal(err)
	}
	mappedAuthorized, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol"})
	if err != nil || mappedAuthorized.UpstreamModel != "gpt-5.6-terra" || mappedAuthorized.SourceEndpointFamily != modelcheckprofile.EndpointResponses || mappedAuthorized.UpstreamEndpointFamily != modelcheckprofile.EndpointChatCompletions || mappedAuthorized.UpstreamProtocol != modelcheckprofile.ProtocolOpenAIChat || mappedAuthorized.UpstreamEndpointMode != modelcheckprofile.EndpointModeChatSSE || mappedAuthorized.CredentialSourceAccountID != "source-account-1" {
		t.Fatalf("authorized source mapping must freeze source identity and upstream family: target=%+v err=%v", mappedAuthorized, err)
	}
	if _, err := db.Exec(`DELETE FROM account_model_mappings WHERE account_id='source-account-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM account_supported_models WHERE account_id='source-account-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol", SourceConfigRevision: "stale"}); err == nil || !strings.Contains(err.Error(), "source account config revision") {
		t.Fatalf("stale source config revision must be rejected: err=%v", err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol", SourceDispatchRevision: 7}); err == nil || !strings.Contains(err.Error(), "source account dispatch revision") {
		t.Fatalf("stale source dispatch revision must be rejected: err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-authorized-source','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,6,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,'source-account-1',NULL,'Authorized Source Account')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-authorized-source','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	sourceOnly, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-authorized-source", Model: "gpt-5.6-sol"})
	if err == nil || sourceOnly.TargetID != "" || !strings.Contains(err.Error(), "outside scope") {
		t.Fatalf("source-bound account without a valid grant must remain private: request=%+v err=%v", sourceOnly, err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-2", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("cross-account target must be rejected")
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", ConfigRevision: "2"}); err == nil || !strings.Contains(err.Error(), "config revision") {
		t.Fatalf("stale config revision err=%v", err)
	}
	if _, err := db.Exec(`UPDATE accounts SET status='quality_isolated',schedulable=0 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("ordinary runs must reject a quality-isolated account")
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", TriggerKind: string(SchedulerQualityRecovery)}); err != nil {
		t.Fatalf("quality recovery must resolve isolated unschedulable account: %v", err)
	}
	if _, err := db.Exec(`UPDATE accounts SET status='active',schedulable=1 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	request, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if request.PolicyRevision != "4" || !request.ManualEnforcementEnabled || !request.OwnPhysicalAccount || request.Threshold != 82 || request.PenaltyAction != "fallback" || request.RecoveryIntervalMinutes != 15 || request.ConfigRevision != "3" || request.SourceConfigRevision != "3" || request.SourceDispatchRevision != 7 || request.ProviderCode != "openai" {
		t.Fatalf("built request=%+v", request)
	}
	if _, err := db.Exec(`UPDATE model_quality_policies SET manual_enforcement_enabled=0 WHERE system_account_id='sys-1'`); err != nil {
		t.Fatal(err)
	}
	diagnosticRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || diagnosticRequest.ManualEnforcementEnabled {
		t.Fatalf("manual policy disable must freeze into request: request=%+v err=%v", diagnosticRequest, err)
	}
	if _, err := db.Exec(`UPDATE model_quality_policies SET profile='full',manual_enforcement_enabled=1 WHERE system_account_id='sys-1'`); err != nil {
		t.Fatal(err)
	}
	quickRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "quick"})
	if err != nil || quickRequest.Profile != "quick" || quickRequest.ProbeSetVersion != modelcheckprofile.QuickProbeSetVersion {
		t.Fatalf("explicit quick request must not be constrained by quality policy: request=%+v err=%v", quickRequest, err)
	}
	comparisonRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "full", TrustedComparison: true, TrustedComparisonID: "acct-3"})
	if err != nil {
		t.Fatal(err)
	}
	if !comparisonRequest.TrustedComparison || comparisonRequest.TrustedComparisonAccountID != "acct-3" || comparisonRequest.TrustedComparisonSystemAccountID != "sys-1" || comparisonRequest.TrustedComparisonConfigRevision != "4" || comparisonRequest.TrustedComparisonDispatchRevision != 9 || comparisonRequest.TrustedComparisonSourceConfigRevision != "4" || comparisonRequest.TrustedComparisonSourceDispatchRevision != 9 || comparisonRequest.ProbeSetVersion != modelcheckprofile.ProbeSetVersion || !strings.Contains(comparisonRequest.IdentityKey, "comparison:sys-1:acct-3:4") {
		t.Fatalf("trusted comparison request=%+v", comparisonRequest)
	}
	comparisonTarget, err := source.ComparisonResolver()(context.Background(), comparisonRequest)
	if err != nil || comparisonTarget.ConfigRevision != "4" || comparisonTarget.DispatchRevision != 9 {
		t.Fatalf("trusted comparison target=%+v err=%v", comparisonTarget, err)
	}
	for name, mutate := range map[string]func(*RunRequest){
		"config":          func(request *RunRequest) { request.TrustedComparisonConfigRevision = "3" },
		"dispatch":        func(request *RunRequest) { request.TrustedComparisonDispatchRevision = 8 },
		"source config":   func(request *RunRequest) { request.TrustedComparisonSourceConfigRevision = "3" },
		"source dispatch": func(request *RunRequest) { request.TrustedComparisonSourceDispatchRevision = 8 },
	} {
		t.Run("comparison resolver stale "+name, func(t *testing.T) {
			stale := comparisonRequest
			mutate(&stale)
			if _, err := source.ComparisonResolver()(context.Background(), stale); err == nil {
				t.Fatalf("comparison resolver must propagate stale %s revision", name)
			}
		})
	}
	quickComparisonRequest, err := source.BuildRequest(context.Background(), "sys-1", RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol", Profile: "quick", TrustedComparison: true, TrustedComparisonID: "acct-3"})
	if err != nil || !quickComparisonRequest.TrustedComparison || quickComparisonRequest.Profile != "quick" || quickComparisonRequest.ProbeSetVersion != modelcheckprofile.QuickProbeSetVersion {
		t.Fatalf("quick profile trusted comparison must preserve quick probe contract and comparison fences: request=%+v err=%v", quickComparisonRequest, err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil {
		t.Fatal("configured account models must restrict the static catalog")
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','responses',1)`); err != nil {
		t.Fatal(err)
	}
	mapped, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || mapped.UpstreamModel != "gpt-5.6-terra" {
		t.Fatalf("enabled mapping must resolve the configured upstream model: target=%+v err=%v", mapped, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='proxy-1' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "proxy profile") {
		t.Fatalf("proxy-configured account with missing profile must fail closed, err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO proxy_profiles VALUES ('proxy-1',1,'http','127.0.0.1',8080,'',NULL)`); err != nil {
		t.Fatal(err)
	}
	proxied, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || proxied.Client == nil {
		t.Fatalf("valid proxy profile must produce an explicit client: target=%+v err=%v", proxied, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='proxy-1' WHERE id='acct-authorized'`); err != nil {
		t.Fatal(err)
	}
	instanceProxy, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol"})
	if err != nil || instanceProxy.Client == nil {
		t.Fatalf("authorized account without source proxy must fall back to instance proxy: target=%+v err=%v", instanceProxy, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='missing-instance-proxy' WHERE id='acct-authorized'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='proxy-1' WHERE id='source-account-1'`); err != nil {
		t.Fatal(err)
	}
	sourceProxy, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-authorized", Model: "gpt-5.6-sol"})
	if err != nil || sourceProxy.Client == nil {
		t.Fatalf("authorized account must prefer a valid source proxy over instance proxy: target=%+v err=%v", sourceProxy, err)
	}
}

func TestBusinessTargetSourceCheckContractIsReadOnly(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessTargetSourceCheckContractRejectsMissingHealthCheckEndpointMode(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddls := businessSourceContractDDL()
	ddls[0] = strings.Replace(ddls[0], ",health_check_endpoint_mode TEXT", "", 1)
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "accounts") {
		t.Fatalf("missing health_check_endpoint_mode must fail contract validation, err=%v", err)
	}
}

func TestBusinessTargetSourceCheckContractRejectsMissingUpstreamEndpointFamily(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ddls := businessSourceContractDDL()
	ddls[len(ddls)-1] = strings.Replace(ddls[len(ddls)-1], ",upstream_endpoint_family TEXT", "", 1)
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "account_model_mappings") {
		t.Fatalf("missing upstream_endpoint_family must fail contract validation, err=%v", err)
	}
}

func TestBusinessTargetSourceRejectsInvalidEndpointModeConfiguration(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-1','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	credential := func(plaintext string) string { return testCredentialEnvelope(t, "secret", plaintext) }
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',1,1,'active',1,'responses_sse',?)`, credential(`{"api_key":"key","supported_endpoint_modes":["responses_sse","images_json"]}`)); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}
	target, err := source.Resolve(context.Background(), request)
	if err != nil || target.EndpointMode != "responses_sse" || !sameStringSet(target.SupportedEndpointModes, []string{"responses_sse", "images_json"}) {
		t.Fatalf("selected executable mode and Node-only capabilities must remain explicit: target=%+v err=%v", target, err)
	}
	for _, tc := range []struct {
		name       string
		mode       string
		credential string
	}{
		{name: "profile mismatch", mode: "chat_json", credential: `{"api_key":"key","supported_endpoint_modes":["chat_json"]}`},
		{name: "selected mode absent", mode: "responses_sse", credential: `{"api_key":"key","supported_endpoint_modes":["responses_json"]}`},
		{name: "malformed list", mode: "responses_sse", credential: `{"api_key":"key","supported_endpoint_modes":"responses_sse"}`},
		{name: "unsupported selected mode", mode: "images_json", credential: `{"api_key":"key","supported_endpoint_modes":["images_json"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode=?,credentials_encrypted=? WHERE id='acct-1'`, tc.mode, credential(tc.credential)); err != nil {
				t.Fatal(err)
			}
			if _, err := source.Resolve(context.Background(), request); err == nil {
				t.Fatal("invalid endpoint mode configuration must fail closed")
			}
		})
	}
}

func TestBusinessTargetFenceDetectsMappingAndCredentialDrift(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-1","supported_endpoint_modes":["responses_sse"]}`)
	rotatedEnvelope := testCredentialEnvelope(t, "secret", `{"api_key":"key-2","supported_endpoint_modes":["responses_sse"]}`)
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct-1','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,7,'active',1,'responses_sse',NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,'Account 1')`, envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-1','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	fenceBefore, err := source.readTargetFence(context.Background(), "sys-1", "acct-1", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-1','gpt-5.6-terra')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','responses',1)`); err != nil {
		t.Fatal(err)
	}
	mapped, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || mapped.UpstreamModel != "gpt-5.6-terra" {
		t.Fatalf("mapping drift fixture target=%+v err=%v", mapped, err)
	}
	fenceAfterMapping, err := source.readTargetFence(context.Background(), "sys-1", "acct-1", mapped)
	if err != nil {
		t.Fatal(err)
	}
	if fenceAfterMapping == fenceBefore || sameTargetFence(target, mapped) {
		t.Fatalf("mapping change must move target fence: before=%s after=%s target=%+v mapped=%+v", fenceBefore, fenceAfterMapping, target, mapped)
	}
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted=? WHERE id='acct-1'`, rotatedEnvelope); err != nil {
		t.Fatal(err)
	}
	rotated, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || rotated.Headers.Get("Authorization") != "Bearer key-2" {
		t.Fatalf("credential drift fixture target=%+v err=%v", rotated, err)
	}
	fenceAfterCredential, err := source.readTargetFence(context.Background(), "sys-1", "acct-1", rotated)
	if err != nil {
		t.Fatal(err)
	}
	if fenceAfterCredential == fenceAfterMapping || sameTargetFence(mapped, rotated) {
		t.Fatalf("credential change must move target fence: mapping=%s credential=%s mapped=%+v rotated=%+v", fenceAfterMapping, fenceAfterCredential, mapped, rotated)
	}
}

func TestCredentialHeadersFollowProtocolAndType(t *testing.T) {
	cases := []struct {
		name     string
		protocol modelcheckprofile.Protocol
		typeName string
		wantAuth string
		wantKey  string
	}{
		{name: "anthropic api key", protocol: modelcheckprofile.ProtocolAnthropic, typeName: "api_key", wantKey: "key"},
		{name: "anthropic oauth", protocol: modelcheckprofile.ProtocolAnthropic, typeName: "oauth", wantAuth: "Bearer key"},
		{name: "gemini api key", protocol: modelcheckprofile.ProtocolGeminiNative, typeName: "api_key", wantKey: "key"},
		{name: "gemini oauth", protocol: modelcheckprofile.ProtocolGeminiNative, typeName: "google_oauth", wantAuth: "Bearer key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers, err := credentialHeaders("", "", tc.protocol, string(tc.protocol), tc.typeName, "key")
			if err != nil {
				t.Fatal(err)
			}
			if got := headers.Get("Authorization"); got != tc.wantAuth {
				t.Fatalf("Authorization=%q want %q", got, tc.wantAuth)
			}
			keyHeader := "x-api-key"
			if tc.protocol == modelcheckprofile.ProtocolGeminiNative {
				keyHeader = "x-goog-api-key"
			}
			if got := headers.Get(keyHeader); got != tc.wantKey {
				t.Fatalf("%s=%q want %q", keyHeader, got, tc.wantKey)
			}
		})
	}
	anthropicOAuthHeaders, err := credentialHeaders("anthropic", "profile_anthropic_anthropic_v1", modelcheckprofile.ProtocolAnthropic, "anthropic", "oauth", "key")
	if err != nil || anthropicOAuthHeaders.Get("anthropic-beta") != "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14" {
		t.Fatalf("Anthropic OAuth beta headers=%v err=%v", anthropicOAuthHeaders, err)
	}
	if anthropicOAuthHeaders.Get("user-agent") != "claude-cli/2.1.161 (external, cli)" || anthropicOAuthHeaders.Get("x-stainless-runtime") != "node" {
		t.Fatalf("Anthropic OAuth CLI identity headers=%v", anthropicOAuthHeaders)
	}
	if _, err := credentialHeaders("", "", modelcheckprofile.ProtocolOpenAIChat, "openai", "google_oauth", "key"); err == nil {
		t.Fatal("google_oauth must be rejected for OpenAI-compatible protocol")
	}
	if _, err := credentialHeaders("", "", modelcheckprofile.ProtocolGeminiNative, "gemini", "oauth", "key"); err == nil {
		t.Fatal("oauth must be rejected for Gemini native protocol")
	}
	if _, err := credentialHeaders("deepseek", "profile_deepseek_anthropic_v1", modelcheckprofile.ProtocolAnthropic, "anthropic", "oauth", "key"); err == nil {
		t.Fatal("DeepSeek Anthropic profile must reject OAuth")
	}
	if _, err := credentialHeaders("glm", "profile_glm_coding_anthropic_v1", modelcheckprofile.ProtocolAnthropic, "anthropic", "oauth", "key"); err == nil {
		t.Fatal("GLM Coding Anthropic profile must reject OAuth")
	}
	if _, err := credentialHeaders("", "", modelcheckprofile.ProtocolAnthropic, "anthropic", "google_oauth", "key"); err == nil {
		t.Fatal("google_oauth must be rejected for Anthropic protocol")
	}
	glmHeaders, err := credentialHeaders("glm", "profile_glm_coding_anthropic_v1", modelcheckprofile.ProtocolAnthropic, "anthropic", "api_key", "key")
	if err != nil || glmHeaders.Get("Authorization") != "Bearer key" || glmHeaders.Get("x-api-key") != "" {
		t.Fatalf("GLM Coding Anthropic API key headers=%v err=%v", glmHeaders, err)
	}
	if glmHeaders.Get("anthropic-beta") != "" || glmHeaders.Get("user-agent") != "" {
		t.Fatalf("GLM Coding API key must not inherit Anthropic OAuth headers=%v", glmHeaders)
	}
}

func TestDecryptProxyPasswordUsesPasswordField(t *testing.T) {
	envelope := testCredentialEnvelope(t, "secret", `{"password":"proxy-secret"}`)
	password, err := decryptProxyPassword("secret", envelope)
	if err != nil || password != "proxy-secret" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	if _, err := decryptProxyPassword("secret", testCredentialEnvelope(t, "secret", `{"api_key":"not-a-password"}`)); err == nil {
		t.Fatal("proxy password envelope without password field must fail closed")
	}
}

func TestDecryptAccountCredentialRejectsOAuthRefreshTokenWhenAccessTokenMissing(t *testing.T) {
	refreshOnly := testCredentialEnvelope(t, "secret", `{"refresh_token":"refresh-secret"}`)
	if token, err := decryptAccountCredential("secret", refreshOnly, "oauth"); err == nil || token != "" || !strings.Contains(err.Error(), "refresh_token only") {
		t.Fatalf("refresh-only OAuth must fail closed: token=%q err=%v", token, err)
	}
	accessPreferred := testCredentialEnvelope(t, "secret", `{"access_token":"access-secret","refresh_token":"refresh-secret"}`)
	token, err := decryptAccountCredential("secret", accessPreferred, "oauth")
	if err != nil || token != "access-secret" {
		t.Fatalf("access token must remain preferred: token=%q err=%v", token, err)
	}
	invalidJSON := testCredentialEnvelope(t, "secret", `{"metadata":"missing-token"}`)
	if _, err := decryptAccountCredential("secret", invalidJSON, "google_oauth"); err == nil {
		t.Fatal("OAuth credential without a usable token must fail closed")
	}
}

func TestCredentialMaterialUsesCredentialBaseURLAndRejectsUnknownJSON(t *testing.T) {
	envelope := testCredentialEnvelope(t, "secret", `{"api_key":"key","base_url":"https://custom.example/v1/"}`)
	baseURL, err := decryptCredentialBaseURL("secret", envelope)
	if err != nil || baseURL != "https://custom.example/v1" {
		t.Fatalf("credential base_url=%q err=%v", baseURL, err)
	}
	if _, err := decryptAccountCredential("secret", testCredentialEnvelope(t, "secret", `{"metadata":"not-a-token"}`), "api_key"); err == nil {
		t.Fatal("unknown credential JSON must not become an API key")
	}
	if _, err := decryptAccountCredential("secret", testCredentialEnvelope(t, "secret", `{"api_key":"key","metadata":"unexpected"}`), "api_key"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown credential fields must fail closed: %v", err)
	}
	if token, err := decryptAccountCredential("secret", testCredentialEnvelope(t, "secret", `{"access_token":"access","client_id":"client","expires_at":"2030-01-01T00:00:00Z"}`), "oauth"); err != nil || token != "access" {
		t.Fatalf("known OAuth metadata must remain accepted: token=%q err=%v", token, err)
	}
	if _, err := decryptCredentialBaseURL("secret", testCredentialEnvelope(t, "secret", `{"api_key":"key","base_url":"file:///tmp/secret"}`)); err == nil {
		t.Fatal("credential base_url must require an HTTP(S) URL")
	}
	if _, err := decryptCredentialBaseURL("secret", testCredentialEnvelope(t, "secret", `{"api_key":"key","base_url":"http://127.0.0.1/v1"}`)); err == nil {
		t.Fatal("credential base_url must reject loopback targets")
	}
}

func TestBusinessTargetSourceUsesCredentialBaseURLAndGeminiOAuthHeaders(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	credential := testCredentialEnvelope(t, "secret", `{"access_token":"google-access","refresh_token":"google-refresh","base_url":"https://credential.example/v1beta/","quota_project_id":"quota-project","supported_endpoint_modes":["generate_content_json"]}`)
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_gemini_native_v1beta',1,'https://profile.example')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('gemini-1','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('gemini-1','sys-1','gemini','profile_gemini_native_v1beta','gemini','google_oauth',1,1,'active',1,'generate_content_json',?)`, credential); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gemini-1", Model: "gemini-3.5-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "https://credential.example/v1beta" {
		t.Fatalf("credential base_url must override profile URL: %q", target.Endpoint)
	}
	if target.Headers.Get("Authorization") != "Bearer google-access" || target.Headers.Get("x-goog-api-key") != "" || target.Headers.Get("x-goog-user-project") != "quota-project" {
		t.Fatalf("Gemini OAuth headers=%v", target.Headers)
	}
}

func TestBusinessTargetSourceRoutesGPTOAuthThroughCodexAdapterIncludingAuthorizedSource(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_gpt_openai_v1',1,'https://profile.example/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	oauth := testCredentialEnvelope(t, "secret", `{"access_token":"oauth-access","account_id":"chatgpt-account","base_url":"https://custom.example/v1","supported_endpoint_modes":["responses_json","responses_sse"]}`)
	apiKey := testCredentialEnvelope(t, "secret", `{"api_key":"api-key","base_url":"https://custom.example/v1","supported_endpoint_modes":["responses_json","responses_sse"]}`)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('gpt-oauth','sys-1','gpt','profile_gpt_openai_v1','openai','oauth',1,1,'active',1,'responses_json',?)`, oauth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('gpt-oauth','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gpt-oauth", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != modelcheckprobe.OpenAIOAuthCodexBaseURL || target.UpstreamAdapter != modelcheckprobe.AdapterOpenAIOAuthCodex || target.Headers.Get("Authorization") != "Bearer oauth-access" || target.Headers.Get("chatgpt-account-id") != "chatgpt-account" {
		t.Fatalf("OAuth target=%+v", target)
	}
	if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode='chat_json' WHERE id='gpt-oauth'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gpt-oauth", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("OAuth chat mode must fail closed, err=%v", err)
	}
	if _, err := db.Exec(`UPDATE accounts SET health_check_endpoint_mode='responses_sse' WHERE id='gpt-oauth'`); err != nil {
		t.Fatal(err)
	}
	streamTarget, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gpt-oauth", Model: "gpt-5.6-sol"})
	if err != nil || streamTarget.UpstreamAdapter != modelcheckprobe.AdapterOpenAIOAuthCodex || streamTarget.Endpoint != modelcheckprobe.OpenAIOAuthCodexBaseURL {
		t.Fatalf("OAuth SSE target=%+v err=%v", streamTarget, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET type='api_key',credentials_encrypted=? WHERE id='gpt-oauth'`, apiKey); err != nil {
		t.Fatal(err)
	}
	apiTarget, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gpt-oauth", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if apiTarget.Endpoint != "https://custom.example/v1" || apiTarget.UpstreamAdapter != "" || apiTarget.Headers.Get("Authorization") != "Bearer api-key" {
		t.Fatalf("API-key target=%+v", apiTarget)
	}

	if _, err := db.Exec(`INSERT INTO resource_authorizations VALUES ('grant-1','account','gpt-source','sys-1','sys-1','use','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('gpt-source','sys-1','gpt','profile_gpt_openai_v1','openai','oauth',3,4,'active',1,'responses_sse',?)`, oauth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('gpt-source','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted,authorization_instance_authorization_id,authorization_instance_source_account_id) VALUES ('gpt-virtual','sys-1','gpt','profile_gpt_openai_v1','openai','oauth',5,6,'active',1,'responses_sse',?,'grant-1','gpt-source')`, apiKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('gpt-virtual','sys-1','group-1','grant-1',1)`); err != nil {
		t.Fatal(err)
	}
	virtual, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "gpt-virtual", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if virtual.OwnPhysicalAccount || virtual.CredentialSourceAccountID != "gpt-source" || virtual.Endpoint != modelcheckprobe.OpenAIOAuthCodexBaseURL || virtual.UpstreamAdapter != modelcheckprobe.AdapterOpenAIOAuthCodex || virtual.Headers.Get("Authorization") != "Bearer oauth-access" {
		t.Fatalf("authorized OAuth target=%+v", virtual)
	}
}

func TestOpenBusinessTargetSourceUsesReadOnlySQLiteURI(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source, closeSource, err := OpenBusinessTargetSource(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if source == nil || closeSource == nil {
		t.Fatal("factory returned incomplete source")
	}
	if err := closeSource(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenBusinessTargetConnectionAllowsWritesOnlyAfterHandoff(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	connection, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret", BusinessHandoffConfirmed: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var queryOnly int
	if err := connection.DB.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 0 {
		t.Fatalf("handoff owner connection must not be query-only: %d", queryOnly)
	}
	stats := connection.DB.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("SQLite Business owner must use one connection, stats=%+v", stats)
	}
	var foreignKeys int
	if err := connection.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("SQLite Business owner must enable foreign-key enforcement: %d", foreignKeys)
	}
}

func TestOpenBusinessTargetConnectionRejectsActiveNodeWriterAfterHandoff(t *testing.T) {
	_, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: filepath.Join(t.TempDir(), "business.db"), CredentialSecret: "secret", BusinessHandoffConfirmed: true})
	if err == nil || !strings.Contains(err.Error(), "Node writer") {
		t.Fatalf("confirmed handoff with active Node writer must fail closed, err=%v", err)
	}
}

func TestOpenBusinessTargetConnectionRejectsUnprovenSQLiteSchemaReady(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret", SchemaReady: true})
	if err == nil || !strings.Contains(err.Error(), "Business SQLite schema") {
		t.Fatalf("schemaReady must be proven against SQLite contract, err=%v", err)
	}
}

func TestOpenBusinessTargetConnectionSharesValidatedHandle(t *testing.T) {
	path := t.TempDir() + "/business.db"
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := OpenBusinessTargetConnection(context.Background(), Config{Enabled: true, StoreMode: "sqlite", BusinessDatabasePath: path, CredentialSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.DB == nil || connection.Source == nil || connection.Source.db != connection.DB {
		t.Fatal("source and connection must share DB handle")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
}

func businessSourceContractDDL() []string {
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,type TEXT,config_revision INTEGER,dispatch_revision INTEGER,status TEXT,schedulable INTEGER,health_check_endpoint_mode TEXT,account_expires_at TEXT,cooldown_until TEXT,last_error_code TEXT,credentials_encrypted TEXT,proxy_profile_id TEXT,availability_schedule_json TEXT,authorization_instance_authorization_id TEXT,authorization_instance_source_account_id TEXT,deleted_at TEXT,name TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER,base_url TEXT)`,
		`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY,enabled INTEGER,type TEXT,host TEXT,port INTEGER,username TEXT,password_encrypted TEXT)`,
		`CREATE TABLE group_accounts (account_id TEXT,system_account_id TEXT,group_id TEXT,account_authorization_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,system_account_id TEXT,enabled INTEGER)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY,resource_type TEXT,resource_id TEXT,resource_owner_system_account_id TEXT,grantee_system_account_id TEXT,scope TEXT,status TEXT,expires_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER)`,
		`CREATE TABLE account_supported_models (account_id TEXT,model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT,source_model TEXT,source_endpoint_family TEXT,upstream_model TEXT,upstream_endpoint_family TEXT,enabled INTEGER)`,
	}
}

func testCredentialEnvelope(t *testing.T, secret, plaintext string) string {
	t.Helper()
	key := sha256Bytes(secret)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	cut := len(sealed) - gcm.Overhead()
	return strings.Join([]string{"v1", base64.RawURLEncoding.EncodeToString(iv), base64.RawURLEncoding.EncodeToString(sealed[cut:]), base64.RawURLEncoding.EncodeToString(sealed[:cut])}, ":")
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
