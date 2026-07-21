package managementaccounttrafficmigration

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

func TestMigrateSelfAuthorizedAccountUsesGranteeScopeAndInvalidatesRuntime(t *testing.T) {
	store := &migrationStoreStub{result: migrationStoreResult("authorized"), found: true}
	runtime := &runtimeMigratorStub{}
	cache := &migrationInvalidatorStub{}
	page := &migrationPageDataStub{}
	service := NewService(Options{Store: store, RuntimeMigrator: runtime, AccountLookupInvalidator: cache, GatewayInvalidator: cache, PageDataPublisher: page, Now: fixedMigrationNow})

	result, err := service.Migrate(context.Background(), Input{ActorSystemAccountID: "sys_self", ActorRole: "user", SelfOnly: true, SourceAccountID: "acct_source", TargetAccountID: "acct_target", SourceStatus: SourceStatusTemporaryUnavailable})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if store.input.EffectiveSystemAccountID != "sys_self" || store.input.CanAccessAll {
		t.Fatalf("store scope = %+v", store.input)
	}
	if runtime.input.AffinityScope == nil || runtime.input.AffinityScope.SystemAccountID != "sys_self" || runtime.input.AffinityScope.GroupID != "group_1" {
		t.Fatalf("runtime input = %+v", runtime.input)
	}
	if result.MigratedSessionCount != 2 || result.SourceStatus != SourceStatusTemporaryUnavailable {
		t.Fatalf("result = %+v", result)
	}
	if cache.lookupCalls != 1 || cache.gatewayCalls != 1 || len(page.runtimeChanges) != 1 {
		t.Fatalf("cache=%+v page=%+v", cache, page)
	}
	if got := page.runtimeChanges[0].OwnerSystemAccountIDs; len(got) != 1 || got[0] != "sys_self" {
		t.Fatalf("page owners = %#v", got)
	}
}

func TestMigrateAdminGlobalScopeAndOwnerPageDataOwners(t *testing.T) {
	store := &migrationStoreStub{result: migrationStoreResult("owner"), found: true}
	page := &migrationPageDataStub{}
	service := NewService(Options{Store: store, GranteeReader: &migrationGranteeStub{ids: []string{"sys_grantee"}}, PageDataPublisher: page, Now: fixedMigrationNow})

	_, err := service.Migrate(context.Background(), Input{ActorSystemAccountID: "sys_admin", ActorRole: "admin", SourceAccountID: "acct_source", TargetAccountID: "acct_target", SourceStatus: SourceStatusDisabled})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !store.input.CanAccessAll || store.input.EffectiveSystemAccountID != "" {
		t.Fatalf("store scope = %+v", store.input)
	}
	owners := page.runtimeChanges[0].OwnerSystemAccountIDs
	if len(owners) != 2 || owners[0] != "sys_grantee" || owners[1] != "sys_owner" {
		t.Fatalf("page owners = %#v", owners)
	}
}

func fixedMigrationNow() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }

func migrationStoreResult(accessType string) port.ManagementAccountTrafficMigrationResult {
	systemID, ownerID, authID := "sys_owner", "sys_owner", ""
	if accessType == "authorized" {
		systemID, ownerID, authID = "sys_self", "sys_owner", "auth_1"
	}
	return port.ManagementAccountTrafficMigrationResult{
		SourceAccount: port.ManagementAccountTrafficMigrationAccount{ID: "acct_source", SystemAccountID: systemID, OwnerSystemAccountID: ownerID, Name: "源账户", ProviderCode: "openai", Type: "api_key", Status: "temporary_unavailable", Schedulable: true, BoundGroupID: "group_1", AccountAuthorizationID: authID, AccessType: accessType},
		TargetAccount: port.ManagementAccountTrafficMigrationAccount{ID: "acct_target", SystemAccountID: systemID, OwnerSystemAccountID: ownerID, Name: "目标账户", ProviderCode: "openai", Type: "api_key", Status: "active", Schedulable: true, BoundGroupID: "group_1", AccessType: "owner"},
		GroupID:       "group_1", SourceCooldownUntil: fixedMigrationNow().Add(3 * time.Second), SourceChanged: true,
	}
}

type migrationStoreStub struct {
	input  port.ManagementAccountTrafficMigrationInput
	result port.ManagementAccountTrafficMigrationResult
	found  bool
	err    error
}

func (s *migrationStoreStub) MigrateManagementAccountTraffic(_ context.Context, input port.ManagementAccountTrafficMigrationInput) (port.ManagementAccountTrafficMigrationResult, bool, error) {
	s.input = input
	return s.result, s.found, s.err
}

type runtimeMigratorStub struct{ input RuntimeMigrationInput }

func (s *runtimeMigratorStub) MigrateAccountTrafficRuntime(_ context.Context, input RuntimeMigrationInput) (int, error) {
	s.input = input
	return 2, nil
}

type migrationInvalidatorStub struct{ lookupCalls, gatewayCalls int }

func (s *migrationInvalidatorStub) InvalidateAccountLookupCache(context.Context, string) error {
	s.lookupCalls++
	return nil
}
func (s *migrationInvalidatorStub) InvalidateGatewayRuntime(context.Context, string) error {
	s.gatewayCalls++
	return nil
}

type migrationPageDataStub struct{ runtimeChanges []accountpagedata.ChangeInput }

func (*migrationPageDataStub) PublishAccountStaticChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}
func (s *migrationPageDataStub) PublishAccountRuntimeChange(_ context.Context, input accountpagedata.ChangeInput) error {
	s.runtimeChanges = append(s.runtimeChanges, input)
	return nil
}

type migrationGranteeStub struct{ ids []string }

func (s *migrationGranteeStub) ListAccountAuthorizationGranteeIDs(context.Context, string) ([]string, error) {
	return s.ids, nil
}
