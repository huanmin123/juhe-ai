package main

// 生图 URL 下载代理端口装配（BUG-0232 关联事实的落地）：上游
// /v1/images/edits（grok-imagine）只返回 imgen.x.ai 临时图片链接，国内
// 单机直连不可达——按生图绑定账户的 proxy_profile 构造出站 HTTP 客户端。
// 客户端经部署级 DialGuard 校验 socket 对端，并在请求发出前由
// targetGuardRoundTripper 对最终目标主机做同一 URL 安全配置面的本地解析
// 校验（生图 URL 下载 SSRF 修复，与 /v1 dispatch 链同一 URL 安全配置面）。
// 未绑定代理、代理停用或解析失败一律返回 nil（直连默认），代理配置问题
// 不升级为生图失败（与 buildProxyClient / proxyProfileRequestURL 同一构造
// 范式：scheme 小写化、socks5 升级 socks5h、密码走 v1 AES-GCM 信封）。

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// newChatImageDownloadProxy 构造 chat.Deps.ImageDownloadProxy 端口：每次解
// 析按账户直查（生图为秒级低频操作，两行查询的直读开销可忽略，且天然跟随
// 绑定变更，无缓存失效面）。urlSecurity 是部署级上游 URL 安全配置（与 /v1
// dispatch 链的 UpstreamURLSecurity 同源）：guard 校验的是实际 socket 对端
// （代理路径即代理主机），私网代理是否放行由部署配置裁决，不在此处硬编码。
func newChatImageDownloadProxy(db *sql.DB, postgres bool, secret string, urlSecurity UpstreamURLSecurityConfig, warn func(message string)) func(accountID string) *http.Client {
	// 单例 guard（transport_urlpolicy.go 同一装配方式）：guard 实例身份参与
	// SharedClient 池键，按工厂复用同一实例避免按账户碎片化传输池。
	guard := upstreamhttp.NewDialGuard(urlSecurity, nil, nil)
	bind := func(query string) string { return accountscore.SQLBind(postgres, query) }
	table := func(name string) string {
		if postgres {
			return "juhe_business." + name
		}
		return name
	}
	return func(accountID string) *http.Client {
		client, err := chatImageProxyClientFor(db, bind, table, secret, guard, accountID)
		if err != nil {
			// 代理不可用时回落直连并留痕；直连失败再按既有下载错误面收敛。
			if warn != nil {
				warn("生图 URL 下载代理解析失败（回落直连）：" + err.Error())
			}
			return nil
		}
		return client
	}
}

// targetGuardRoundTripper 在代理请求发出前对最终目标主机做本地解析校验
// （方案 A，BUG-0296 残留收敛）。部署级 DialGuard 只校验实际 socket 对端：
// HTTP(S) 代理即代理主机，SOCKS 分支由代理解析、guard 不可达，因此不能替
// 代最终目标校验；生图结果 url 是上游响应派生的地址，其最终目标主机必须
// 单独校验。两条生图下载路径均禁跟随重定向，RoundTripper 看到的 req.URL
// 即最终目标首跳。ValidateHost 是发送前的本地解析预检：socks5h 的实际连
// 接仍由代理解析，预检不消除远端解析偏移窗口（BUG-0296 档案如实登记该残
// 留风险）。
type targetGuardRoundTripper struct {
	base  http.RoundTripper
	guard *upstreamhttp.DialGuard
}

// RoundTrip 先按部署级 URLSecurityConfig 校验 request.URL 指向的最终目标
// 主机（IP 字面量直接区间校验，域名本地解析后校验；allowlist 命中放行），
// 再交给池化 transport 发出请求；校验错误原样透传给下载错误面收敛。
func (t *targetGuardRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return nil, &upstreamhttp.UnsafeResolvedUpstreamURLError{Message: "生图 URL 下载仅支持 http/https 目标地址"}
	}
	port := request.URL.Port()
	if port == "" {
		if request.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if err := t.guard.ValidateHost(request.Context(), request.URL.Hostname(), port); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}

// newTargetGuardProxyClient 装配带最终目标校验的共享代理客户端：借用
// SharedClient 池条目后浅拷贝并替换 Transport 为包装层，不改写池内 client
// 本身（池条目被共享借用方共用）。池 client 的 CheckRedirect 恒为
// http.ErrUseLastResponse（NewClientWithTransport），浅拷贝直接继承两条
// 下载路径"禁跟随重定向"的既有契约。
func newTargetGuardProxyClient(proxyURL *url.URL, guard *upstreamhttp.DialGuard) (*http.Client, error) {
	client, err := upstreamhttp.SharedClient(proxyURL.String(), upstreamhttp.TransportOptions{DialGuard: guard})
	if err != nil {
		return nil, err
	}
	wrapped := *client
	wrapped.Transport = &targetGuardRoundTripper{base: client.Transport, guard: guard}
	return &wrapped, nil
}

func chatImageProxyClientFor(db *sql.DB, bind func(string) string, table func(string) string, secret string, guard *upstreamhttp.DialGuard, accountID string) (*http.Client, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil
	}
	var profileID sql.NullString
	err := db.QueryRow(bind(`SELECT proxy_profile_id FROM `+table("accounts")+`
		WHERE id = ? LIMIT 1`), accountID).Scan(&profileID)
	if errors.Is(err, sql.ErrNoRows) || !profileID.Valid || strings.TrimSpace(profileID.String) == "" {
		// 账户不存在或未绑代理：不构成错误，直连。
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var row struct {
		proxyType sql.NullString
		host      sql.NullString
		port      sql.NullInt64
		username  sql.NullString
		password  sql.NullString
		enabled   sql.NullBool
	}
	err = db.QueryRow(bind(`SELECT type, host, port, username, password_encrypted, enabled
		FROM `+table("proxy_profiles")+`
		WHERE id = ? LIMIT 1`), strings.TrimSpace(profileID.String)).Scan(
		&row.proxyType, &row.host, &row.port, &row.username, &row.password, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("代理配置不存在：" + strings.TrimSpace(profileID.String))
	}
	if err != nil {
		return nil, err
	}
	if !row.enabled.Valid || !row.enabled.Bool {
		return nil, errors.New("代理配置已停用：" + strings.TrimSpace(profileID.String))
	}
	host := strings.TrimSpace(row.host.String)
	if host == "" || !row.port.Valid || row.port.Int64 < 1 || row.port.Int64 > 65535 {
		return nil, errors.New("代理配置 host/port 无效：" + strings.TrimSpace(profileID.String))
	}
	scheme := strings.ToLower(strings.TrimSpace(row.proxyType.String))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return nil, errors.New("代理协议不支持：" + strings.TrimSpace(row.proxyType.String))
	}
	proxyURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.FormatInt(row.port.Int64, 10))}
	if user := strings.TrimSpace(row.username.String); user != "" {
		if strings.TrimSpace(row.password.String) == "" {
			return nil, errors.New("代理配置缺少密码：" + strings.TrimSpace(profileID.String))
		}
		fields := map[string]any{}
		if err := accountscore.DecryptJSON(secret, row.password.String, &fields); err != nil {
			return nil, errors.New("代理密码解密失败：" + strings.TrimSpace(profileID.String))
		}
		plain, _ := fields["password"].(string)
		if strings.TrimSpace(plain) == "" {
			return nil, errors.New("代理密码解密失败：" + strings.TrimSpace(profileID.String))
		}
		proxyURL.User = url.UserPassword(user, plain)
	}
	return newTargetGuardProxyClient(proxyURL, guard)
}
