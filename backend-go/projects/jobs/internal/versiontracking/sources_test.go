package versiontracking

import (
	"strings"
	"testing"
)

func TestParseCodexReleaseAcceptsMatchingNameAndTag(t *testing.T) {
	parsed, err := sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0",
		"tag_name": "rust-v0.160.0",
		"prerelease": false,
		"draft": false,
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.160.0"
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Version != "0.160.0" || parsed.RawField != "0.160.0" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseCodexReleaseFallsBackToTagWhenNameMissing(t *testing.T) {
	parsed, err := sourceByFamily(FamilyCodex).Parse([]byte(`{
		"tag_name": "rust-v0.160.1",
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.160.1"
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Version != "0.160.1" || parsed.RawField != "rust-v0.160.1" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseCodexReleaseRejectsNameTagMismatch(t *testing.T) {
	_, err := sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0",
		"tag_name": "rust-v0.159.9",
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.159.9"
	}`))
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCodexReleaseRejectsPrereleaseAndDraft(t *testing.T) {
	_, err := sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0",
		"tag_name": "rust-v0.160.0",
		"prerelease": true,
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.160.0"
	}`))
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("prerelease err = %v", err)
	}
	_, err = sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0",
		"tag_name": "rust-v0.160.0",
		"draft": true,
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.160.0"
	}`))
	if err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("draft err = %v", err)
	}
}

func TestParseCodexReleaseRejectsWrongRepositoryAndBadVersion(t *testing.T) {
	_, err := sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0",
		"tag_name": "rust-v0.160.0",
		"html_url": "https://github.com/other/codex/releases/tag/rust-v0.160.0"
	}`))
	if err == nil || !strings.Contains(err.Error(), "不属于") {
		t.Fatalf("repo err = %v", err)
	}
	_, err = sourceByFamily(FamilyCodex).Parse([]byte(`{
		"name": "0.160.0-beta",
		"tag_name": "rust-v0.160.0-beta",
		"html_url": "https://github.com/openai/codex/releases/tag/rust-v0.160.0-beta"
	}`))
	if err == nil || !strings.Contains(err.Error(), "三段数字版本") {
		t.Fatalf("version err = %v", err)
	}
}

func TestParseCodexReleaseRejectsMissingFields(t *testing.T) {
	_, err := sourceByFamily(FamilyCodex).Parse([]byte(`{"html_url":"https://github.com/openai/codex/releases/latest"}`))
	if err == nil || !strings.Contains(err.Error(), "tag_name") {
		t.Fatalf("err = %v", err)
	}
	_, err = sourceByFamily(FamilyCodex).Parse([]byte(`{"name":"0.160.0","tag_name":"rust-v0.160.0"}`))
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("ownership err = %v", err)
	}
}

func TestParseNPMReleaseAcceptsOfficialPackage(t *testing.T) {
	for _, sample := range []struct {
		family  string
		name    string
		version string
	}{
		{FamilyClaudeCode, "@anthropic-ai/claude-code", "2.1.300"},
		{FamilyGeminiCLI, "@google/gemini-cli", "0.62.1"},
	} {
		parsed, err := sourceByFamily(sample.family).Parse([]byte(`{"name":"` + sample.name + `","version":"` + sample.version + `"}`))
		if err != nil {
			t.Fatalf("%s parse: %v", sample.family, err)
		}
		if parsed.Version != sample.version || parsed.RawField != sample.version {
			t.Fatalf("%s parsed = %+v", sample.family, parsed)
		}
	}
}

func TestParseNPMReleaseRejectsWrongPackageMissingAndBadVersion(t *testing.T) {
	_, err := sourceByFamily(FamilyClaudeCode).Parse([]byte(`{"name":"@other/claude-code","version":"2.1.300"}`))
	if err == nil || !strings.Contains(err.Error(), "包名") {
		t.Fatalf("package err = %v", err)
	}
	_, err = sourceByFamily(FamilyGeminiCLI).Parse([]byte(`{"name":"@google/gemini-cli"}`))
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("missing err = %v", err)
	}
	_, err = sourceByFamily(FamilyGeminiCLI).Parse([]byte(`{"version":"0.62.1"}`))
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("missing name err = %v", err)
	}
	_, err = sourceByFamily(FamilyClaudeCode).Parse([]byte(`{"name":"@anthropic-ai/claude-code","version":"2.1"}`))
	if err == nil || !strings.Contains(err.Error(), "三段数字版本") {
		t.Fatalf("version err = %v", err)
	}
}

func TestParseZCodeReleaseAcceptsPrefixedTag(t *testing.T) {
	parsed, err := sourceByFamily(FamilyZCode).Parse([]byte(`{
		"tag_name": "v3.15.0",
		"html_url": "https://github.com/zai-org/ZCode/releases/tag/v3.15.0"
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Version != "3.15.0" || parsed.RawField != "v3.15.0" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseZCodeReleaseRejectsPrereleaseWrongRepoAndBadVersion(t *testing.T) {
	_, err := sourceByFamily(FamilyZCode).Parse([]byte(`{
		"tag_name": "v3.15.0",
		"prerelease": true,
		"url": "https://api.github.com/repos/zai-org/ZCode/releases/1"
	}`))
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("prerelease err = %v", err)
	}
	_, err = sourceByFamily(FamilyZCode).Parse([]byte(`{
		"tag_name": "v3.15.0",
		"html_url": "https://github.com/other/ZCode/releases/tag/v3.15.0"
	}`))
	if err == nil || !strings.Contains(err.Error(), "不属于") {
		t.Fatalf("repo err = %v", err)
	}
	_, err = sourceByFamily(FamilyZCode).Parse([]byte(`{
		"tag_name": "v3.15",
		"html_url": "https://github.com/zai-org/ZCode/releases/tag/v3.15"
	}`))
	if err == nil || !strings.Contains(err.Error(), "三段数字版本") {
		t.Fatalf("version err = %v", err)
	}
	_, err = sourceByFamily(FamilyZCode).Parse([]byte(`{"html_url":"https://github.com/zai-org/ZCode/releases/latest"}`))
	if err == nil || !strings.Contains(err.Error(), "tag_name") {
		t.Fatalf("missing err = %v", err)
	}
}

