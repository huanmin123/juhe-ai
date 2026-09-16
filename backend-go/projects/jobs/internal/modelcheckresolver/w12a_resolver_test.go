package modelcheckresolver

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// w12a_resolver_test.go 覆盖 nil resolver、账户缺失、凭据信封错误与
// proxy 信封错误分支。

func TestW12aNilResolverAndMissingAccount(t *testing.T) {
	var resolver *Resolver
	if _, err := resolver.Resolve(context.Background(), modelcheckexecutor.ResolutionRequest{}); err == nil || !strings.Contains(err.Error(), "resolver is nil") {
		t.Fatalf("nil resolver 应报错: %v", err)
	}
	loaded, err := New(nil, "w12a-secret")
	if err != nil {
		t.Fatal(err)
	}
	account := modelcheckinput.AccountSnapshot{ID: "w12a-ghost"}
	if _, err := loaded.Resolve(context.Background(), modelcheckexecutor.ResolutionRequest{Account: account}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("缺失账户应报错: %v", err)
	}
}

func TestW12aCredentialEnvelopeErrors(t *testing.T) {
	const secret = "w12a-envelope-secret"
	// 无法解密的凭据。
	resolver, err := New([]Snapshot{{
		AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: "not-an-envelope"},
		Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(snapshotFor("acc", "1", "p", "r", "m"))); err == nil || !strings.Contains(err.Error(), "credential envelope is unavailable") {
		t.Fatalf("凭据解密失败应报错: %v", err)
	}
}

func TestW12aProxyEnvelopeErrors(t *testing.T) {
	const secret = "w12a-proxy-secret"
	credential, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	// proxy 信封无法解密。
	resolver, err := New([]Snapshot{{
		AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "proxy-v1",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: credential},
		Proxy:    &accounthealth.CredentialEnvelope{Ciphertext: "broken"},
		Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	proxyAccount := snapshotFor("acc", "1", "p", "r", "m")
	proxyAccount.ProxyConfigurationVersion = "proxy-v1"
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(proxyAccount)); err == nil || !strings.Contains(err.Error(), "proxy envelope is unavailable") {
		t.Fatalf("proxy 解密失败应报错: %v", err)
	}
	// proxy 信封内容非法 → invalid。
	proxy, err := accounthealth.EncryptV1Envelope(secret, []byte(`not-json`))
	if err != nil {
		t.Fatal(err)
	}
	resolver2, err := New([]Snapshot{{
		AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "proxy-v1",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: credential},
		Proxy:    &accounthealth.CredentialEnvelope{Ciphertext: proxy},
		Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	proxyAccount2 := snapshotFor("acc", "1", "p", "r", "m")
	proxyAccount2.ProxyConfigurationVersion = "proxy-v1"
	if _, err := resolver2.Resolve(context.Background(), resolutionRequest(proxyAccount2)); err == nil || !strings.Contains(err.Error(), "proxy envelope is invalid") {
		t.Fatalf("proxy 信封非法应报错: %v", err)
	}
}
