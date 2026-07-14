package managementaccounttestoptions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceGetGPTOAuthOptionsAndExactJSON(t *testing.T) {
	reader := &accountTestOptionsReaderStub{
		found:  true,
		source: baseAccountTestOptionsSource(),
	}
	reader.source.ID = "account-oauth"
	reader.source.OwnerSystemAccountID = "owner-1"
	reader.source.ProviderCode = "gpt"
	reader.source.ProtocolCode = "openai"
	reader.source.ProtocolVersion = "v1"
	reader.source.Type = "oauth"
	reader.source.HealthCheckModel = " \t gpt-5.1-codex \r\n"
	reader.source.HealthCheckEndpointMode = "responses_sse"
	reader.source.CredentialsEncrypted = "encrypted-oauth"
	catalog := &modelCatalogStub{items: []managementprovidermodels.ModelCatalogItem{
		{
			Model:                 "gpt-5.1-codex",
			Status:                "active",
			Mode:                  "chat",
			SupportedAPIProtocols: []string{"responses"},
		},
	}}
	codec := &credentialCodecStub{credentials: map[string]any{}}
	service := NewServiceWithOptions(ServiceOptions{Reader: reader, ModelCatalog: catalog, CredentialCodec: codec})

	got, found, err := service.Get(context.Background(), Input{
		AccountID:       " account-oauth ",
		SystemAccountID: " viewer-1 ",
	})
	if err != nil || !found {
		t.Fatalf("Get() found = %t, err = %v", found, err)
	}
	if !reflect.DeepEqual(reader.input, port.ManagementAccountTestOptionsInput{
		AccountID:       "account-oauth",
		SystemAccountID: "viewer-1",
	}) {
		t.Fatalf("reader input = %+v", reader.input)
	}
	if !reflect.DeepEqual(catalog.input, managementprovidermodels.ModelListInput{
		ProviderCode:    "gpt",
		SystemAccountID: "owner-1",
		IncludeInactive: false,
		IncludeUnpriced: true,
	}) {
		t.Fatalf("catalog input = %+v", catalog.input)
	}
	if codec.encrypted != "encrypted-oauth" {
		t.Fatalf("codec encrypted input = %q", codec.encrypted)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	wantJSON := `{"accountId":"account-oauth","defaultModel":"gpt-5.1-codex","models":[{"model":"gpt-5.1-codex","supportedApiProtocols":["responses"]}],"testEndpointModes":["responses_sse","responses_json"],"defaultTestEndpointMode":"responses_sse"}`
	if string(encoded) != wantJSON {
		t.Fatalf("result JSON = %s, want %s", encoded, wantJSON)
	}
}

func TestServiceGetAnthropicIncludesSSEAndJSONButExcludesTools(t *testing.T) {
	source := baseAccountTestOptionsSource()
	source.ProviderCode = "anthropic"
	source.ProtocolCode = " Anthropic "
	source.ProtocolVersion = " V1 "
	source.ProviderProtocolProfileID = "profile_anthropic_anthropic_v1"
	source.HealthCheckModel = "claude-sonnet-4"
	source.HealthCheckEndpointMode = "messages_sse"
	service := serviceForOptions(source, map[string]any{
		"supported_endpoint_modes": []any{"message_token_counting", "messages_json", "messages_sse"},
	}, []managementprovidermodels.ModelCatalogItem{
		activeCatalogModel("claude-sonnet-4", "messages"),
	})

	got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
	if err != nil || !found {
		t.Fatalf("Get() found = %t, err = %v", found, err)
	}
	want := []string{"messages_sse", "messages_json"}
	if !reflect.DeepEqual(got.TestEndpointModes, want) {
		t.Fatalf("test endpoint modes = %#v, want %#v", got.TestEndpointModes, want)
	}
	if got.DefaultTestEndpointMode != "messages_sse" {
		t.Fatalf("default endpoint mode = %q", got.DefaultTestEndpointMode)
	}
}

func TestServiceGetGeminiRuntimeFallbackIncludesJSONAndStreaming(t *testing.T) {
	source := baseAccountTestOptionsSource()
	source.ProviderCode = "gemini"
	source.ProtocolCode = " Gemini "
	source.ProtocolVersion = " V1BETA "
	source.ProviderProtocolProfileID = "profile_gemini_native_v1beta"
	source.HealthCheckModel = "gemini-2.5-pro"
	source.HealthCheckEndpointMode = "generate_content_sse"
	service := serviceForOptions(source, map[string]any{
		"supported_endpoint_modes": "invalid-runtime-shape",
	}, []managementprovidermodels.ModelCatalogItem{
		activeCatalogModel("gemini-2.5-pro", "stream_generate_content"),
	})

	got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
	if err != nil || !found {
		t.Fatalf("Get() found = %t, err = %v", found, err)
	}
	want := []string{"generate_content_sse", "generate_content_json"}
	if !reflect.DeepEqual(got.TestEndpointModes, want) {
		t.Fatalf("test endpoint modes = %#v, want %#v", got.TestEndpointModes, want)
	}
}

func TestServiceGetHybridTakesPriorityAndExcludesToolModes(t *testing.T) {
	source := baseAccountTestOptionsSource()
	source.ProviderCode = " HYBRID "
	source.ProtocolCode = "openai"
	source.ProtocolVersion = "v1"
	source.HealthCheckModel = "hybrid-claude"
	source.HealthCheckEndpointMode = "messages_sse"
	service := serviceForOptions(source, map[string]any{
		"supported_endpoint_modes": []any{
			"message_token_counting",
			"count_tokens",
			"embed_content",
			"chat_json",
			"messages_sse",
		},
	}, []managementprovidermodels.ModelCatalogItem{
		activeCatalogModel("hybrid-claude", "messages"),
	})

	got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
	if err != nil || !found {
		t.Fatalf("Get() found = %t, err = %v", found, err)
	}
	want := []string{"messages_sse", "chat_json"}
	if !reflect.DeepEqual(got.TestEndpointModes, want) {
		t.Fatalf("test endpoint modes = %#v, want %#v", got.TestEndpointModes, want)
	}
}

func TestServiceGetRuntimeNormalizerFallsBackToDefaults(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{name: "missing", credentials: map[string]any{}},
		{name: "not array", credentials: map[string]any{"supported_endpoint_modes": 7}},
		{name: "empty array", credentials: map[string]any{"supported_endpoint_modes": []any{}}},
		{name: "all invalid", credentials: map[string]any{"supported_endpoint_modes": []any{"messages_json", nil, 3}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := baseAccountTestOptionsSource()
			source.ProviderCode = "gpt"
			source.ProtocolCode = "openai"
			source.ProtocolVersion = "v1"
			source.Type = "api_key"
			source.HealthCheckModel = "gpt-model"
			source.HealthCheckEndpointMode = "chat_json"
			service := serviceForOptions(source, tt.credentials, []managementprovidermodels.ModelCatalogItem{
				activeCatalogModel("gpt-model", "chat_completions"),
			})

			got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
			if err != nil || !found {
				t.Fatalf("Get() found = %t, err = %v", found, err)
			}
			want := []string{"chat_json", "chat_sse", "responses_sse", "responses_json"}
			if !reflect.DeepEqual(got.TestEndpointModes, want) {
				t.Fatalf("test endpoint modes = %#v, want %#v", got.TestEndpointModes, want)
			}
		})
	}
}

