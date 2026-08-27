package modelcheckresolver

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestResolverBuildsCredentialFreeTargetWithInMemoryHeaders(t *testing.T) {
	const secret = "resolver-test-secret"
	credential, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_key":"sk-resolver-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New([]Snapshot{{
		AccountID: "account-1", ConfigRevision: "7", Provider: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "profile-revision-7",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: credential}, Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "gpt-5.6-sol", Prompt: "Reply with exactly: OK-MODEL-CHECK", Endpoint: "https://example.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	account := snapshotFor("account-1", "7", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol")
	target, err := resolver.Resolve(context.Background(), resolutionRequest(account))
	if err != nil {
		t.Fatal(err)
	}
	if target.Protocol != modelcheckprofile.ProtocolOpenAIResponses || target.Endpoint != "https://example.test" || target.Headers.Get("Authorization") != "Bearer sk-resolver-secret" || target.Model != "gpt-5.6-sol" || target.Client == nil {
		t.Fatalf("target=%#v headers=%#v", target, target.Headers)
	}
	if target.ProtocolProfileRevision != "profile-revision-7" {
		t.Fatalf("unexpected profile revision %q", target.ProtocolProfileRevision)
	}
	account.ConfigRevision = "8"
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(account)); err == nil {
		t.Fatal("stale immutable snapshot was accepted")
	}
}

func TestResolverUsesProtocolSpecificOAuthHeaders(t *testing.T) {
	const secret = "resolver-oauth-secret"
	credential, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"access_token":"oauth-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New([]Snapshot{{
		AccountID: "gemini-account", ConfigRevision: "1", Provider: "gemini", ProtocolProfileID: "profile_gemini_native_v1beta", ProtocolProfileRevision: "v1beta",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct",
		CredentialType: "google_oauth", Credential: accounthealth.CredentialEnvelope{Kind: "oauth_access", Ciphertext: credential}, Protocol: modelcheckprofile.ProtocolGeminiNative,
		Model: "gemini-3.5-flash", Prompt: "Reply with exactly: OK-MODEL-CHECK", Endpoint: "https://example.test", OAuthQuotaProjectID: "quota-project",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(context.Background(), resolutionRequest(snapshotFor("gemini-account", "1", "profile_gemini_native_v1beta", "v1beta", "gemini-3.5-flash")))
	if err != nil {
		t.Fatal(err)
	}
	if target.Headers.Get("Authorization") != "Bearer oauth-token" || target.Headers.Get("x-goog-user-project") != "quota-project" || target.Headers.Get("x-goog-api-key") != "" {
		t.Fatalf("unexpected Gemini headers: %#v", target.Headers)
	}
}

func TestResolverUsesFirstConfiguredAPIKeyWhenCredentialStoresAPIPool(t *testing.T) {
	const secret = "resolver-api-key-pool-secret"
	credential, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["","first-key","second-key"]}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New([]Snapshot{{
		AccountID: "account-pool", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "v1",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: credential}, Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "gpt-5.6-sol", Prompt: "Reply with exactly: OK-MODEL-CHECK", Endpoint: "https://example.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(context.Background(), resolutionRequest(snapshotFor("account-pool", "1", "profile_openai_openai_v1", "v1", "gpt-5.6-sol")))
	if err != nil {
		t.Fatal(err)
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer first-key" {
		t.Fatalf("Authorization=%q", got)
	}
}

func TestResolverClientUsesExplicitProxyWithoutEnvironmentFallback(t *testing.T) {
	const secret = "resolver-proxy-secret"
	credential, _ := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_key":"sk"}`))
	proxy, _ := accounthealth.EncryptV1Envelope(secret, []byte(`{"url":"http://127.0.0.1:1"}`))
	resolver, err := New([]Snapshot{{
		AccountID: "account-1", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "v1",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "proxy-v1",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: credential}, Proxy: &accounthealth.CredentialEnvelope{Ciphertext: proxy}, Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model: "gpt-5.6-sol", Prompt: "Reply with exactly: OK-MODEL-CHECK", Endpoint: "https://example.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	account := snapshotFor("account-1", "1", "profile_openai_openai_v1", "v1", "gpt-5.6-sol")
	account.ProxyConfigurationVersion = "proxy-v1"
	target, err := resolver.Resolve(context.Background(), resolutionRequest(account))
	if err != nil || target.Client == nil {
		t.Fatalf("target=%#v err=%v", target, err)
	}
}

func TestResolverRejectsIncompleteSnapshots(t *testing.T) {
	if _, err := New([]Snapshot{{AccountID: "account-1"}}, "resolver-test-secret"); err == nil {
		t.Fatal("incomplete snapshot was accepted")
	}
}

func snapshotFor(id, revision, profile, profileRevision, model string) modelcheckinput.AccountSnapshot {
	return modelcheckinput.AccountSnapshot{ID: id, ConfigRevision: revision, ProviderCode: "openai", ProtocolProfileID: profile, ProtocolProfileRevision: profileRevision, EndpointFingerprint: "endpoint-fp", MappedUpstreamModel: model, CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct"}
}

func resolutionRequest(account modelcheckinput.AccountSnapshot) modelcheckexecutor.ResolutionRequest {
	return modelcheckexecutor.ResolutionRequest{Input: modelcheckinput.IssuedInput{SystemAccountID: "system-account", Model: account.MappedUpstreamModel}, Account: account}
}
