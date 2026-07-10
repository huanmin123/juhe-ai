package publicaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceAddCreatesTargetGroupPendingTestAndDoesNotExposeCredentials(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetDisplayName:         "管理员",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "主账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{" gpt-5.5 ", "gpt-5.5", "gpt-5.5-mini"}, true),
		Status:                    StatusActive,
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Action != "created" || !response.Target.Created || response.Target.GroupID == "" || response.Target.GroupCreated == nil || !*response.Target.GroupCreated {
		t.Fatalf("response target/action = %+v", response)
	}
	if response.Account == nil || response.Account.Status != StatusPendingTest || response.Account.Schedulable {
		t.Fatalf("response account = %+v, want pending_test and unschedulable", response.Account)
	}
	if got := response.Account.SupportedModels; len(got) != 2 || got[0] != "gpt-5.5" || got[1] != "gpt-5.5-mini" {
		t.Fatalf("supported models = %#v", got)
	}
	created := store.accounts[response.Account.ID]
	if created.Status != port.PublicAccountStatusPendingTest || created.Schedulable {
		t.Fatalf("stored account status/schedulable = %s/%v", created.Status, created.Schedulable)
	}
	if created.CredentialsEncrypted == "" ||
		strings.Contains(created.CredentialsEncrypted, "sk-public-account-secret") ||
		strings.Contains(created.CredentialsEncrypted, "api.openai.com") {
		t.Fatalf("credentials_encrypted is not encrypted enough for public account test: %q", created.CredentialsEncrypted)
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"sk-public-account-secret", "api.openai.com", "credentials", "baseurl", "apikey"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("response leaked %q in %s", forbidden, string(data))
		}
	}
}

func TestServiceAddUsesProviderDefaultSupportedModelsWhenOmitted(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})

	response, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "默认模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}
	if response.Account == nil {
		t.Fatal("response account is nil")
	}
	if got, want := response.Account.SupportedModels, defaultGPTSupportedModels; !slices.Equal(got, want) {
		t.Fatalf("supported models = %#v, want %#v", got, want)
	}
}

func TestServiceAddExplicitEmptySupportedModelsReturnsErrorWithoutCreatingAccount(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue([]string{}, true),
	})
	assertInvalidSupportedModelsRequired(t, err)
	if len(store.accounts) != 0 {
		t.Fatalf("created accounts = %d, want 0", len(store.accounts))
	}
}

func TestServiceAddEmptyProviderDefaultSupportedModelsReturnsError(t *testing.T) {
	store := newPublicAccountStoreFake()
	profileKey := "gpt|profile_gpt_openai_v1"
	profile := store.profiles[profileKey]
	profile.DefaultSupportedModels = nil
	store.profiles[profileKey] = profile
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})

	_, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "默认空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	assertInvalidSupportedModelsRequired(t, err)
	if len(store.accounts) != 0 {
		t.Fatalf("created accounts = %d, want 0", len(store.accounts))
	}
}

func TestServiceAddDuplicateNamePrecedesEmptySupportedModelsValidation(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{
		Store:  store,
		Now:    fixedPublicAccountNow,
		NewID:  sequentialPublicAccountID(),
		Secret: "public-account-test-secret",
	})
	input := AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "重复账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	}
	if _, err := service.Add(context.Background(), input); err != nil {
		t.Fatalf("seed public account: %v", err)
	}

	input.SupportedModels = NewStringListValue([]string{}, true)
	_, err := service.Add(context.Background(), input)
	if !errors.Is(err, ErrDuplicateAccountName) {
		t.Fatalf("duplicate add error = %v, want ErrDuplicateAccountName", err)
	}
}

func TestServiceUpdateRejectsPendingTestToActive(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{Store: store, Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID(), Secret: "public-account-test-secret"})
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "主账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	status := StatusActive
	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Status:    &status,
	})
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("update status error = %v, want ErrInvalidStatusTransition", err)
	}
}

func TestServiceUpdateOmittedSupportedModelsRejectsExistingEmptyModelsWithoutUpdatingStore(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{Store: store, Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID(), Secret: "public-account-test-secret"})
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "旧空模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	account := store.accounts[created.Account.ID]
	account.SupportedModels = nil
	store.accounts[account.ID] = account

	name := "仅修改名称"
	_, err = service.Update(context.Background(), UpdateInput{
		AccountID: account.ID,
		Name:      &name,
	})
	assertInvalidSupportedModelsRequired(t, err)
	if store.updateCalls != 0 {
		t.Fatalf("store update calls = %d, want 0", store.updateCalls)
	}
	stored := store.accounts[account.ID]
	if stored.Name != account.Name || len(stored.SupportedModels) != 0 {
		t.Fatalf("stored account = %+v, want unchanged empty-model account", stored)
	}
}