func TestServiceGetDoesNotTrimEndpointModeValues(t *testing.T) {
	tests := []struct {
		name            string
		healthCheckMode string
		credentialModes []any
		want            []string
	}{
		{
			name:            "health check mode stays exact",
			healthCheckMode: " responses_sse ",
			credentialModes: []any{"responses_json", "responses_sse"},
			want:            []string{"responses_json", "responses_sse"},
		},
		{
			name:            "credential mode stays exact",
			healthCheckMode: "responses_json",
			credentialModes: []any{" responses_sse ", "responses_json"},
			want:            []string{"responses_json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := baseAccountTestOptionsSource()
			source.ProviderCode = "gpt"
			source.Type = "oauth"
			source.HealthCheckEndpointMode = tt.healthCheckMode
			service := serviceForOptions(source, map[string]any{
				"supported_endpoint_modes": tt.credentialModes,
			}, []managementprovidermodels.ModelCatalogItem{
				activeCatalogModel("model-default", "responses"),
			})

			got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
			if err != nil || !found {
				t.Fatalf("Get() found = %t, err = %v", found, err)
			}
			if !reflect.DeepEqual(got.TestEndpointModes, tt.want) {
				t.Fatalf("test endpoint modes = %#v, want %#v", got.TestEndpointModes, tt.want)
			}
		})
	}
}

