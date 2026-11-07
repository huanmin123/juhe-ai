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
		if vendor == "" {
			t.Fatalf("profile %q → preset %q: 没有真实厂商归属", profile, preset)
		}
		if driver != vendor {
			t.Fatalf("profile %q → preset %q: vendor=%q 与 driver=%q 不一致", profile, preset, vendor, driver)
		}
	}
}

// TestLegacyProfilePresetCoversProductionProfiles guards the exact profile ids
// observed in the 2026-10-22 production snapshot. A new legacy profile appearing
// in production must be added deliberately, not silently mapped to generic.
func TestLegacyProfilePresetCoversProductionProfiles(t *testing.T) {
	production := []string{
		"profile_gpt_openai_v1", "profile_anthropic_anthropic_v1",
		"profile_xai_openai_v1", "profile_deepseek_openai_v1", "profile_glm_general_openai_v1",
		"profile_gemini_openai_chat_v1beta", "profile_glm_coding_openai_v1",
		"profile_gemini_native_v1beta", "profile_deepseek_anthropic_v1",
	}
	for _, profile := range production {
		if _, ok := legacyProfilePresets[profile]; !ok {
			t.Fatalf("生产快照中的 profile %q 没有固定映射", profile)
		}
	}
	// 旧 openai/hybrid 档案不再映射到 generic preset，改为按模型推断厂商。
	for _, profile := range []string{
		"profile_openai_openai_v1", "profile_hybrid_openai_chat_v1",
		"profile_hybrid_anthropic_messages_v1", "profile_hybrid_gemini_native_v1beta",
	} {
		if _, ok := legacyGenericProfileSurfaces[profile]; !ok {
			t.Fatalf("旧 generic profile %q 缺少协议面映射", profile)
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
		byUpstream: map[string]modelIndexEntry{modelKey("gpt", "gpt-5.4"): {id: "m_upstream", bindable: true}},
		byPublic:   map[string]modelIndexEntry{modelKey("gpt", "gpt-5.4"): {id: "m_public", bindable: true}},
	}
	if entry, ok := index.lookup("gpt", "gpt-5.4"); !ok || entry.id != "m_upstream" {
		t.Fatalf("lookup = %q,%v want upstream id", entry.id, ok)
	}
	index.byPublic[modelKey("gpt", "alias")] = modelIndexEntry{id: "m_alias", bindable: true}
	if entry, ok := index.lookup("gpt", "alias"); !ok || entry.id != "m_alias" {
		t.Fatalf("public fallback = %q,%v", entry.id, ok)
	}
	if _, ok := index.lookup("gemini", "gpt-5.4"); ok {
		t.Fatal("跨 vendor 不得命中")
	}
}

// TestLegacyEndpointFamilyAliasesPinsRetirementBatch 钉死 revision 20 退役批的
// 反向别名表：三个历史 token 各归一到唯一现行 token（与 upgrade_steps.go 的
// 归一 CASE 同源，两处必须一起改）。
func TestLegacyEndpointFamilyAliasesPinsRetirementBatch(t *testing.T) {
	want := map[string]string{
		"anthropic_messages":      "messages",
		"gemini_generate_content": "generate_content",
		"gemini_stream_generate":  "stream_generate_content",
	}
	if len(legacyEndpointFamilyAliases) != len(want) {
		t.Fatalf("反向别名表条目数 = %d, want %d（新增别名须同步 revision 20 升级步骤的归一 CASE）",
			len(legacyEndpointFamilyAliases), len(want))
	}
	for legacy, canonical := range want {
		got, ok := legacyEndpointFamilyAliases[legacy]
		if !ok || got != canonical {
			t.Fatalf("别名 %q = %q,%v, want %q", legacy, got, ok, canonical)
		}
	}
}

// TestNormalizeEntryMappingFamilies 锁定归一行为：两侧全量归一，未知 token
// 原样返回（不静默改写）。
func TestNormalizeEntryMappingFamilies(t *testing.T) {
	cases := []struct{ source, target, wantSource, wantTarget string }{
		{"anthropic_messages", "anthropic_messages", "messages", "messages"},
		{"gemini_generate_content", "gemini_generate_content", "generate_content", "generate_content"},
		{"gemini_stream_generate", "gemini_generate_content", "stream_generate_content", "generate_content"},
		{"gemini_stream_generate", "gemini_stream_generate", "stream_generate_content", "stream_generate_content"},
		{"chat_completions", "responses", "chat_completions", "responses"},
		{"unknown_legacy_token", "messages", "unknown_legacy_token", "messages"},
	}
	for _, item := range cases {
		gotSource, gotTarget := normalizeEntryMappingFamilies(item.source, item.target)
		if gotSource != item.wantSource || gotTarget != item.wantTarget {
			t.Fatalf("normalize(%q, %q) = (%q, %q), want (%q, %q)",
				item.source, item.target, gotSource, gotTarget, item.wantSource, item.wantTarget)
		}
	}
}

// TestEntryMappingPairAdmissible 锁定协议等价判定：同族或 Gemini stream 归一
// pair 合规；矩阵外（含已退役 responses→chat_completions）一律不合规。
func TestEntryMappingPairAdmissible(t *testing.T) {
	for _, pair := range [][2]string{
		{"chat_completions", "chat_completions"},
		{"responses", "responses"},
		{"messages", "messages"},
		{"generate_content", "generate_content"},
		{"stream_generate_content", "generate_content"},
		{"video_generation", "video_generation"},
		{"tts", "tts"},
		{"anthropic_messages", "anthropic_messages"},
	} {
		if !entryMappingPairAdmissible(pair[0], pair[1]) {
			t.Fatalf("合规 pair %v 被误判为不合规", pair)
		}
	}
	for _, pair := range [][2]string{
		{"responses", "chat_completions"},
		{"chat_completions", "responses"},
		{"messages", "chat_completions"},
		{"generate_content", "stream_generate_content"},
		{"anthropic_messages", "chat_completions"},
		{"unknown_legacy_token", "responses"},
	} {
		if entryMappingPairAdmissible(pair[0], pair[1]) {
			t.Fatalf("跨协议 pair %v 被误判为合规", pair)
		}
	}
}
