package accounts

// 连接 base_url validator（供应商与上游协议面重构设计 §2.1 base_url 约束，
// 上游 Base URL 版本根语义 v2 R8 统一规则）。规则全部由 registry 数据驱动
// （backend-go/shared/contracts 的 connector registry：operation_path_templates
// []，连接域按 preset 取 EndpointModeCombo.OperationTemplates），不维护第二份
// endpoint/前缀清单（设计 §1.1「URL validator 与 route builder 均只能读取这组
// 数组」）：
//
//   - Unicode TrimSpace 后原样保存且长度 1..2048 UTF-8 bytes；
//   - 带 hostname 的绝对 http/https URL（两 scheme 均合法）；
//   - 禁 userinfo / query / fragment；
//   - 禁 `.` / `..` path segment、无效 percent escape、percent-decoded
//     slash/backslash、非 UTF-8 path segment；
//   - path 按 `/` 切段、每段 strict percent-decode 恰好一次；匹配前去掉 path
//     末尾全部 `/`（空 path 保持空，因此 `/v1` 与 `/v1/` 等价）；
//   - R8 统一路径规则：base 路径按段切分后取任意连续后缀，命中登记的操作
//     模板（已去版本前缀；qwen preset 保持全路径为唯一例外）的完整路径或其
//     按段前缀 → 拒绝；版本根（/v1、/api/v3、/api/paas/v4、/compatible-mode
//     /v1 等）、`/api/plan/v3` 类服务前缀与空路径放行。allowed_base_path_
//     suffixes 自 R8 起不再参与写面判定（registry 数据保留）；
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
// 组合在 registry 注册的 operation path template 集合；未注册组合 ok=false
// （组合合法性由 IsAllowedConnectorCombo 另行判定，这里只取 URL 契约数据）。
// registry 的公开 API 以单 mode 组合展开导出，同一 preset 的 template 全组合
// 一致，取首个命中组合的副本。R8：allowed_base_path_suffixes 不再参与写面
// 判定，不再返回。
func connectionURLContract(driver, accountType, oauthType, preset, domain string) (templates []string, ok bool) {
	for _, combo := range contracts.AllowedEndpointModeCombos() {
		if combo.Driver == driver && combo.AccountType == accountType &&
			combo.OAuthType == oauthType && combo.Preset == preset && combo.Domain == domain {
			return append([]string(nil), combo.OperationTemplates...), true
		}
	}
	return nil, false
}

// invalidConnectionBaseURL 渲染 §8 固定错误码 400。
func invalidConnectionBaseURL(reason string) error {
	return &ValidationError{Message: "invalid_connection_base_url: " + reason}
}

// connectionBasePathViolationMessage 是凭据域/连接域统一的 R8 违规文案
// （各域保留自己的前缀风格，见 upstream_base_url.go 的凭据域常量）。
const connectionBasePathViolationMessage = "连接 Base URL 不能填写具体接口路径，请填写包含版本前缀的根地址，例如 /v1、/api/v3"

// validateConnectionBaseURL 按 §2.1 + R8 校验一个连接 base_url；templates
// 来自 registry preset record（connectionURLContract）。
func validateConnectionBaseURL(templates []string, raw string) error {
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
	// R8 统一路径规则：空路径放行（版本根可省略为 origin）；任意连续后缀
	// 命中登记操作模板的完整路径或按段前缀 → 拒绝。
	if basePathHitsOperationPath(templates, decoded) {
		return invalidConnectionBaseURL(connectionBasePathViolationMessage)
	}
	return nil
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

// basePathHitsOperationPath 实现 R8 统一路径规则（凭据域/连接域共用）：base
// 解码段序列的任意连续后缀命中任一操作模板的完整路径或按段前缀 → true。
// 空段序列（空路径）恒 false（版本根可省略为 origin）。
func basePathHitsOperationPath(templates []string, path []string) bool {
	for index := range path {
		if connectionOperationPathViolation(templates, path[index:]) {
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