func TestServiceGetFiltersModelsWithoutIntersectingSupportedModelsAndPreservesOrder(t *testing.T) {
	source := baseAccountTestOptionsSource()
	source.ProviderCode = "anthropic"
	source.ProtocolCode = "anthropic"
	source.ProtocolVersion = "v1"
	source.HealthCheckModel = "model-no-protocols"
	source.HealthCheckEndpointMode = "messages_json"
	catalog := []managementprovidermodels.ModelCatalogItem{
		{Model: "model-no-protocols", Status: "active", Mode: "chat", SupportedAPIProtocols: nil},
		{Model: "inactive", Status: "inactive", Mode: "chat", SupportedAPIProtocols: []string{"messages"}},
		{Model: "image", Status: "active", Mode: "image", SupportedAPIProtocols: nil},
		{Model: "audio", Status: "active", Mode: "audio", SupportedAPIProtocols: []string{"messages"}},
		{Model: "openai-only", Status: "active", Mode: "chat", SupportedAPIProtocols: []string{"responses"}},
		{Model: "model-messages", Status: "active", Mode: "chat", SupportedAPIProtocols: []string{"messages"}},
		{Model: "model-mixed", Status: "active", Mode: "chat", SupportedAPIProtocols: []string{"responses", "messages"}},
	}
	service := serviceForOptions(source, map[string]any{
		"supported_models":         []any{"some-other-model"},
		"supported_endpoint_modes": []any{"messages_json"},
	}, catalog)

	got, found, err := service.Get(context.Background(), Input{AccountID: source.ID, SystemAccountID: "viewer"})
	if err != nil || !found {
		t.Fatalf("Get() found = %t, err = %v", found, err)
	}
	want := []ModelOption{
		{Model: "model-no-protocols", SupportedAPIProtocols: []string{}},
		{Model: "model-messages", SupportedAPIProtocols: []string{"messages"}},
		{Model: "model-mixed", SupportedAPIProtocols: []string{"responses", "messages"}},
	}
	if !reflect.DeepEqual(got.Models, want) {
		t.Fatalf("models = %#v, want %#v", got.Models, want)
	}
	encoded, err := json.Marshal(got.Models[0])
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}
	if string(encoded) != `{"model":"model-no-protocols","supportedApiProtocols":[]}` {
		t.Fatalf("empty protocols JSON = %s", encoded)
	}
}

func TestServiceGetPreservesInfrastructureErrors(t *testing.T) {
	readerFailure := errors.New("reader failed")
	decryptFailure := errors.New("invalid ciphertext")
	catalogFailure := errors.New("catalog failed")

	tests := []struct {
		name    string
		service *Service
		wantErr error
	}{
		{
			name: "reader",
			service: NewServiceWithOptions(ServiceOptions{
				Reader:          &accountTestOptionsReaderStub{err: readerFailure},
				ModelCatalog:    &modelCatalogStub{},
				CredentialCodec: &credentialCodecStub{},
			}),
			wantErr: readerFailure,
		},
		{
			name: "invalid ciphertext",
			service: NewServiceWithOptions(ServiceOptions{
				Reader:          &accountTestOptionsReaderStub{source: baseAccountTestOptionsSource(), found: true},
				ModelCatalog:    &modelCatalogStub{},
				CredentialCodec: &credentialCodecStub{err: decryptFailure},
			}),
			wantErr: decryptFailure,
		},
		{
			name: "catalog",
			service: NewServiceWithOptions(ServiceOptions{
				Reader:          &accountTestOptionsReaderStub{source: baseAccountTestOptionsSource(), found: true},
				ModelCatalog:    &modelCatalogStub{err: catalogFailure},
				CredentialCodec: &credentialCodecStub{credentials: map[string]any{}},
			}),
			wantErr: catalogFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tt.service.Get(context.Background(), Input{AccountID: "account-1", SystemAccountID: "viewer"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Get() error = %v, want %v", err, tt.wantErr)
			}
			if _, ok := ValidationMessage(err); ok {
				t.Fatalf("infrastructure error was converted to ValidationError: %v", err)
			}
		})
	}
}

func TestServiceGetNotFound(t *testing.T) {
	reader := &accountTestOptionsReaderStub{}
	catalog := &modelCatalogStub{}
	codec := &credentialCodecStub{}
	service := NewServiceWithOptions(ServiceOptions{Reader: reader, ModelCatalog: catalog, CredentialCodec: codec})

	got, found, err := service.Get(context.Background(), Input{AccountID: "missing", SystemAccountID: "viewer"})
	if err != nil || found {
		t.Fatalf("Get() result = %+v, found = %t, err = %v", got, found, err)
	}
	if catalog.calls != 0 || codec.calls != 0 {
		t.Fatalf("not-found dependencies called: catalog = %d, codec = %d", catalog.calls, codec.calls)
	}
}

