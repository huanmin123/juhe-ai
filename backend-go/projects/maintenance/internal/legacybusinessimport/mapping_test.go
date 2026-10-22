package legacybusinessimport

import (
	"strings"
	"testing"
)

// TestLegacyProfilePresetTable pins the fixed legacy-profile → connection-preset
// table. Every entry must also resolve a driver and a vendor consistent with the
// preset's own vendor column: a typo here silently mis-routes a production
// account after cutover.
func TestLegacyProfilePresetTable(t *testing.T) {
	for profile, preset := range legacyProfilePresets {
		driver, ok := presetDriver(preset)
		if !ok || driver == "" {
			t.Fatalf("profile %q → preset %q 没有 driver", profile, preset)
		}
		if _, ok := presetSurfaces[preset]; !ok {
			t.Fatalf("profile %q → preset %q 缺少 protocol surface 定义", profile, preset)
		}
		vendor := presetVendor(preset)
		if vendor == "" && driver != "generic_http" {
			t.Fatalf("profile %q → preset %q: 无 vendor 但 driver=%q（应为 generic_http）", profile, preset, driver)
		}
		if vendor != "" && driver != vendor {
			t.Fatalf("profile %q → preset %q: vendor=%q 与 driver=%q 不一致", profile, preset, vendor, driver)
		}
	}
}

// TestLegacyProfilePresetCoversProductionProfiles guards the exact profile ids
// observed in the 2026-10-22 production snapshot. A new legacy profile appearing
// in production must be added deliberately, not silently mapped to generic.
func TestLegacyProfilePresetCoversProductionProfiles(t *testing.T) {
	production := []string{
		"profile_gpt_openai_v1", "profile_anthropic_anthropic_v1", "profile_openai_openai_v1",
		"profile_xai_openai_v1", "profile_deepseek_openai_v1", "profile_glm_general_openai_v1",
		"profile_gemini_openai_chat_v1beta", "profile_glm_coding_openai_v1",
		"profile_gemini_native_v1beta", "profile_deepseek_anthropic_v1",
	}
	for _, profile := range production {
		if _, ok := legacyProfilePresets[profile]; !ok {
			t.Fatalf("生产快照中的 profile %q 没有固定映射", profile)
		}
	}
}

// TestPresetDefaultBaseURLMatchesRegistry verifies every preset a legacy profile
// can produce has a default base URL; a missing entry would leave migrated
// accounts with an empty upstream address when the credential carried none.
func TestPresetDefaultBaseURLMatchesRegistry(t *testing.T) {
	for _, preset := range legacyProfilePresets {
		if surface := presetSurfaces[preset]; surface == "" {
			continue
		}
		// generic_* 没有官方地址，必须由账户凭据自带 base_url；其余 preset
		// 必须有 registry 默认值。
		if strings.HasPrefix(preset, "generic_") {
			if _, ok := presetDefaultBaseURL[preset]; !ok {
				t.Fatalf("generic preset %q 必须显式登记（空串表示由凭据提供）", preset)
			}
			continue
		}
		if strings.TrimSpace(presetDefaultBaseURL[preset]) == "" {
			t.Fatalf("preset %q 缺少默认 base URL", preset)
		}
	}
}

// TestEffectiveModesFallsBackToVocabulary covers the two paths that matter for
// production: a declared set that survives normalisation is used as-is, and an
// empty/foreign set falls back to the whole preset vocabulary so the account is
// never migrated with zero executable modes.
func TestEffectiveModesFallsBackToVocabulary(t *testing.T) {
	declared := effectiveModes("gpt_openai_v1", []string{"chat_json", "responses_sse", "chat_json"})
	if len(declared) != 2 || declared[0] != "chat_json" || declared[1] != "responses_sse" {
		t.Fatalf("declared modes = %v, want deduped vocabulary order", declared)
	}
	fallback := effectiveModes("gpt_openai_v1", []string{"unknown_mode", ""})
	if len(fallback) != len(presetVocabulary["openai_v1"]) {
		t.Fatalf("fallback modes = %v, want full openai vocabulary", fallback)
	}
	// 越域 mode 被裁掉：anthropic preset 不接受 chat_json。
	anthropic := effectiveModes("anthropic_v1", []string{"chat_json"})
	if len(anthropic) != len(presetVocabulary["anthropic_v1"]) {
		t.Fatalf("anthropic fallback = %v", anthropic)
	}
}

// TestNormalizeModesDropsForeignModes locks the trimming behaviour: modes the
// target driver cannot execute must not survive into account_endpoint_modes.
func TestNormalizeModesDropsForeignModes(t *testing.T) {
	got := normalizeModes("gemini_native_v1beta", []string{"generate_content_json", "chat_json", " nope "})
	if len(got) != 1 || got[0] != "generate_content_json" {
		t.Fatalf("normalizeModes = %v", got)
	}
}

// TestVendorForLegacyProviderKeepsGenericEmpty pins the design rule that
// openai/hybrid never become the gpt vendor.
func TestVendorForLegacyProviderKeepsGenericEmpty(t *testing.T) {
	if vendor := vendorForLegacyProvider("openai"); vendor != "" {
		t.Fatalf("openai legacy provider must map to empty vendor, got %q", vendor)
	}
	if vendor := vendorForLegacyProvider("hybrid"); vendor != "" {
		t.Fatalf("hybrid legacy provider must map to empty vendor, got %q", vendor)
	}
	if vendor := vendorForLegacyProvider("gpt"); vendor != "gpt" {
		t.Fatalf("gpt legacy provider must map to gpt, got %q", vendor)
	}
}

// TestCredentialFingerprintIsStable makes sure the fingerprint written to the
// target is a pure function of the envelope (no secret material leaks).
func TestCredentialFingerprintIsStable(t *testing.T) {
	first := credentialFingerprint("v1:aaa:bbb:ccc")
	second := credentialFingerprint("v1:aaa:bbb:ccc")
	if first != second || len(first) != 64 {
		t.Fatalf("fingerprint = %q / %q", first, second)
	}
	if strings.Contains(first, "aaa") {
		t.Fatal("fingerprint 不得包含密文片段")
	}
}

// TestModelIndexPrefersUpstreamThenPublic documents the lookup order used when
// re-pointing legacy (provider, model) pairs at target model ids.
func TestModelIndexPrefersUpstreamThenPublic(t *testing.T) {
	index := &modelIndex{
		byUpstream: map[string]string{modelKey("gpt", "gpt-5.4"): "m_upstream"},
		byPublic:   map[string]string{modelKey("gpt", "gpt-5.4"): "m_public"},
	}
	if id, ok := index.lookup("gpt", "gpt-5.4"); !ok || id != "m_upstream" {
		t.Fatalf("lookup = %q,%v want upstream id", id, ok)
	}
	index.byPublic[modelKey("gpt", "alias")] = "m_alias"
	if id, ok := index.lookup("gpt", "alias"); !ok || id != "m_alias" {
		t.Fatalf("public fallback = %q,%v", id, ok)
	}
	if _, ok := index.lookup("gemini", "gpt-5.4"); ok {
		t.Fatal("跨 vendor 不得命中")
	}
}
