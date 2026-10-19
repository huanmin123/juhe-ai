package accounts

// 连接 base_url validator（供应商与上游协议面重构设计 §2.1 base_url 约束，
// B1 断点批）。规则全部由 registry 数据驱动（backend-go/shared/contracts 的
// connector registry：allowed_base_path_suffixes[] + operation_path_templates[]），
// 不维护第二份 endpoint/前缀清单（设计 §1.1「URL validator 与 route builder 均
// 只能读取这两组数组」）：
//
//   - Unicode TrimSpace 后原样保存且长度 1..2048 UTF-8 bytes；
//   - 带 hostname 的绝对 http/https URL（两 scheme 均合法）；
//   - 禁 userinfo / query / fragment；
//   - 禁 `.` / `..` path segment、无效 percent escape、percent-decoded
//     slash/backslash、非 UTF-8 path segment；
//   - path 按 `/` 切段、每段 strict percent-decode 恰好一次；匹配前去掉 path
//     末尾全部 `/`（空 path 保持空，因此 `/v1` 与 `/v1/` 等价）；
//   - 解码后的 path 必须以该 preset 注册的一个 allowed_base_path_suffix 结束
//     （空 suffix 表示根路径，仅空 path 满足）；
//   - 拒绝任何 operation path template 的完整路径或中间前缀（如
//     `/v1beta/models/foo`），但已登记的 service/version suffix 不拒绝；
//     模板字面段区分大小写，`{name}` 匹配单个非空 segment，嵌入式占位符
//     （`{model}:generateContent`）匹配该 segment 的非空部分。
//
// 违规统一返回 400 族 ValidationError，错误文案以 invalid_connection_base_url
// 开头（§8 固定错误码）。SSRF/DNS/拨号校验不属本函数（§2.1 尾段：保存与出站
// 各自执行核心功能设计的同一 URL 安全校验）。

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// connectionURLContract 返回 (driver, account_type, oauth_type, preset, domain)
// 组合在 registry 注册的 base path suffix 与 operation path template 集合；
// 未注册组合 ok=false（组合合法性由 IsAllowedConnectorCombo 另行判定，这里只
// 取 URL 契约数据）。registry 的公开 API 以单 mode 组合展开导出，同一 preset
// 的 suffix/template 全组合一致，取首个命中组合的副本。
func connectionURLContract(driver, accountType, oauthType, preset, domain string) (suffixes, templates []string, ok bool) {
	for _, combo := range contracts.AllowedEndpointModeCombos() {
		if combo.Driver == driver && combo.AccountType == accountType &&
			combo.OAuthType == oauthType && combo.Preset == preset && combo.Domain == domain {
			return append([]string(nil), combo.AllowedBaseSuffixes...),
				append([]string(nil), combo.OperationTemplates...), true
		}
	}
	return nil, nil, false
}

// invalidConnectionBaseURL 渲染 §8 固定错误码 400。
func invalidConnectionBaseURL(reason string) error {
	return &ValidationError{Message: "invalid_connection_base_url: " + reason}
}