func TestServiceGetReturnsValidationErrorsForBusinessFailures(t *testing.T) {
	tests := []struct {
		name        string
		source      port.ManagementAccountTestOptionsSource
		credentials map[string]any
		catalog     []managementprovidermodels.ModelCatalogItem
		wantMessage string
	}{
		{
			name: "health check model unavailable",
			source: func() port.ManagementAccountTestOptionsSource {
				source := baseAccountTestOptionsSource()
				source.HealthCheckModel = "  model-default\t"
				return source
			}(),
			credentials: map[string]any{"supported_endpoint_modes": []any{"chat_json"}},
			catalog:     []managementprovidermodels.ModelCatalogItem{activeCatalogModel("another-model", "chat_completions")},
			wantMessage: "账户检查模型已不在当前供应商可用目录中，请先修正账户检查模型：model-default",
		},
		{
			name: "no generation endpoint mode",
			source: func() port.ManagementAccountTestOptionsSource {
				source := baseAccountTestOptionsSource()
				source.ProviderCode = "anthropic"
				source.ProtocolCode = "anthropic"
				source.ProtocolVersion = "v1"
				source.HealthCheckEndpointMode = "messages_json"
				return source
			}(),
			credentials: map[string]any{"supported_endpoint_modes": []any{"message_token_counting"}},
			catalog:     []managementprovidermodels.ModelCatalogItem{activeCatalogModel("model-default", "messages")},
			wantMessage: "账户上游接口能力中没有可用于连接测试的请求形态",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := serviceForOptions(tt.source, tt.credentials, tt.catalog)
			_, _, err := service.Get(context.Background(), Input{AccountID: tt.source.ID, SystemAccountID: "viewer"})
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Get() error = %T %v, want ValidationError", err, err)
			}
			if validationErr.Message != tt.wantMessage {
				t.Fatalf("validation message = %q, want %q", validationErr.Message, tt.wantMessage)
			}
			if message, ok := ValidationMessage(err); !ok || message != tt.wantMessage {
				t.Fatalf("ValidationMessage() = %q, %t", message, ok)
			}
		})
	}
}

func baseAccountTestOptionsSource() port.ManagementAccountTestOptionsSource {
	return port.ManagementAccountTestOptionsSource{
		ID:                        "account-1",
		OwnerSystemAccountID:      "owner-1",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Type:                      "api_key",
		ClientCompatibility:       "openai",
		HealthCheckModel:          "model-default",
		HealthCheckEndpointMode:   "chat_json",
		CredentialsEncrypted:      "encrypted-credentials",
	}
}

func activeCatalogModel(model string, protocols ...string) managementprovidermodels.ModelCatalogItem {
	return managementprovidermodels.ModelCatalogItem{
		Model:                 model,
		Status:                "active",
		Mode:                  "chat",
		SupportedAPIProtocols: append([]string{}, protocols...),
	}
}

func serviceForOptions(
	source port.ManagementAccountTestOptionsSource,
	credentials map[string]any,
	items []managementprovidermodels.ModelCatalogItem,
) *Service {
	return NewServiceWithOptions(ServiceOptions{
		Reader:          &accountTestOptionsReaderStub{source: source, found: true},
		ModelCatalog:    &modelCatalogStub{items: items},
		CredentialCodec: &credentialCodecStub{credentials: credentials},
	})
}

type accountTestOptionsReaderStub struct {
	source port.ManagementAccountTestOptionsSource
	found  bool
	err    error
	input  port.ManagementAccountTestOptionsInput
	calls  int
}

func (s *accountTestOptionsReaderStub) GetManagementAccountTestOptionsSource(
	_ context.Context,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	s.calls++
	s.input = input
	return s.source, s.found, s.err
}

type modelCatalogStub struct {
	items []managementprovidermodels.ModelCatalogItem
	err   error
	input managementprovidermodels.ModelListInput
	calls int
}

func (s *modelCatalogStub) Models(
	_ context.Context,
	input managementprovidermodels.ModelListInput,
) ([]managementprovidermodels.ModelCatalogItem, error) {
	s.calls++
	s.input = input
	return s.items, s.err
}

type credentialCodecStub struct {
	credentials map[string]any
	err         error
	encrypted   string
	calls       int
}

func (s *credentialCodecStub) DecryptJSON(value string) (map[string]any, error) {
	s.calls++
	s.encrypted = value
	return s.credentials, s.err
}
