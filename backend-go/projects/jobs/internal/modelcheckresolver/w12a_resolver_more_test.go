package modelcheckresolver

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// w12a_resolver_more_test.go 覆盖校验分支、凭据池回退、原生 token 与
// 非法代理地址的 SharedClient 失败分支。

func TestW12aValidateSnapshotBranches(t *testing.T) {
	const secret = "w12a-validate-secret"
	base := Snapshot{
		AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
		EndpointFingerprint: "fp", CredentialEnvelopeRef: "ref", ProxyConfigurationVersion: "direct",
		CredentialType: "api_key", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
	}
	cases := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"config revision", func(s *Snapshot) { s.ConfigRevision = " " }},
		{"profile snapshot", func(s *Snapshot) { s.ProtocolProfileRevision = "" }},
		{"target snapshot", func(s *Snapshot) { s.Model = "" }},
		{"identity snapshot", func(s *Snapshot) { s.EndpointFingerprint = "" }},
		{"credential snapshot", func(s *Snapshot) { s.CredentialType = "" }},
		{"protocol", func(s *Snapshot) { s.Protocol = "w12a-bad" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := base
			tc.mutate(&snapshot)
			_, err := New([]Snapshot{snapshot}, secret)
			if err == nil {
				t.Fatalf("非法快照应报错")
			}
		})
	}
}

func TestW12aCredentialTokenVariants(t *testing.T) {
	const secret = "w12a-token-secret"
	build := func(plain string) *Resolver {
		credential, err := accounthealth.EncryptV1Envelope(secret, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		resolver, err := New([]Snapshot{{
			AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
			EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct",
			CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: credential},
			Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
		}}, secret)
		if err != nil {
			t.Fatal(err)
		}
		return resolver
	}
	account := snapshotFor("acc", "1", "p", "r", "m")
	// api_keys 池内空字符串被跳过，取到后续键。
	resolver := build(`{"api_keys":["", "  ", "second-key"]}`)
	target, err := resolver.Resolve(context.Background(), resolutionRequest(account))
	if err != nil || target.Headers.Get("Authorization") != "Bearer second-key" {
		t.Fatalf("api_keys 池应跳过空串: %q %v", target.Headers.Get("Authorization"), err)
	}
	// 非 JSON 凭据原文直接作为 token。
	raw := build("raw-token-text")
	target, err = raw.Resolve(context.Background(), resolutionRequest(account))
	if err != nil || target.Headers.Get("Authorization") != "Bearer raw-token-text" {
		t.Fatalf("原文 token 应直接使用: %q %v", target.Headers.Get("Authorization"), err)
	}
}

func TestW12aSharedClientProxyURLError(t *testing.T) {
	const secret = "w12a-sharedclient-secret"
	credential, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"url":"://bad"}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New([]Snapshot{{
		AccountID: "acc", ConfigRevision: "1", Provider: "openai", ProtocolProfileID: "p", ProtocolProfileRevision: "r",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "proxy-v1",
		CredentialType: "api_key", Credential: accounthealth.CredentialEnvelope{Ciphertext: credential},
		Proxy:    &accounthealth.CredentialEnvelope{Ciphertext: proxy},
		Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "m", Endpoint: "https://x.test",
	}}, secret)
	if err != nil {
		t.Fatal(err)
	}
	proxyAccount := snapshotFor("acc", "1", "p", "r", "m")
	proxyAccount.ProxyConfigurationVersion = "proxy-v1"
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(proxyAccount)); err == nil || !strings.Contains(err.Error(), "model check proxy client") {
		t.Fatalf("非法代理地址应使客户端构造失败: %v", err)
	}
}