// validateConnectionBaseURL 按 §2.1 校验一个连接 base_url；suffixes/templates
// 来自 registry preset record（connectionURLContract）。
func validateConnectionBaseURL(suffixes, templates []string, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return invalidConnectionBaseURL("连接 Base URL 不能为空")
	}
	if length := len(trimmed); length < 1 || length > 2048 {
		return invalidConnectionBaseURL("连接 Base URL 长度必须是 1..2048 字节")
	}
	// 复用既有上游 URL 原始串检查（空白/控制字符/反斜杠），再手工切出
	// scheme/authority/path/query/fragment（parseRawAbsoluteURLParts 先于
	// URL parser，畸形形态以明确文案失败）。
	input, err := validateRawUpstreamURLString(trimmed)
	if err != nil {
		return invalidConnectionBaseURL(err.Error())
	}
	parts, err := parseRawAbsoluteURLParts(input)
	if err != nil {
		return invalidConnectionBaseURL(err.Error())
	}
	if strings.Contains(parts.authority, "@") {
		return invalidConnectionBaseURL("连接 Base URL 不能包含用户名或密码")
	}
	if parts.query != "" {
		return invalidConnectionBaseURL("连接 Base URL 不能包含查询参数")
	}
	if parts.hash != "" {
		return invalidConnectionBaseURL("连接 Base URL 不能包含片段标识")
	}
	parsed, err := url.Parse(input)
	if err != nil {
		return invalidConnectionBaseURL("连接 Base URL 格式无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return invalidConnectionBaseURL("连接 Base URL 只允许 http 或 https 协议")
	}
	if parsed.Hostname() == "" {
		return invalidConnectionBaseURL("连接 Base URL 必须包含主机名")
	}
	if parsed.User != nil {
		return invalidConnectionBaseURL("连接 Base URL 不能包含用户名或密码")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return invalidConnectionBaseURL("连接 Base URL 不能包含查询参数")
	}
	if parsed.Fragment != "" {
		return invalidConnectionBaseURL("连接 Base URL 不能包含片段标识")
	}
	if parts.path != "" && !strings.HasPrefix(parts.path, "/") {
		return invalidConnectionBaseURL("连接 Base URL 路径必须以 / 开头")
	}
	decoded, err := decodedConnectionPathSegments(parts.path)
	if err != nil {
		return err
	}
	if connectionPathEndsWithSuffix(decoded, suffixes) {
		return nil
	}
	// suffix 未命中时，先区分「operation 路径」与「普通越界前缀」两类文案。
	if connectionOperationPathViolation(templates, decoded) {
		return invalidConnectionBaseURL("连接 Base URL 不能填写接口路径或其前缀，请填写服务根地址或已登记的版本前缀")
	}
	return invalidConnectionBaseURL("连接 Base URL 路径必须以该连接预设登记的版本前缀结束")
}

// decodedConnectionPathSegments 把 raw path 按 `/` 切段并对每段 strict
// percent-decode 恰好一次（§2.1）。拒绝：空段（连续斜杠）、无效 percent
// escape、解码后 slash/backslash、`.` / `..` 段、非 UTF-8 段（raw 与 decoded
// 双侧校验）。末尾全部 `/` 去掉（空 path 保持空）。
func decodedConnectionPathSegments(rawPath string) ([]string, error) {
	if rawPath == "" || rawPath == "/" {
		return nil, nil
	}
	trimmed := rawPath
	for len(trimmed) > 1 && strings.HasSuffix(trimmed, "/") {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	segments := []string{}
	for _, raw := range strings.Split(strings.TrimPrefix(trimmed, "/"), "/") {
		if raw == "" {
			// 连续斜杠（`//v1`）切出空段：路径形态非法。
			return nil, invalidConnectionBaseURL("连接 Base URL 路径不能包含连续斜杠")
		}
		if !utf8.ValidString(raw) {
			return nil, invalidConnectionBaseURL("连接 Base URL 路径段必须是有效 UTF-8 文本")
		}
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			return nil, invalidConnectionBaseURL("连接 Base URL 路径编码无效")
		}
		if strings.ContainsAny(decoded, "/\\") {
			return nil, invalidConnectionBaseURL("连接 Base URL 路径不能包含编码后的斜杠")
		}
		if !utf8.ValidString(decoded) {
			return nil, invalidConnectionBaseURL("连接 Base URL 路径段必须是有效 UTF-8 文本")
		}
		if decoded == "." || decoded == ".." {
			return nil, invalidConnectionBaseURL("连接 Base URL 路径不能包含 . 或 .. 段")
		}
		segments = append(segments, decoded)
	}
	return segments, nil
}

// connectionSuffixSegments 把 registry suffix（解码后 path suffix，如 "/v1"、
// ""、"/compatible-mode/v1"）切成段；"" 表示根路径（空段集）。
func connectionSuffixSegments(suffix string) []string {
	segments := []string{}
	for _, part := range strings.Split(strings.Trim(suffix, "/"), "/") {
		if part == "" {
			continue
		}
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" {
			continue
		}
		segments = append(segments, decoded)
	}
	return segments
}