func TestServiceUpdateOmittedSupportedModelsPreservesExistingNonEmptyModels(t *testing.T) {
	store := newPublicAccountStoreFake()
	service := NewService(Options{Store: store, Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID(), Secret: "public-account-test-secret"})
	wantModels := []string{"gpt-5.5", "gpt-5.4-mini"}
	created, err := service.Add(context.Background(), AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "福利",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "正常模型账号",
		Type:                      AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-public-account-secret-0123456789abcdef",
		SupportedModels:           NewStringListValue(wantModels, true),
	})
	if err != nil {
		t.Fatalf("add public account: %v", err)
	}

	name := "正常改名"
	response, err := service.Update(context.Background(), UpdateInput{
		AccountID: created.Account.ID,
		Name:      &name,
	})
	if err != nil {
		t.Fatalf("update public account: %v", err)
	}
	if store.updateCalls != 1 {
		t.Fatalf("store update calls = %d, want 1", store.updateCalls)
	}
	if response.Account == nil || !slices.Equal(response.Account.SupportedModels, wantModels) {
		t.Fatalf("response account = %+v, want supported models %#v", response.Account, wantModels)
	}
	if got := store.accounts[created.Account.ID].SupportedModels; !slices.Equal(got, wantModels) {
		t.Fatalf("stored supported models = %#v, want %#v", got, wantModels)
	}
}

func assertInvalidSupportedModelsRequired(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidSupportedModels) {
		t.Fatalf("supported models error = %v, want ErrInvalidSupportedModels", err)
	}
	want := ErrInvalidSupportedModels.Error() + ": " + invalidSupportedModelsRequiredMessage
	if err.Error() != want {
		t.Fatalf("supported models error = %q, want %q", err.Error(), want)
	}
}

func TestServiceDeleteMissingIsNotFoundAction(t *testing.T) {
	service := NewService(Options{Store: newPublicAccountStoreFake(), Now: fixedPublicAccountNow, NewID: sequentialPublicAccountID()})
	username := "admin"
	response, err := service.Delete(context.Background(), DeleteInput{
		AccountID:      "acc_missing",
		TargetUsername: &username,
	})
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if response.Action != "not_found" || response.Account != nil || response.Target.Username != "admin" {
		t.Fatalf("delete missing response = %+v", response)
	}
}

func fixedPublicAccountNow() time.Time {
	return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
}

func sequentialPublicAccountID() func(string) string {
	seq := map[string]int{}
	return func(prefix string) string {
		seq[prefix]++
		return prefix + "_test_" + string(rune('0'+seq[prefix]))
	}
}

var defaultGPTSupportedModels = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-image-2",
}

type publicAccountStoreFake struct {
	targetsByUsername map[string]port.PublicGroupTarget
	targetsByID       map[string]port.PublicGroupTarget
	profiles          map[string]port.PublicAccountProviderProfile
	groups            map[string]port.PublicAccountGroupRef
	accounts          map[string]port.PublicAccountSummary
	updateCalls       int
}

func newPublicAccountStoreFake() *publicAccountStoreFake {
	return &publicAccountStoreFake{
		targetsByUsername: map[string]port.PublicGroupTarget{},
		targetsByID:       map[string]port.PublicGroupTarget{},
		profiles: map[string]port.PublicAccountProviderProfile{
			"gpt|profile_gpt_openai_v1": {
				ID:                     "profile_gpt_openai_v1",
				ProviderCode:           "gpt",
				Name:                   "GPT / OpenAI v1",
				Enabled:                true,
				ProviderEnabled:        true,
				ProtocolCode:           "openai",
				ProtocolVersion:        "v1",
				AccountTypesJSON:       `["oauth","api_key"]`,
				DefaultSupportedModels: append([]string(nil), defaultGPTSupportedModels...),
			},
		},
		groups:   map[string]port.PublicAccountGroupRef{},
		accounts: map[string]port.PublicAccountSummary{},
	}
}

func (s *publicAccountStoreFake) FindPublicAccountTargetByUsername(_ context.Context, username string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByUsername[strings.ToLower(strings.TrimSpace(username))]
	return target, ok, nil
}

func (s *publicAccountStoreFake) FindPublicAccountTargetByID(_ context.Context, id string) (port.PublicGroupTarget, bool, error) {
	target, ok := s.targetsByID[id]
	return target, ok, nil
}

func (s *publicAccountStoreFake) CreatePublicAccountTarget(_ context.Context, input port.PublicGroupTargetCreateInput) (port.PublicGroupTarget, error) {
	target := port.PublicGroupTarget{
		ID:          input.ID,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      "active",
		Created:     true,
	}
	s.targetsByUsername[strings.ToLower(input.Username)] = target
	s.targetsByID[input.ID] = target
	return target, nil
}

func (s *publicAccountStoreFake) FindPublicAccountProviderProfile(_ context.Context, providerCode string, profileID string) (port.PublicAccountProviderProfile, bool, error) {
	profile, ok := s.profiles[strings.TrimSpace(providerCode)+"|"+strings.TrimSpace(profileID)]
	return profile, ok, nil
}

func (s *publicAccountStoreFake) FindExistingPublicAccountGroupByName(_ context.Context, systemAccountID string, providerCode string, name string) (port.PublicAccountGroupRef, bool, error) {
	group, ok := s.groups[systemAccountID+"|"+providerCode+"|"+strings.ToLower(strings.TrimSpace(name))]
	return group, ok, nil
}

