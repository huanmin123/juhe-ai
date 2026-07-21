package managementaccountcreate

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type createStoreStub struct {
	input port.ManagementAccountCreateInput
}

func (s *createStoreStub) CreateManagementAccount(_ context.Context, input port.ManagementAccountCreateInput) (port.ManagementAccountCreateResult, error) {
	s.input = input
	return port.ManagementAccountCreateResult{Account: map[string]any{"id": input.ID, "systemAccountId": input.SystemAccountID, "name": input.Name, "status": input.Status}}, nil
}

type createCodecStub struct{}

func (createCodecStub) EncryptJSON(map[string]any) (string, error) { return "encrypted", nil }

func TestCreateEncryptsAndDefaultsAccount(t *testing.T) {
	store := &createStoreStub{}
	service := NewService(Options{Store: store, CredentialCodec: createCodecStub{}})
	result, err := service.Create(context.Background(), Input{ActorSystemAccountID: "actor", ActorRole: "admin", SystemAccountID: "owner", ProviderCode: "openai", ProviderProtocolProfileID: "profile", Name: "account", Type: "api_key", Credentials: map[string]any{"api_key": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if store.input.CredentialsEncrypted != "encrypted" || store.input.Status != "pending_test" || store.input.ConcurrencyLimit != 20 || store.input.HealthCheckEndpointMode != "chat_json" || result["status"] != "pending_test" {
		t.Fatalf("input=%+v result=%+v", store.input, result)
	}
}

func TestCreateRejectsConflictingDispatchFlags(t *testing.T) {
	service := NewService(Options{Store: &createStoreStub{}, CredentialCodec: createCodecStub{}})
	if _, err := service.Create(context.Background(), Input{ActorSystemAccountID: "actor", ActorRole: "admin", SystemAccountID: "owner", ProviderCode: "openai", ProviderProtocolProfileID: "profile", Name: "account", Type: "api_key", Credentials: map[string]any{"api_key": "secret"}, SuperPriorityEnabled: true, FallbackEnabled: true}); err != ErrInvalid {
		t.Fatalf("err=%v", err)
	}
}
