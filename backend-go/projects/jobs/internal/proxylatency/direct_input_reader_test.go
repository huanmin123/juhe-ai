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
		// 供应商解耦后 target 事实源 = vendors.enabled ∩ registry 派生 target
		// （§9.11：providers/provider_protocol_profiles 表退役）。
		"juhe_business.vendors",
		"v.enabled = 1",
		"AS t(provider,profile_id,target_url)",
		"LIMIT $1 OFFSET $2",
		"ORDER BY (p.last_tested_at IS NOT NULL) ASC,p.last_tested_at ASC,p.updated_at DESC,p.id ASC",
		"v.name AS provider_name",
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

func TestProxyLatencyCandidateSQLRegistryDerivedTargets(t *testing.T) {
	// target 全集由编译期 registry 派生：vendor 语义默认档与 gateway
	// vendorDefaultProtocolContext 同口径（gemini→native、glm→coding openai），
	// profile ID 走 ConnectionPresetProfileID 固定映射，URL 取
	// registry default_base_url（与 seed/UI 初始值同口径，非空）。
	byProvider := map[string]j3aVendorProbeTarget{}
	for _, target := range j3aVendorProbeTargets {
		byProvider[target.provider] = target
	}
	if byProvider["gemini"].profileID != "profile_gemini_native_v1beta" {
		t.Fatalf("gemini 默认 target=%+v", byProvider["gemini"])
	}
	if byProvider["glm"].profileID != "profile_glm_coding_openai_v1" {
		t.Fatalf("glm 默认 target=%+v", byProvider["glm"])
	}
	if byProvider["gpt"].targetURL != "https://api.openai.com/v1" {
		t.Fatalf("gpt 默认 target=%+v", byProvider["gpt"])
	}
	for _, target := range j3aVendorProbeTargets {
		if strings.TrimSpace(target.provider) == "" || strings.TrimSpace(target.profileID) == "" || strings.TrimSpace(target.targetURL) == "" {
			t.Fatalf("registry target 三元组必须完整: %+v", target)
		}
	}
	// SQL VALUES 行与 registry 全集一一对应（provider 维度）。
	for _, target := range j3aVendorProbeTargets {
		required := "('" + target.provider + "','" + target.profileID + "','" + target.targetURL + "')"
		if !strings.Contains(proxyLatencyCandidatesSQL, required) {
			t.Fatalf("候选 SQL 缺少 registry target 行 %q", required)
		}
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
