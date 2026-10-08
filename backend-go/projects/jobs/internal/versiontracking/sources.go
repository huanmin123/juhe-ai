// Package versiontracking 实现客户端版本自动跟版任务的发布源解析与单轮刷新
// （客户端版本自动跟版设计 §5/§6/§8.3）。覆盖五族：codex/claudeCode/
// geminiCLI/zcode/grokCLI（grokCLI 为第二期收尾追加，源契约见设计 §8.3）。
package versiontracking

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 家族键与 upstreamidentity / system_settings 自动键的合法键一致。
const (
	FamilyCodex      = "codex"
	FamilyClaudeCode = "claudeCode"
	FamilyGeminiCLI  = "geminiCLI"
	FamilyZCode      = "zcode"
	FamilyGrokCLI    = "grokCLI"
)

const (
	codexReleasesURL      = "https://api.github.com/repos/openai/codex/releases/latest"
	claudeCodeRegistryURL = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"
	geminiCLIRegistryURL  = "https://registry.npmjs.org/@google/gemini-cli/latest"
	zcodeReleasesURL      = "https://api.github.com/repos/zai-org/ZCode/releases/latest"
	// grokCLICargoTomlURL 是 grokCLI 家族的主动跟版源（设计 §8.3：xai-org/
	// grok-build 不发 GitHub Release/tag，不能用 releases/latest 惯用接口；
	// 改取 CLI 主 crate 的 Cargo.toml，raw.githubusercontent 国内可达，与
	// 现有四源同路）。
	grokCLICargoTomlURL = "https://raw.githubusercontent.com/xai-org/grok-build/main/crates/codegen/xai-grok-shell/Cargo.toml"

	codexOwner = "openai"
	codexRepo  = "codex"
	zcodeOwner = "zai-org"
	zcodeRepo  = "ZCode"

	claudeCodePackage = "@anthropic-ai/claude-code"
	geminiCLIPackage  = "@google/gemini-cli"

	codexTagPrefix = "rust-v"
	zcodeTagPrefix = "v"
)

// semverPattern 是发布源版本的唯一合法形状（设计 §5：^\d+\.\d+\.\d+$）。
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Source 是一个客户端家族的官方发布源（URL + 提取规则）。
type Source struct {
	Family string
	URL    string
	// GitHub 为 true 时请求必须带标识 UA（设计 §5）。
	GitHub bool
	parse  func(body []byte) (parsed ParsedRelease, err error)
}

// ParsedRelease 是单源解析结果。RawField 是提取前的原始版本字段
// （codex 为 name，缺失回退时为去前缀前的 tag_name；zcode 为去 v 前的 tag_name）。
type ParsedRelease struct {
	Version  string
	RawField string
}

// Sources 按设计 §5/§8.3 固定顺序返回五族发布源（grokCLI 追加在末位）。
func Sources() []Source {
	return []Source{
		{Family: FamilyCodex, URL: codexReleasesURL, GitHub: true, parse: parseCodexRelease},
		{Family: FamilyClaudeCode, URL: claudeCodeRegistryURL, parse: parseNPMRelease(FamilyClaudeCode, claudeCodePackage)},
		{Family: FamilyGeminiCLI, URL: geminiCLIRegistryURL, parse: parseNPMRelease(FamilyGeminiCLI, geminiCLIPackage)},
		{Family: FamilyZCode, URL: zcodeReleasesURL, GitHub: true, parse: parseZCodeRelease},
		{Family: FamilyGrokCLI, URL: grokCLICargoTomlURL, GitHub: true, parse: parseGrokCLICargoToml},
	}
}

// Parse 按该源的提取规则解析响应体。不满足契约即返回带原因的 error，不做容错猜测。
func (s Source) Parse(body []byte) (ParsedRelease, error) {
	if s.parse == nil {
		return ParsedRelease{}, fmt.Errorf("%s 发布源未配置 parser", s.Family)
	}
	return s.parse(body)
}

type githubRelease struct {
	Name            string          `json:"name"`
	TagName         string          `json:"tag_name"`
	Prerelease      bool            `json:"prerelease"`
	Draft           bool            `json:"draft"`
	HTMLURL         string          `json:"html_url"`
	URL             string          `json:"url"`
	TargetCommitish json.RawMessage `json:"target_commitish"`
}

