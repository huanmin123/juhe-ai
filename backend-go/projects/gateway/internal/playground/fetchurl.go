package playground

// fetch_url 工具实现（设计 §3.3/§7）：GET 公网 http(s) URL，20s 超时，下载
// 上限 512KB，剥 HTML 取正文（≤50KB）；SSRF 防护——仅 http/https、解析后按
// 共享上游 blocked ranges 拒绝私网/环回/链路本地/组播/CGNAT/保留地址。
// 拨号经守卫式 DialContext（解析-校验-钉扎拨号同一 IP），重定向不跟随
// （302/30x 一律按非 2xx 失败收敛，避免逐跳重校验的规避面）。失败不产生
// HTTP 错误：由调用方把「抓取失败:<原因>」作为工具结果回喂模型
// （chatStream.executeFetchTool）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// fetch_url 限额（设计 §3.3，包内常量，无环境开关）。重定向策略在客户端
// 构造处以 http.ErrUseLastResponse 固化为「不跟随」（守卫拨号只钉扎首跳
// 校验结果，逐跳重校验的规避面不开放）。
const (
	fetchTimeout        = 20 * time.Second
	fetchMaxBytes       = 512 * 1024 // 下载字节上限
	fetchTextMaxBytes   = 50 * 1024  // 剥 HTML 后正文上限
	fetchUserAgentValue = "juhe-ai-playground-fetch-url/1.0"
)

// ipForbidden 报告 IP 是否属于 SSRF 拒绝面：拒绝面以共享上游 blocked ranges
// 为准（私网/环回/链路本地/组播/CGNAT/保留段，IPv4 与 IPv6 一并覆盖；
// upstreamhttp.IsPrivateOrReservedIP 是部署级单源）。nil 一律拒绝。
func ipForbidden(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return sharedupstreamhttp.IsPrivateOrReservedIP(unmapIP(ip).String())
}

// validateFetchURL 校验抓取目标：仅 http/https、host 非空、解析后的地址全部
// 不在拒绝面。IP 字面量本地判定（无 DNS）；域名走系统解析器（任一解析结果
// 落拒绝面即整体拒绝——保守口径，避免挑地址绕过）。
func validateFetchURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("URL 无法解析: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("仅支持 http/https URL")
	}
	host := parsed.Hostname()
	if host == "" {
		return "", errors.New("URL 缺少主机名")
	}
	// IPv4-Mapped IPv6（::ffff:127.0.0.1）先还原为 v4 再判定，避免映射形态绕过。
	if ip := net.ParseIP(host); ip != nil {
		if ipForbidden(unmapIP(ip)) {
			return "", fmt.Errorf("目标地址 %s 属于内网/保留地址，禁止抓取", host)
		}
		return scheme, nil
	}
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		return "", errors.New("URL 主机名不支持链路本地 zone 标识")
	}
	resolveCtx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil {
		return "", fmt.Errorf("主机解析失败: %w", err)
	}
	if len(addresses) == 0 {
		return "", errors.New("主机没有可用的解析结果")
	}
	for _, address := range addresses {
		if ipForbidden(unmapIP(address.IP)) {
			return "", fmt.Errorf("目标地址 %s 解析到内网/保留地址（%s），禁止抓取", host, address.IP.String())
		}
	}
	return scheme, nil
}

