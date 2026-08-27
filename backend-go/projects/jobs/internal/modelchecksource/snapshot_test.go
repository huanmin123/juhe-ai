package modelchecksource

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

func TestFreezeBuildsSeparatedDurableAndExecutionSnapshots(t *testing.T) {
	candidate := validCandidate()
	candidate.Credential = accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: "encrypted-api-key"}
	frozen, err := Freeze(Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if frozen.DurableAccount.MappedUpstreamModel != "gpt-5.6-sol" || frozen.Execution.Model != "gpt-5.6-sol" || frozen.Execution.Credential.Ciphertext != "encrypted-api-key" {
		t.Fatalf("frozen=%#v", frozen)
	}
	encoded, err := json.Marshal(frozen.DurableAccount)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "encrypted-api-key") || strings.Contains(string(encoded), "https://example.test") {
		t.Fatalf("durable snapshot leaked execution secret: %s", encoded)
	}
}

func TestFreezeUsesAllowedModelMapping(t *testing.T) {
	candidate := validCandidate()
	candidate.SupportedModels = []string{"gpt-5.6-terra"}
	candidate.ModelMappings = []ModelMapping{{Enabled: true, SourceModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-terra", SourceEndpointFamily: "responses", UpstreamEndpointFamily: "responses"}}
	frozen, err := Freeze(Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret")
	if err != nil || frozen.DurableAccount.MappedUpstreamModel != "gpt-5.6-terra" || frozen.Execution.Model != "gpt-5.6-terra" {
		t.Fatalf("frozen=%#v err=%v", frozen, err)
	}
}

func TestFreezeRejectsScopeProfileAndEligibilityDrift(t *testing.T) {
	candidate := validCandidate()
	if _, err := Freeze(Request{SystemAccountID: "other-system", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret"); err == nil {
		t.Fatal("cross-scope candidate accepted")
	}
	candidate = validCandidate()
	candidate.EndpointMode = "chat_json"
	if _, err := Freeze(Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret"); err == nil {
		t.Fatal("profile/mode mismatch accepted")
	}
	candidate = validCandidate()
	candidate.Eligible = false
	if _, err := Freeze(Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret"); err == nil {
		t.Fatal("ineligible candidate accepted")
	}
}

func TestFreezeRejectsGatewayOnlyCrossEndpointMapping(t *testing.T) {
	candidate := validCandidate()
	candidate.SupportedModels = []string{"gpt-5.6-terra"}
	candidate.ModelMappings = []ModelMapping{{
		Enabled: true, SourceModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-terra", SourceEndpointFamily: "responses", UpstreamEndpointFamily: "chat_completions",
	}}
	if _, err := Freeze(Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}, candidate, "source-test-secret"); err == nil || !strings.Contains(err.Error(), "conversion") {
		t.Fatalf("err=%v", err)
	}
}

func validCandidate() Candidate {
	return Candidate{AccountID: "account-1", SystemAccountID: "system-1", ConfigRevision: "7", ProviderCode: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolRevision: "profile-r7", Status: "active", Eligible: true, EndpointMode: "responses_json", Endpoint: "https://example.test", CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: "encrypted"}, ProxyVersion: "direct"}
}