func parseCodexRelease(body []byte) (ParsedRelease, error) {
	release, err := decodeGitHubRelease(FamilyCodex, body)
	if err != nil {
		return ParsedRelease{}, err
	}
	if err := rejectPrereleaseOrDraft(FamilyCodex, release); err != nil {
		return ParsedRelease{}, err
	}
	if err := assertGitHubRepository(FamilyCodex, release, codexOwner, codexRepo); err != nil {
		return ParsedRelease{}, err
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return ParsedRelease{}, fmt.Errorf("%s 发布响应缺少 tag_name", FamilyCodex)
	}
	if !strings.HasPrefix(tag, codexTagPrefix) {
		return ParsedRelease{}, fmt.Errorf("%s tag_name %q 缺少 %s 前缀", FamilyCodex, tag, codexTagPrefix)
	}
	tagVersion := strings.TrimPrefix(tag, codexTagPrefix)
	name := strings.TrimSpace(release.Name)
	rawField := name
	version := name
	if name == "" {
		rawField = tag
		version = tagVersion
	} else if name != tagVersion {
		return ParsedRelease{}, fmt.Errorf("%s name %q 与去前缀 tag_name %q 不一致", FamilyCodex, name, tagVersion)
	}
	if !semverPattern.MatchString(version) {
		return ParsedRelease{}, fmt.Errorf("%s 版本 %q 不是三段数字版本", FamilyCodex, version)
	}
	return ParsedRelease{Version: version, RawField: rawField}, nil
}

func parseZCodeRelease(body []byte) (ParsedRelease, error) {
	release, err := decodeGitHubRelease(FamilyZCode, body)
	if err != nil {
		return ParsedRelease{}, err
	}
	if err := rejectPrereleaseOrDraft(FamilyZCode, release); err != nil {
		return ParsedRelease{}, err
	}
	if err := assertGitHubRepository(FamilyZCode, release, zcodeOwner, zcodeRepo); err != nil {
		return ParsedRelease{}, err
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return ParsedRelease{}, fmt.Errorf("%s 发布响应缺少 tag_name", FamilyZCode)
	}
	if !strings.HasPrefix(tag, zcodeTagPrefix) || strings.HasPrefix(tag, codexTagPrefix) {
		return ParsedRelease{}, fmt.Errorf("%s tag_name %q 缺少 %s 前缀", FamilyZCode, tag, zcodeTagPrefix)
	}
	version := strings.TrimPrefix(tag, zcodeTagPrefix)
	if !semverPattern.MatchString(version) {
		return ParsedRelease{}, fmt.Errorf("%s 版本 %q 不是三段数字版本", FamilyZCode, version)
	}
	return ParsedRelease{Version: version, RawField: tag}, nil
}

func decodeGitHubRelease(family string, body []byte) (githubRelease, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return githubRelease{}, fmt.Errorf("%s 发布响应为空", family)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return githubRelease{}, fmt.Errorf("%s 发布响应不是合法 JSON: %w", family, err)
	}
	return release, nil
}

func rejectPrereleaseOrDraft(family string, release githubRelease) error {
	if release.Prerelease {
		return fmt.Errorf("%s 发布被标记为 prerelease", family)
	}
	if release.Draft {
		return fmt.Errorf("%s 发布被标记为 draft", family)
	}
	return nil
}

// assertGitHubRepository 用 html_url 或 url 校验 owner/repo 归属。两者都缺失
// 视为归属无法证明，拒绝该源（设计 §5：repository 归属不满足即拒绝）。
func assertGitHubRepository(family string, release githubRelease, owner, repo string) error {
	candidates := []string{strings.TrimSpace(release.HTMLURL), strings.TrimSpace(release.URL)}
	var saw bool
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		saw = true
		if githubRepositoryMatches(candidate, owner, repo) {
			return nil
		}
	}
	if !saw {
		return fmt.Errorf("%s 发布响应缺少 repository 归属字段（html_url/url）", family)
	}
	return fmt.Errorf("%s 发布不属于 %s/%s", family, owner, repo)
}

func githubRepositoryMatches(rawURL, owner, repo string) bool {
	marker := "/" + owner + "/" + repo + "/"
	if strings.Contains(rawURL, marker) {
		return true
	}
	// api.github.com/repos/{owner}/{repo} 尾部无额外斜杠的形态。
	suffix := "/" + owner + "/" + repo
	return strings.HasSuffix(rawURL, suffix)
}