func (s *publicAccountStoreFake) CreatePublicAccountGroup(_ context.Context, input port.PublicGroupCreateInput) (port.PublicAccountGroupRef, error) {
	group := port.PublicAccountGroupRef{
		ID:              input.ID,
		SystemAccountID: input.SystemAccountID,
		Name:            input.Name,
		ProviderCode:    input.ProviderCode,
		Enabled:         input.Enabled,
		GroupType:       input.GroupType,
		Created:         true,
	}
	s.groups[input.SystemAccountID+"|"+input.ProviderCode+"|"+strings.ToLower(input.Name)] = group
	return group, nil
}

func (s *publicAccountStoreFake) FindPublicAccountGroupByID(_ context.Context, groupID string) (port.PublicAccountGroupRef, bool, error) {
	for _, group := range s.groups {
		if group.ID == groupID {
			return group, true, nil
		}
	}
	return port.PublicAccountGroupRef{}, false, nil
}

func (s *publicAccountStoreFake) ListPublicAccounts(_ context.Context, input port.PublicAccountListInput) (port.PublicAccountListPage, error) {
	items := []port.PublicAccountSummary{}
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID {
			items = append(items, account)
		}
	}
	return port.PublicAccountListPage{Items: items, Page: 1, PageSize: 50, PageUpperBound: len(items), HasMore: false}, nil
}

func (s *publicAccountStoreFake) FindPublicAccountByID(_ context.Context, accountID string) (port.PublicAccountSummary, bool, error) {
	account, ok := s.accounts[accountID]
	return account, ok, nil
}

func (s *publicAccountStoreFake) FindExistingPublicAccountByNameInGroup(_ context.Context, input port.PublicAccountNameLookupInput) (port.PublicAccountSummary, bool, error) {
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID &&
			account.ProviderCode == input.ProviderCode &&
			account.ProviderProtocolProfileID == input.ProviderProtocolProfileID &&
			account.BoundGroupID != nil && *account.BoundGroupID == input.GroupID &&
			strings.EqualFold(account.Name, input.Name) {
			return account, true, nil
		}
	}
	return port.PublicAccountSummary{}, false, nil
}

func (s *publicAccountStoreFake) CreatePublicAccount(_ context.Context, input port.PublicAccountCreateInput) (port.PublicAccountSummary, error) {
	for _, account := range s.accounts {
		if account.SystemAccountID == input.SystemAccountID && strings.EqualFold(account.Name, input.Name) {
			return port.PublicAccountSummary{}, port.ErrPublicAccountDuplicateName
		}
	}
	group, _, _ := s.FindPublicAccountGroupByID(context.Background(), input.GroupID)
	account := port.PublicAccountSummary{
		ID:                        input.ID,
		SystemAccountID:           input.SystemAccountID,
		Name:                      input.Name,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		ProtocolCode:              input.ProtocolCode,
		ProtocolVersion:           input.ProtocolVersion,
		Type:                      input.Type,
		Status:                    input.Status,
		CredentialsEncrypted:      input.CredentialsEncrypted,
		CredentialFingerprint:     input.CredentialFingerprint,
		CredentialMask:            input.CredentialMask,
		ClientCompatibility:       input.ClientCompatibility,
		SupportedModels:           input.SupportedModels,
		BoundGroupID:              &group.ID,
		BoundGroupName:            &group.Name,
		Schedulable:               input.Schedulable,
		AvailabilityScheduleJSON:  input.AvailabilityScheduleJSON,
		ConcurrencyLimit:          input.ConcurrencyLimit,
		Priority:                  input.Priority,
		Notes:                     input.Notes,
		CreatedAt:                 input.Now,
		UpdatedAt:                 input.Now,
	}
	s.accounts[input.ID] = account
	return account, nil
}

func (s *publicAccountStoreFake) UpdatePublicAccount(_ context.Context, input port.PublicAccountUpdateInput) (port.PublicAccountSummary, bool, error) {
	s.updateCalls++
	account, ok := s.accounts[input.ID]
	if !ok {
		return port.PublicAccountSummary{}, false, nil
	}
	account.Name = input.Name
	account.Status = input.Status
	account.CredentialsEncrypted = input.CredentialsEncrypted
	account.CredentialFingerprint = input.CredentialFingerprint
	account.CredentialMask = input.CredentialMask
	account.SupportedModels = input.SupportedModels
	account.Schedulable = input.Schedulable
	account.AvailabilityScheduleJSON = input.AvailabilityScheduleJSON
	account.ConcurrencyLimit = input.ConcurrencyLimit
	account.Priority = input.Priority
	account.Notes = input.Notes
	account.UpdatedAt = input.Now
	s.accounts[input.ID] = account
	return account, true, nil
}

func (s *publicAccountStoreFake) DeletePublicAccount(_ context.Context, accountID string, systemAccountID string, _ string, _ time.Time) (bool, error) {
	account, ok := s.accounts[accountID]
	if !ok || account.SystemAccountID != systemAccountID {
		return false, nil
	}
	delete(s.accounts, accountID)
	return true, nil
}
