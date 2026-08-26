package proxylatency

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProxyLatencyCandidateSQLKeepsBusinessEligibilityAndStableOrder(t *testing.T) {
	for _, required := range []string{
		"p.enabled = TRUE",
		"p.enabled = 1",
		"ppp.enabled = 1",
		"profile_gemini_native_v1beta",
		"profile_glm_coding_openai_v1",
		"LIMIT $1 OFFSET $2",
		"ORDER BY (p.last_tested_at IS NOT NULL) ASC,p.last_tested_at ASC,p.updated_at DESC,p.id ASC",
		"row_number() OVER",
		"ppp.updated_at DESC,ppp.id ASC",
		"p.name AS provider_name",
		"target.provider_name ASC,target.provider ASC",
	} {
		if !strings.Contains(proxyLatencyCandidatesSQL, required) {
			t.Fatalf("候选 SQL 缺少 %q", required)
		}
	}
	if strings.Contains(proxyLatencyCandidatesSQL, "juhe_jobs") || strings.Contains(proxyLatencyCandidatesSQL, "INSERT ") || strings.Contains(proxyLatencyCandidatesSQL, "UPDATE ") {
		t.Fatalf("业务输入 reader 必须保持只读: %s", proxyLatencyCandidatesSQL)
	}
}

func TestProxyLatencyCandidateSQLMatchesNodeProfileFallbackAndSpecialPriority(t *testing.T) {
	// Node first keeps enabled profiles when any exist; only within that candidate
	// set do Gemini/GLM protocol IDs outrank the ordinary stable ordering.
	for _, required := range []string{
		"CASE WHEN ppp.enabled = 1 THEN 0 ELSE 1 END",
		"CASE WHEN (p.code = 'gemini' AND ppp.id = 'profile_gemini_native_v1beta')",
		"OR (p.code = 'glm' AND ppp.id = 'profile_glm_coding_openai_v1') THEN 0 ELSE 1 END",
		"ppp.updated_at DESC,ppp.id ASC",
	} {
		if !strings.Contains(proxyLatencyCandidatesSQL, required) {
			t.Fatalf("profile resolver SQL 缺少 Node 兼容排序 %q", required)
		}
	}
	// An outer enabled-only filter would silently drop Node's all-disabled fallback.
	if strings.Contains(proxyLatencyCandidatesSQL, "WHERE ppp.enabled") {
		t.Fatalf("profile resolver must retain Node all-disabled fallback: %s", proxyLatencyCandidatesSQL)
	}
}

func TestInputDraftCannotBeSerializedBeforeStoreIssuance(t *testing.T) {
	draft := InputDraft{ProxyID: "proxy-1"}
	if _, err := json.Marshal(draft); err == nil {
		t.Fatal("input draft must not serialize before Store issuance")
	}
}

func TestMakeProxyLatencyCandidateRejectsBadInputWithoutPasswordLeak(t *testing.T) {
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	secretEnvelope := "v1:bm9uY2U:YmFkLXRhZw:YmFkLWNpcGhlcnRleHQ"
	_, err := makeProxyLatencyInputDraft(proxyLatencyCandidateAssembly{
		row:     proxyLatencyCandidateRow{proxyID: "proxy-bad", proxyType: "http", proxyHost: "127.0.0.1", proxyPort: 8080, proxyEnabled: true, configRevision: "bad-time", passwordEncrypted: secretEnvelope},
		targets: []Target{{Provider: "gpt", ProfileID: "profile", URL: "https://api.openai.com/v1"}},
	}, now, time.Minute)
	if err == nil || strings.Contains(err.Error(), secretEnvelope) || strings.Contains(err.Error(), "bad-ciphertext") {
		t.Fatalf("无效输入必须 fail-closed 且不泄露 password envelope: %v", err)
	}
}