// grokCLICargoTomlFixture 按 xai-org/grok-build xai-grok-shell/Cargo.toml 的
// 真实形状构造（设计 §8.3）：首部注释行 + [patch.crates-io]（在 [workspace]
// 之前，段内含 version 干扰行）+ [package] 在文件后部 + 依赖段含裸版本与
// 内嵌 version 键的干扰行 + [[bench]]/[[bin]] array-of-table 头。
const grokCLICargoTomlFixture = `# Codegen shell crate. The CLI version lives in [package].version below.
[patch.crates-io]
rustls = { version = "0.23", git = "https://github.com/example/rustls" }

[workspace]
members = ["crates/*"]

[package]
license = "Apache-2.0"
name = "xai-grok-shell"
version = "1.0.45"
edition.workspace = true

[dependencies]
bm25 = "2.3"
serde = { version = "1.0", features = ["derive"] }
tokio-rustls = { version = "0.26", default-features = false, features = ["ring"] }

[dev-dependencies]
serial_test = { workspace = true }

[[bench]]
name = "session_list"
harness = false

[[bin]]
name = "chat-history-downgrade"
path = "src/bin/chat-history-downgrade.rs"
`

// TestParseGrokCLICargoTomlAcceptsPackageSectionVersion：真实形状样本下只取
// [package] section 内的 version，忽略 patch 段与依赖段的全部 version 行。
func TestParseGrokCLICargoTomlAcceptsPackageSectionVersion(t *testing.T) {
	parsed, err := sourceByFamily(FamilyGrokCLI).Parse([]byte(grokCLICargoTomlFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Version != "1.0.45" || parsed.RawField != "1.0.45" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

// TestParseGrokCLICargoTomlRejectsMissingPackageSectionAndBadVersion：缺
// [package]、section 内无 version、版本非法分别报带原因 error；子表
// [package.metadata] 中的 version 行不得被误取为 [package] 版本。
func TestParseGrokCLICargoTomlRejectsMissingPackageSectionAndBadVersion(t *testing.T) {
	_, err := sourceByFamily(FamilyGrokCLI).Parse([]byte(`[dependencies]
serde = { version = "1.0" }
`))
	if err == nil || !strings.Contains(err.Error(), "缺少 [package]") {
		t.Fatalf("missing package err = %v", err)
	}
	_, err = sourceByFamily(FamilyGrokCLI).Parse([]byte(`[package]
name = "xai-grok-shell"
edition.workspace = true
`))
	if err == nil || !strings.Contains(err.Error(), "缺少 version") {
		t.Fatalf("missing version err = %v", err)
	}
	_, err = sourceByFamily(FamilyGrokCLI).Parse([]byte(`[package]
version = "1.0.45-beta"
`))
	if err == nil || !strings.Contains(err.Error(), "三段数字版本") {
		t.Fatalf("bad version err = %v", err)
	}
	_, err = sourceByFamily(FamilyGrokCLI).Parse([]byte(`[package]
name = "xai-grok-shell"

[package.metadata]
version = "9.9.9"
`))
	if err == nil || !strings.Contains(err.Error(), "缺少 version") {
		t.Fatalf("子表 version 不得顶替 [package] 版本: %v", err)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.10", -1},
		{"2.0.0", "1.9.9", 1},
		{"0.10.0", "0.9.99", 1},
	}
	for _, sample := range cases {
		got, err := CompareSemver(sample.left, sample.right)
		if err != nil || got != sample.want {
			t.Fatalf("CompareSemver(%s, %s) = %d, %v; want %d", sample.left, sample.right, got, err, sample.want)
		}
	}
	if _, err := CompareSemver("1.2", "1.2.3"); err == nil {
		t.Fatal("非法形状必须返回 error")
	}
}

func TestSourcesCoverFiveFamilies(t *testing.T) {
	sources := Sources()
	if len(sources) != 5 {
		t.Fatalf("sources = %d", len(sources))
	}
	want := []string{FamilyCodex, FamilyClaudeCode, FamilyGeminiCLI, FamilyZCode, FamilyGrokCLI}
	for index, family := range want {
		if sources[index].Family != family || sources[index].URL == "" {
			t.Fatalf("source %d = %+v", index, sources[index])
		}
	}
	if !sources[0].GitHub || !sources[3].GitHub || !sources[4].GitHub || sources[1].GitHub || sources[2].GitHub {
		t.Fatal("GitHub UA 标记与设计不符")
	}
}

func sourceByFamily(family string) Source {
	for _, source := range Sources() {
		if source.Family == family {
			return source
		}
	}
	panic("missing source " + family)
}
