package managementaccountdraft

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestPrepareBuildsScopedReadOnlySnapshot(t *testing.T) {
	groups := &groupReaderStub{rows: []port.ManagementGroupOption{{
		ID: "group_1", SystemAccountID: "owner_1", OwnerSystemAccountID: "owner_1", Name: "Default", ProviderCode: "openai", Enabled: true,
	}}}
	providers := &providerReaderStub{rows: validProviders()}
	service := NewService(Options{Groups: groups, Providers: providers, NewID: func(string) string { return "acctdraft_1" }})

	got, err := service.Prepare(context.Background(), Input{
		Access:  port.ManagementAccountTestAccess{ActorSystemAccountID: "admin_1", ActorRole: "admin", FilterSystemAccountID: "owner_1"},
		Account: validDraftAccount(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "acctdraft_1" || got.OwnerSystemAccountID != "owner_1" || got.GroupName != "Default" || got.ProtocolCode != "openai" || got.ProtocolVersion != "v1" {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.ConcurrencyLimit != 20 || got.ClientCompatibility != "openai_standard" || len(got.SupportedModels) != 1 || got.SupportedModels[0] != "gpt-5.5" {
		t.Fatalf("defaults = %+v", got)
	}
	if groups.input.SystemAccountID != "owner_1" || !groups.input.ManageableOnly || len(groups.input.IDs) != 1 || groups.input.IDs[0] != "group_1" {
		t.Fatalf("group input = %+v", groups.input)
	}
	if providers.input.SystemAccountID != "owner_1" {
		t.Fatalf("provider input = %+v", providers.input)
	}
}

func TestPrepareRejectsInaccessibleGroupBeforeProviderLookup(t *testing.T) {
	providers := &providerReaderStub{rows: validProviders()}
	service := NewService(Options{Groups: &groupReaderStub{}, Providers: providers})
	_, err := service.Prepare(context.Background(), Input{
		Access:  port.ManagementAccountTestAccess{ActorSystemAccountID: "user_1", ActorRole: "user", FilterSystemAccountID: "user_1"},
		Account: validDraftAccount(),
	})
	if !errors.Is(err, ErrGroupInvalid) || providers.called {
		t.Fatalf("err=%v providerCalled=%v", err, providers.called)
	}
}

func TestPrepareRejectsDisabledProfileAndUnsupportedEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		providers []port.ManagementProviderOption
		mutate    func(*Account)
		want      error
	}{
		{
			name: "disabled profile",
			providers: []port.ManagementProviderOption{{Code: "openai", Enabled: true, ProtocolProfiles: []port.ManagementProviderProtocolProfile{{
				ID: "profile_openai", ProviderCode: "openai", Enabled: false, ProtocolCode: "openai", ProtocolVersion: "v1", AccountTypes: []string{"api_key"},
			}}}},
			want: ErrProviderInvalid,
		},
		{
			name:      "endpoint outside protocol",
			providers: validProviders(),
			mutate:    func(account *Account) { account.HealthCheckEndpointMode = "messages_sse" },
			want:      ErrInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := validDraftAccount()
			if tt.mutate != nil {
				tt.mutate(&account)
			}
			service := NewService(Options{
				Groups:    &groupReaderStub{rows: []port.ManagementGroupOption{{ID: "group_1", SystemAccountID: "owner_1", OwnerSystemAccountID: "owner_1", ProviderCode: "openai"}}},
				Providers: &providerReaderStub{rows: tt.providers},
			})
			if _, err := service.Prepare(context.Background(), Input{Access: port.ManagementAccountTestAccess{ActorSystemAccountID: "owner_1", FilterSystemAccountID: "owner_1"}, Account: account}); !errors.Is(err, tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateBalanceDraftRequiresSingleAPIKeyAndStrictConfig(t *testing.T) {
	snapshot := Snapshot{Type: "api_key", Credentials: map[string]any{"api_keys": []any{"sk-a", "sk-b"}}}
	if err := ValidateBalanceDraft(snapshot, BalanceQueryConfig{Adapter: "builtin"}); !errors.Is(err, ErrBalanceUnsupported) {
		t.Fatalf("multi-key err=%v", err)
	}
	snapshot.Credentials = map[string]any{"api_key": "sk-a"}
	if err := ValidateBalanceDraft(snapshot, BalanceQueryConfig{Adapter: "custom"}); !errors.Is(err, ErrBalanceConfigInvalid) {
		t.Fatalf("custom config err=%v", err)
	}
	if err := ValidateBalanceDraft(snapshot, BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5, PreferredBuiltinAdapter: "sub2api"}); err != nil {
		t.Fatalf("valid config err=%v", err)
	}
}

func validDraftAccount() Account {
	return Account{
		ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai", Name: "Draft", Type: "api_key",
		Credentials:      map[string]any{"api_key": "sk-draft", "supported_endpoint_modes": []any{"responses_sse"}},
		HealthCheckModel: "gpt-5.5", HealthCheckEndpointMode: "responses_sse", GroupID: "group_1",
	}
}

func validProviders() []port.ManagementProviderOption {
	return []port.ManagementProviderOption{{
		Code: "openai", Enabled: true, DefaultSupportedModels: []string{"gpt-5.5"},
		ProtocolProfiles: []port.ManagementProviderProtocolProfile{{
			ID: "profile_openai", ProviderCode: "openai", Enabled: true, ProtocolCode: "openai", ProtocolVersion: "v1",
			BaseURL: "https://api.openai.com/v1", AccountTypes: []string{"api_key", "oauth"},
		}},
	}}
}

type groupReaderStub struct {
	input port.ManagementGroupOptionListInput
	rows  []port.ManagementGroupOption
}

func (s *groupReaderStub) ListManagementGroupOptions(_ context.Context, input port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error) {
	s.input = input
	return s.rows, nil
}

func (s *groupReaderStub) ListManagementGroupAccountOptions(context.Context, port.ManagementGroupOptionListInput) ([]port.ManagementGroupAccountOption, error) {
	return nil, nil
}

type providerReaderStub struct {
	input  port.ManagementProviderOptionListInput
	rows   []port.ManagementProviderOption
	called bool
}

func (s *providerReaderStub) ListManagementProviderOptions(_ context.Context, input port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error) {
	s.called = true
	s.input = input
	return s.rows, nil
}
