package playground

// 出站 URL 守卫（原 fetch_url 工具实现随服务端工具循环退役，守卫面保留给
// 生图 URL 回退下载 images.go downloadGeneratedImageBytes）：仅 http/https、
// 解析后按共享上游 blocked ranges 拒绝私网/环回/链路本地/组播/CGNAT/保留
// 地址；拨号经守卫式 DialContext（解析-校验-钉扎拨号同一 IP）。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// 守卫拨号超时（包内常量）。重定向策略在客户端构造处以
// http.ErrUseLastResponse 固化为「不跟随」（守卫拨号只钉扎首跳校验结果，
// 逐跳重校验的规避面不开放）。
const fetchTimeout = 20 * time.Second

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
