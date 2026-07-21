package managementaccountexport

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestWriteUsesCursorBatchesAndWritesImportV1Document(t *testing.T) {
	reader := &exportReaderStub{pages: []port.ManagementAccountExportPage{
		{
			Items: []port.ManagementAccountExportAccount{
				exportAccountFixture("account-1", "proxy-1"),
				exportAccountFixture("account-2", "proxy-1"),
			},
			Matched: 3, HasMore: true, NextID: "account-2",
		},
		{Items: []port.ManagementAccountExportAccount{exportAccountFixture("account-3", "")}, Matched: 3, NextID: "account-3"},
	}}
	service := NewService(ServiceOptions{Reader: reader, CredentialCodec: exportCodecStub{}})
	var output bytes.Buffer

	summary, err := service.Write(context.Background(), &output, Input{
		SystemAccountID: "owner-1",
		Filters:         &Filters{Keyword: " demo "},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(reader.afterIDs) != 2 || reader.afterIDs[0] != "" || reader.afterIDs[1] != "account-2" {
		t.Fatalf("cursor calls = %#v", reader.afterIDs)
	}
	if summary.Accounts != 3 || summary.Proxies != 1 || summary.MatchedAccounts != 3 || summary.SkippedAccounts != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	var envelope struct {
		Data Result `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode output: %v; body=%s", err, output.String())
	}
	result := envelope.Data
	if result.Document.Type != ProtocolType || result.Document.Version != ProtocolVersion || len(result.Document.Accounts) != 3 {
		t.Fatalf("document = %+v", result.Document)
	}
	if len(result.Document.Proxies) != 1 || result.Document.Accounts[0].Credentials["api_key"] != "secret-account-1" {
		t.Fatalf("document fields = %+v", result.Document)
	}
	if _, exists := result.Document.Accounts[0].Credentials["internal"]; exists {
		t.Fatalf("unexpected credential field: %+v", result.Document.Accounts[0].Credentials)
	}
}

func TestWriteNormalizesIDsAndRejectsAmbiguousRequest(t *testing.T) {
	reader := &exportReaderStub{pages: []port.ManagementAccountExportPage{{
		Items:   []port.ManagementAccountExportAccount{exportAccountFixture("account-1", "")},
		Matched: 1,
	}}}
	service := NewService(ServiceOptions{Reader: reader, CredentialCodec: exportCodecStub{}})
	var output bytes.Buffer
	_, err := service.Write(context.Background(), &output, Input{AccountIDs: []string{" account-1 ", "account-1"}})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := reader.inputs[0].AccountIDs; len(got) != 1 || got[0] != "account-1" {
		t.Fatalf("normalized IDs = %#v", got)
	}
	if _, err := service.Write(context.Background(), &bytes.Buffer{}, Input{AccountIDs: []string{"account-1"}, Filters: &Filters{}}); err == nil {
		t.Fatal("Write() ambiguous request error = nil")
	}
}

func exportAccountFixture(id, proxyID string) port.ManagementAccountExportAccount {
	return port.ManagementAccountExportAccount{
		ID: id, Name: id, ProviderCode: "openai", ProviderProtocolProfileID: "profile-openai",
		ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", Status: "active",
		CredentialsEncrypted: "credentials:" + id, ProxyProfileID: proxyID, ProxyName: "proxy",
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080, ProxyEnabled: proxyID != "",
		ProxyPasswordEncrypted: "proxy-password", ConcurrencyLimit: 20, HealthCheckEndpointMode: "responses_json",
		SupportedModelsJSON: `["gpt-5"]`, ModelMappingsJSON: `[]`, TagsJSON: `["prod"]`,
	}
}

type exportReaderStub struct {
	pages    []port.ManagementAccountExportPage
	inputs   []port.ManagementAccountExportInput
	afterIDs []string
}

func (s *exportReaderStub) ListManagementAccountExportBatch(_ context.Context, input port.ManagementAccountExportInput, afterID string, _ int) (port.ManagementAccountExportPage, error) {
	s.inputs = append(s.inputs, input)
	s.afterIDs = append(s.afterIDs, afterID)
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}

type exportCodecStub struct{}

func (exportCodecStub) DecryptJSON(value string) (map[string]any, error) {
	if value == "proxy-password" {
		return map[string]any{"password": "proxy-secret"}, nil
	}
	return map[string]any{"api_key": "secret-" + value[len("credentials:"):], "internal": "omit"}, nil
}
