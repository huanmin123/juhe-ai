package modelcheckresolver

import (
	"context"
	"strings"
	"testing"

	accounthealth "github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func w9hSnapshot(id string) Snapshot {
	return Snapshot{
		AccountID: id, ConfigRevision: "7", Provider: "openai",
		ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "profile-revision-7",
		EndpointFingerprint: "endpoint-fp", CredentialEnvelopeRef: "credential-ref",
		ProxyConfigurationVersion: "direct", CredentialType: "api_key",
		Protocol: modelcheckprofile.ProtocolOpenAIResponses,
		Model:    "gpt-5.6-sol", Prompt: "hello", Endpoint: "https://example.test",
	}
}

func w9hSealed(t *testing.T, payload string, secret string) accounthealth.CredentialEnvelope {
	t.Helper()
	ciphertext, err := accounthealth.EncryptV1Envelope(secret, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: ciphertext}
}

func TestW9HNewValidationArms(t *testing.T) {
	if _, err := New(nil, "  "); err == nil || !strings.Contains(err.Error(), "credential secret is required") {
		t.Fatalf("secret err=%v", err)
	}
	broken := w9hSnapshot("acc-1")
	broken.Model = ""
	if _, err := New([]Snapshot{broken}, "secret-w9h"); err == nil || !strings.Contains(err.Error(), "target snapshot is incomplete") {
		t.Fatalf("incomplete err=%v", err)
	}
	valid := w9hSnapshot("acc-1")
	valid.Credential = w9hSealed(t, `{"api_key":"sk-w9h"}`, "secret-w9h")
	if _, err := New([]Snapshot{valid, valid}, "secret-w9h"); err == nil || !strings.Contains(err.Error(), "duplicate account acc-1") {
		t.Fatalf("duplicate err=%v", err)
	}
	if _, err := New([]Snapshot{{AccountID: ""}}, "secret-w9h"); err == nil || !strings.Contains(err.Error(), "account id is required") {
		t.Fatalf("missing id err=%v", err)
	}
}

func TestW9HResolveArms(t *testing.T) {
	secret := "secret-w9h"
	input := w9hSnapshot("acc-1")
	input.Credential = w9hSealed(t, `{"api_key":"sk-w9h"}`, secret)
	resolver, err := New([]Snapshot{input}, secret)
	if err != nil {
		t.Fatal(err)
	}
	// nil resolver。
	var nilResolver *Resolver
	if _, err := nilResolver.Resolve(context.Background(), resolutionRequest(snapshotFor("acc-1", "7", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol"))); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil resolver err=%v", err)
	}
	// 未知账户。
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(snapshotFor("acc-absent", "7", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol"))); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing err=%v", err)
	}
	// 快照漂移。
	stale := snapshotFor("acc-1", "9", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol")
	if _, err := resolver.Resolve(context.Background(), resolutionRequest(stale)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale err=%v", err)
	}
	// 损坏的凭据封套。
	broken := input
	broken.Credential.Ciphertext = "garbage"
	brokenResolver, err := New([]Snapshot{broken}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenResolver.Resolve(context.Background(), resolutionRequest(snapshotFor("acc-1", "7", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol"))); err == nil || !strings.Contains(err.Error(), "credential envelope is unavailable") {
		t.Fatalf("broken envelope err=%v", err)
	}
	// 空 token。
	empty := input
	empty.Credential = w9hSealed(t, `   `, secret)
	emptyResolver, err := New([]Snapshot{empty}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyResolver.Resolve(context.Background(), resolutionRequest(snapshotFor("acc-1", "7", "profile_openai_openai_v1", "profile-revision-7", "gpt-5.6-sol"))); err == nil {
		t.Fatal("empty credential must fail")
	}
}

func TestW9HCredentialTokenArms(t *testing.T) {
	if token, err := credentialToken([]byte(`{"token":"plain-token"}`)); err != nil || token != "plain-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if token, err := credentialToken([]byte(`{"api_keys":["","pooled-key"]}`)); err != nil || token != "pooled-key" {
		t.Fatalf("pooled token=%q err=%v", token, err)
	}
	if token, err := credentialToken([]byte("bare-secret")); err != nil || token != "bare-secret" {
		t.Fatalf("bare token=%q err=%v", token, err)
	}
	if _, err := credentialToken([]byte("   ")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty err=%v", err)
	}
}

func TestW9HProxyAndLimitsHelpers(t *testing.T) {
	secret := "secret-w9h"
	// 无代理配置 → 空字符串。
	if got, err := proxyURLFromSnapshot(secret, w9hSnapshot("acc-1")); err != nil || got != "" {
		t.Fatalf("no proxy=%q err=%v", got, err)
	}
	// 代理封套损坏。
	withProxy := w9hSnapshot("acc-1")
	withProxy.Proxy = &accounthealth.CredentialEnvelope{Kind: "proxy", Ciphertext: "garbage"}
	if _, err := proxyURLFromSnapshot(secret, withProxy); err == nil || !strings.Contains(err.Error(), "proxy envelope is unavailable") {
		t.Fatalf("broken proxy err=%v", err)
	}
	// 代理封套缺 url。
	emptyProxy := w9hSnapshot("acc-1")
	emptyProxy.Proxy = &accounthealth.CredentialEnvelope{Kind: "proxy", Ciphertext: w9hSealedText(t, secret, `{}`)}
	if _, err := proxyURLFromSnapshot(secret, emptyProxy); err == nil || !strings.Contains(err.Error(), "proxy envelope is invalid") {
		t.Fatalf("invalid proxy err=%v", err)
	}
	// 有效代理 URL。
	goodProxy := w9hSnapshot("acc-1")
	goodProxy.Proxy = &accounthealth.CredentialEnvelope{Kind: "proxy", Ciphertext: w9hSealedText(t, secret, `{"url":"socks5://127.0.0.1:1080"}`)}
	got, err := proxyURLFromSnapshot(secret, goodProxy)
	if err != nil || got != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy=%q err=%v", got, err)
	}
	// timeout 与响应上限缺省。
	if timeoutFor(inputWithTimeout0()) != 30_000_000_000 {
		t.Fatal("default timeout")
	}
	if maxResponseBytesFor(w9hSnapshot("acc-1")) <= 0 {
		t.Fatal("default response bytes")
	}
}

func inputWithTimeout0() Snapshot { return w9hSnapshot("acc-1") }

func w9hSealedText(t *testing.T, secret, payload string) string {
	t.Helper()
	envelope, err := accounthealth.EncryptV1Envelope(secret, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