// unmapIP 把 IPv4-mapped IPv6 地址还原为 IPv4（其余原样返回）。
func unmapIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// defaultFetchClient 是 fetch_url 的守卫客户端：DialContext 对每次拨号重走
// 「解析 → 校验 → 钉扎首 IP」路径（防校验与拨号之间的 DNS 重绑定），超时与
// 重定向策略按包内常量。生产与默认装配共用一个实例。
func defaultFetchClient() *http.Client {
	transport := &http.Transport{
		DialContext: guardedDialContext,
		// 复用上游http默认的代理解析会引入环境代理绕过 SSRF 校验，演练场
		// fetch_url 明确直连（Proxy: nil，不走 HTTP_PROXY 等环境变量）。
		Proxy: nil,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// guardedDialContext 是守卫拨号：解析目标主机并逐 IP 校验拒绝面，全部通过后
// 钉扎首个合法 IP 建连（连接地址即校验地址，无重绑定窗口）。
func guardedDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipForbidden(unmapIP(ip)) {
			return nil, fmt.Errorf("目标地址 %s 属于内网/保留地址，禁止连接", host)
		}
		return (&net.Dialer{Timeout: fetchTimeout}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("主机解析失败: %w", err)
	}
	for _, candidate := range addresses {
		if ipForbidden(unmapIP(candidate.IP)) {
			return nil, fmt.Errorf("目标地址 %s 解析到内网/保留地址（%s），禁止连接", host, candidate.IP.String())
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("主机没有可用的解析结果")
	}
	return (&net.Dialer{Timeout: fetchTimeout}).DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

var defaultFetchURLFunc = newDefaultFetchURL(defaultFetchClient())

// newDefaultFetchURL 用给定客户端构造 FetchURLFunc（测试可注入 httptest 或
// 自定义 transport；生产传 defaultFetchClient()）。
func newDefaultFetchURL(client *http.Client) FetchURLFunc {
	return func(ctx context.Context, rawURL string) (FetchResult, error) {
		if _, err := validateFetchURL(rawURL); err != nil {
			return FetchResult{}, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
		if err != nil {
			return FetchResult{}, fmt.Errorf("请求构造失败: %w", err)
		}
		request.Header.Set("user-agent", fetchUserAgentValue)
		request.Header.Set("accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
		response, err := client.Do(request)
		if err != nil {
			return FetchResult{}, fmt.Errorf("请求失败: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return FetchResult{}, fmt.Errorf("上游返回 HTTP %d", response.StatusCode)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, fetchMaxBytes+1))
		if readErr != nil {
			return FetchResult{}, fmt.Errorf("响应读取失败: %w", readErr)
		}
		if len(data) > fetchMaxBytes {
			return FetchResult{}, errors.New("响应超过 512KB 下载上限")
		}
		if len(data) == 0 {
			return FetchResult{}, errors.New("响应内容为空")
		}
		return FetchResult{Text: extractBodyText(response.Header.Get("Content-Type"), data)}, nil
	}
}

// defaultFetchURL 是装配缺省的 fetch_url 执行实现（Deps.FetchURL 为空时使用）。
func defaultFetchURL(ctx context.Context, rawURL string) (FetchResult, error) {
	return defaultFetchURLFunc(ctx, rawURL)
}

// extractBodyText 按内容类型剥 HTML 取正文（JSON 等非 HTML 原文返回，仍受
// 50KB 上限约束；HTML 经 stripHTMLToText 归一）。
func extractBodyText(contentType string, data []byte) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if mediaType != "" && mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return clampTextBytes(string(data), fetchTextMaxBytes)
	}
	return clampTextBytes(stripHTMLToText(data), fetchTextMaxBytes)
}

// clampTextBytes 把文本截断到 maxBytes 字节边界（不劈开多字节 UTF-8 序列）。
func clampTextBytes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := text[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

var (
	// scriptStyleBlockPatterns 是逐标签的非闭合回引模式（Go RE2 不支持 \1
	// 反向引用；脚本/样式等块各配独立模式整块移除）。
	scriptStyleBlockPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`),
		regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`),
		regexp.MustCompile(`(?is)<template\b[^>]*>.*?</template>`),
	}
	commentBlockPattern  = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTagPattern       = regexp.MustCompile(`(?s)<[^>]*>`)
	blockTagTailPattern  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|blockquote|pre|table|ul|ol|dl|dt|dd|header|footer|main|nav)(\s[^>]*)?/?>`)
	whitespaceRunPattern = regexp.MustCompile(`[ \t\r\f]+`)
	blankLineRunPattern  = regexp.MustCompile(`\n{3,}`)
	entityPattern        = regexp.MustCompile(`&(#[xX]?[0-9a-fA-F]+|[a-zA-Z]+);`)
)

// stripHTMLToText 把 HTML 字节剥成正文文本：脚本/样式/注释块整块移除，块级
// 标签边界折行，剩余标签剔除，实体解码，空白与空行收敛。
func stripHTMLToText(data []byte) string {
	text := string(data)
	for _, pattern := range scriptStyleBlockPatterns {
		text = pattern.ReplaceAllString(text, " ")
	}
	text = commentBlockPattern.ReplaceAllString(text, " ")
	// 块级标签先折行再统一剔标签，保证段落结构可读。
	text = blockTagTailPattern.ReplaceAllString(text, "\n")
	text = htmlTagPattern.ReplaceAllString(text, " ")
	text = decodeHTMLEntities(text)
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSpace(whitespaceRunPattern.ReplaceAllString(line, " "))
	}
	joined := strings.Join(lines, "\n")
	joined = blankLineRunPattern.ReplaceAllString(joined, "\n\n")
	return strings.TrimSpace(joined)
}

// namedEntities 是常用命名实体词表（HTML 语义空白与 XML 五实体；长尾数值
// 实体走 entityPattern 的数字分支）。
var namedEntities = map[string]string{
	"amp":  "&",
	"lt":   "<",
	"gt":   ">",
	"quot": "\"",
	"apos": "'",
	"nbsp": " ",
}

// decodeHTMLEntities 解码命名实体与数值（十进制/十六进制）实体；未知命名
// 实体原样保留（不做猜测展开）。
func decodeHTMLEntities(text string) string {
	return entityPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := match[1 : len(match)-1]
		if value, ok := namedEntities[strings.ToLower(name)]; ok {
			return value
		}
		if name[0] == '#' {
			digits := name[1:]
			base := 10
			if len(digits) > 1 && (digits[0] == 'x' || digits[0] == 'X') {
				digits = digits[1:]
				base = 16
			}
			var code int64
			for _, char := range digits {
				digit := runeDigitValue(char)
				if digit < 0 {
					return match
				}
				code = code*int64(base) + int64(digit)
				if code > 0x10FFFF {
					return match
				}
			}
			if code <= 0 {
				return match
			}
			return string(rune(code))
		}
		return match
	})
}

// runeDigitValue 返回字符在 36 进制下的数值（0-9a-f 覆盖十进制与十六进制），
// 越界返回 -1。
func runeDigitValue(char rune) int {
	switch {
	case char >= '0' && char <= '9':
		return int(char - '0')
	case char >= 'a' && char <= 'f':
		return int(char-'a') + 10
	case char >= 'A' && char <= 'F':
		return int(char-'A') + 10
	}
	return -1
}
