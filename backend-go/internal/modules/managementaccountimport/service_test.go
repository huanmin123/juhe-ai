package managementaccountimport

import (
	"context"
	"errors"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type importStoreStub struct {
	input port.ManagementAccountImportInput
}

func (s *importStoreStub) Import(_ context.Context, input port.ManagementAccountImportInput) (port.ManagementAccountImportResult, error) {
	s.input = input
	return port.ManagementAccountImportResult{Imported: len(input.Accounts), Summary: Summary{Accounts: ImportSummaryCounts{Create: len(input.Accounts)}}}, nil
}

type importCodecStub struct{}

func (importCodecStub) EncryptJSON(value map[string]any) (string, error) {
	if value["api_key"] != "secret" {
		return "", errors.New("unexpected credentials")
	}
	return "encrypted", nil
}

func TestPreviewValidatesProtocolAndLimits(t *testing.T) {
	service := NewService(Options{Store: &importStoreStub{}, CredentialCodec: importCodecStub{}})
	result, err := service.Preview(context.Background(), []byte(`{"type":"juhe-ai-account-import","version":1,"accounts":[{"name":"a","providerCode":"openai","providerProtocolProfileId":"profile","type":"api_key","status":"active","credentials":{"api_key":"secret"}}]}`), OptionsInput{})
	if err != nil || !result.CanImport || result.Mode != "preview" || result.Summary.Accounts.Total != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_, err = service.Preview(context.Background(), []byte(`{"type":"wrong","version":1,"accounts":[]}`), OptionsInput{})
	if err == nil {
		t.Fatal("expected protocol validation error")
	}
}

func TestConfirmEncryptsCredentialsAndKeepsScope(t *testing.T) {
	store := &importStoreStub{}
	service := NewService(Options{Store: store, CredentialCodec: importCodecStub{}})
	result, err := service.Confirm(context.Background(), []byte(`{"type":"juhe-ai-account-import","version":1,"accounts":[{"name":"a","providerCode":"openai","providerProtocolProfileId":"profile","type":"api_key","status":"active","credentials":{"api_key":"secret"}}]}`), OptionsInput{}, "owner-1")
	if err != nil || result.Mode != "import" || store.input.SystemAccountID != "owner-1" || store.input.Accounts[0].CredentialsEncrypted != "encrypted" {
		t.Fatalf("result=%+v input=%+v err=%v", result, store.input, err)
	}
}

func TestPreviewRejectsMoreThanFiftyAccounts(t *testing.T) {
	service := NewService(Options{Store: &importStoreStub{}, CredentialCodec: importCodecStub{}})
	data := `{"type":"juhe-ai-account-import","version":1,"accounts":[` + strings.Repeat(`{"name":"a","providerCode":"openai","providerProtocolProfileId":"profile","type":"api_key","status":"active","credentials":{}},`, 50) + `{"name":"a","providerCode":"openai","providerProtocolProfileId":"profile","type":"api_key","status":"active","credentials":{}}]}`
	if _, err := service.Preview(context.Background(), []byte(data), OptionsInput{}); err == nil {
		t.Fatal("expected account limit error")
	}
}