// connectionPathEndsWithSuffix 判定解码 path 段序列是否以任一注册 suffix 的段
// 序列结束；空 suffix 仅空 path 满足（根路径）。
func connectionPathEndsWithSuffix(path []string, suffixes []string) bool {
	for _, suffix := range suffixes {
		want := connectionSuffixSegments(suffix)
		if len(want) == 0 {
			if len(path) == 0 {
				return true
			}
			continue
		}
		if len(path) < len(want) {
			continue
		}
		tail := path[len(path)-len(want):]
		matched := true
		for index := range want {
			if tail[index] != want[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// connectionOperationPathViolation 判定 path 是否命中任一 operation template
// 的完整路径或中间前缀（§2.1）。完整路径：段数相等且逐段全匹配；中间前缀：
// path 比模板短、前缀段逐段全匹配且边界段可能是模板段的「开头」。
func connectionOperationPathViolation(templates []string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, template := range templates {
		templatePath := template
		if index := strings.Index(template, " "); index >= 0 {
			templatePath = template[index+1:]
		}
		segments := connectionSuffixSegments(templatePath)
		if len(segments) == 0 {
			continue
		}
		if len(path) == len(segments) && connectionTemplateSegmentsFullMatch(segments, path) {
			return true
		}
		if len(path) < len(segments) && connectionTemplateSegmentsPrefixMatch(segments, path) {
			return true
		}
	}
	return false
}

// connectionTemplateSegmentPattern 把模板段编译为锚定正则：`{name}` 匹配非空
// 任意文本，其余字面字节按原文匹配（区分大小写）。嵌入式占位符
// （`{model}:generateContent`）因此匹配「非空前缀 + 字面尾部」。编译失败返回
// nil（registry 模板为编译期常量，正常不触发；nil 按不匹配处理）。
func connectionTemplateSegmentPattern(segment string) *regexp.Regexp {
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(segment); {
		if segment[index] == '{' {
			end := strings.IndexByte(segment[index:], '}')
			if end < 0 {
				builder.WriteString(regexp.QuoteMeta(segment[index:]))
				break
			}
			builder.WriteString(".+")
			index += end + 1
			continue
		}
		next := strings.IndexByte(segment[index:], '{')
		if next < 0 {
			builder.WriteString(regexp.QuoteMeta(segment[index:]))
			break
		}
		builder.WriteString(regexp.QuoteMeta(segment[index : index+next]))
		index += next
	}
	builder.WriteString("$")
	compiled, err := regexp.Compile(builder.String())
	if err != nil {
		return nil
	}
	return compiled
}

// connectionTemplateSegmentsFullMatch 判定 path 段序列与模板段序列等长逐段
// 全匹配。
func connectionTemplateSegmentsFullMatch(segments, path []string) bool {
	for index, templateSegment := range segments {
		pattern := connectionTemplateSegmentPattern(templateSegment)
		if pattern == nil || !pattern.MatchString(path[index]) {
			return false
		}
	}
	return true
}

// connectionTemplateSegmentsPrefixMatch 判定 path 是模板的中间前缀：前
// len(path)-1 段全匹配，边界段满足 possible-prefix。
func connectionTemplateSegmentsPrefixMatch(segments, path []string) bool {
	for index := 0; index < len(path)-1; index++ {
		pattern := connectionTemplateSegmentPattern(segments[index])
		if pattern == nil || !pattern.MatchString(path[index]) {
			return false
		}
	}
	boundaryTemplate := segments[len(path)-1]
	boundaryPath := path[len(path)-1]
	if boundaryPath == "" {
		return false
	}
	// 模板段以占位符开头（纯 `{name}` 或嵌入式 `{model}:...`）时，任意非空
	// path 段都可能是它的开头（占位符匹配非空任意文本）；否则要求 path 段是
	// 该字面段的非空前缀（含全长——等长情形已由全匹配分支覆盖，此处保持
	// 前缀语义）。
	return strings.HasPrefix(boundaryTemplate, "{") || strings.HasPrefix(boundaryTemplate, boundaryPath)
}