type npmLatest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func parseNPMRelease(family, packageName string) func([]byte) (ParsedRelease, error) {
	return func(body []byte) (ParsedRelease, error) {
		if len(strings.TrimSpace(string(body))) == 0 {
			return ParsedRelease{}, fmt.Errorf("%s 发布响应为空", family)
		}
		var latest npmLatest
		if err := json.Unmarshal(body, &latest); err != nil {
			return ParsedRelease{}, fmt.Errorf("%s 发布响应不是合法 JSON: %w", family, err)
		}
		name := strings.TrimSpace(latest.Name)
		if name == "" {
			return ParsedRelease{}, fmt.Errorf("%s 发布响应缺少 name", family)
		}
		if name != packageName {
			return ParsedRelease{}, fmt.Errorf("%s 包名 %q 不是 %s", family, name, packageName)
		}
		version := strings.TrimSpace(latest.Version)
		if version == "" {
			return ParsedRelease{}, fmt.Errorf("%s 发布响应缺少 version", family)
		}
		if !semverPattern.MatchString(version) {
			return ParsedRelease{}, fmt.Errorf("%s 版本 %q 不是三段数字版本", family, version)
		}
		return ParsedRelease{Version: version, RawField: version}, nil
	}
}

// parseGrokCLICargoToml 解析 grok-build 仓库 xai-grok-shell crate 的
// Cargo.toml（设计 §8.3）：只取 [package] section 内的 version 键——从
// `[package]` 行起、到下一个行首 `[` section 头止；section 外的 version 行
// （[dependencies] 等段的依赖版本、[package.metadata] 子表）一律忽略，
// 不作"取全文第一个 version 行"假设（文件布局可变，[patch.crates-io]/
// [workspace] 可出现在 [package] 之前）。
func parseGrokCLICargoToml(body []byte) (ParsedRelease, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return ParsedRelease{}, fmt.Errorf("%s 发布响应为空", FamilyGrokCLI)
	}
	version, err := cargoTomlPackageVersion(FamilyGrokCLI, string(body))
	if err != nil {
		return ParsedRelease{}, err
	}
	if !semverPattern.MatchString(version) {
		return ParsedRelease{}, fmt.Errorf("%s 版本 %q 不是三段数字版本", FamilyGrokCLI, version)
	}
	return ParsedRelease{Version: version, RawField: version}, nil
}

// cargoTomlPackageVersion 在 Cargo.toml 文本中按 section 定位 [package] 内的
// 首个 version 键。section 头去掉行尾注释后必须精确等于 [package]
// （[package.metadata] 等子表不得误判）；任何行首 `[`（含 [[bench]] 等
// array-of-table 头）都终止 [package] 段。缺 [package] 与段内无 version
// 分别返回带原因 error。
func cargoTomlPackageVersion(family, text string) (string, error) {
	inPackage := false
	sawPackage := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			header := strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0])
			inPackage = header == "[package]"
			if inPackage {
				sawPackage = true
			}
			continue
		}
		if !inPackage || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "version" {
			continue
		}
		return cargoTomlQuotedValue(family, strings.TrimSpace(value))
	}
	if !sawPackage {
		return "", fmt.Errorf("%s 的 Cargo.toml 缺少 [package] section", family)
	}
	return "", fmt.Errorf("%s 的 [package] section 缺少 version", family)
}

// cargoTomlQuotedValue 取 TOML 值的引号内文本（双引号或单引号；裸值或引号
// 未闭合报错，取首个闭合引号以容忍行尾注释）。
func cargoTomlQuotedValue(family, value string) (string, error) {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return value[1 : 1+end], nil
		}
		return "", fmt.Errorf("%s 的 version 值引号未闭合: %q", family, value)
	}
	return "", fmt.Errorf("%s 的 version 值不是带引号字符串: %q", family, value)
}

// CompareSemver 比较两段已由 parser 保证形状的三段整数版本。
// 返回 -1/0/1；形状非法时返回 error，不猜测。
func CompareSemver(left, right string) (int, error) {
	leftParts, err := splitSemver(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := splitSemver(right)
	if err != nil {
		return 0, err
	}
	for index := 0; index < 3; index++ {
		if leftParts[index] < rightParts[index] {
			return -1, nil
		}
		if leftParts[index] > rightParts[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func splitSemver(version string) ([3]int, error) {
	if !semverPattern.MatchString(version) {
		return [3]int{}, fmt.Errorf("版本 %q 不是三段数字版本", version)
	}
	var parts [3]int
	for index, piece := range strings.Split(version, ".") {
		value := 0
		for _, digit := range piece {
			value = value*10 + int(digit-'0')
		}
		parts[index] = value
	}
	return parts, nil
}

// ErrSourceFailed 标记单源拉取或解析失败（调用方按族隔离，不中断其他族）。
var ErrSourceFailed = errors.New("发布源失败")