func TestMakeProxyLatencyCandidatePreservesOnlyEncryptedPasswordEnvelope(t *testing.T) {
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	envelope := "v1:MTIzNDU2Nzg5MDEy:MTIzNDU2Nzg5MDEyMzQ1Ng:Y2lwaGVydGV4dA"
	candidate, err := makeProxyLatencyInputDraft(proxyLatencyCandidateAssembly{
		row:     proxyLatencyCandidateRow{proxyID: "proxy-1", proxyType: "socks5h", proxyHost: "127.0.0.1", proxyPort: 1080, proxyUsername: "user", passwordEncrypted: envelope, proxyEnabled: true, configRevision: "2026-08-21T03:59:00Z"},
		targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
	}, now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ProxyPassword == nil || candidate.ProxyPassword.Kind != "proxy_password" || candidate.ProxyPassword.Ciphertext != envelope {
		t.Fatal("reader 必须只保留 opaque password envelope")
	}
	if candidate.ExpiresAt.Sub(candidate.IssuedAt) != 5*time.Minute || candidate.Trigger != TriggerPeriodic || candidate.PolicyVersion != proxyLatencyInputPolicyVersion {
		t.Fatal("冻结 input 元数据不正确")
	}
}

func TestMakeProxyLatencyCandidateRejectsInvalidTTLAndBoolean(t *testing.T) {
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	base := proxyLatencyCandidateAssembly{row: proxyLatencyCandidateRow{proxyID: "proxy-1", proxyType: "http", proxyHost: "127.0.0.1", proxyPort: 8080, proxyEnabled: true, configRevision: "2026-08-21T03:59:00Z"}, targets: []Target{{Provider: "gpt", ProfileID: "profile", URL: "https://api.openai.com/v1"}}}
	if _, err := makeProxyLatencyInputDraft(base, now, 30*time.Second); err == nil {
		t.Fatal("TTL 必须 fail-closed")
	}
	base.row.proxyEnabled = false
	if _, err := makeProxyLatencyInputDraft(base, now, time.Minute); err == nil {
		t.Fatal("enabled=false 候选必须 fail-closed")
	}
	base.row.proxyEnabled = true
}

func TestMakeProxyLatencyCandidatePreservesInvalidProviderTargetAsUnknown(t *testing.T) {
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	draft, err := makeProxyLatencyInputDraft(proxyLatencyCandidateAssembly{
		row: proxyLatencyCandidateRow{proxyID: "proxy-1", proxyType: "http", proxyHost: "127.0.0.1", proxyPort: 8080, proxyEnabled: true, configRevision: "2026-08-21T03:59:00Z"},
		targets: []Target{
			{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"},
			{Provider: "hybrid", ProfileID: "profile-hybrid", URL: ""},
			{Provider: "unsupported", ProfileID: "profile-unsupported", URL: "ftp://provider.invalid/v1"},
		},
	}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Targets) != 3 || draft.Targets[0].URL == "" || draft.Targets[1].URL != "" || draft.Targets[1].ProbeError != targetProbeErrorInvalidURL || draft.Targets[2].URL != "" || draft.Targets[2].ProbeError != targetProbeErrorInvalidURL {
		t.Fatalf("invalid provider target must remain an explicit unknown item: %+v", draft.Targets)
	}
}

func TestMakeProxyLatencyInputDraftAllowsUsernameOnlyProxy(t *testing.T) {
	now := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	draft, err := makeProxyLatencyInputDraft(proxyLatencyCandidateAssembly{
		row:     proxyLatencyCandidateRow{proxyID: "proxy-user-only", proxyType: "http", proxyHost: "127.0.0.1", proxyPort: 8080, proxyUsername: "user", proxyEnabled: true, configRevision: "2026-08-21T03:59:00Z"},
		targets: []Target{{Provider: "gpt", ProfileID: "profile", URL: "https://api.openai.com/v1"}},
	}, now, time.Minute)
	if err != nil || draft.ProxyPassword != nil || draft.ProxyUsername != "user" {
		t.Fatalf("username-only proxy must remain valid: draft=%+v err=%v", draft, err)
	}
}
