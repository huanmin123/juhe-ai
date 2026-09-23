package oauthmgmt

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// proxyProfileRequestURL mirrors Node resolveRefreshProxyUrlOrThrow: 把账户绑定
// 的代理配置解析为 token 请求出站 URL。profileID 为空返回 ""（未绑定，保持
// 既有直连/环境变量行为）；配置不存在、已停用、host/port 非法、协议不支持或
// 密码解密失败时返回 ValidationError（路由层渲染为 400，对应 Node
// oauth_proxy_configuration_invalid 家族）。
func (s *Store) proxyProfileRequestURL(ctx context.Context, proxyProfileID string) (string, error) {
	proxyProfileID = strings.TrimSpace(proxyProfileID)
	if proxyProfileID == "" {
		return "", nil
	}
	var row struct {
		proxyType sql.NullString
		host      sql.NullString
		port      sql.NullInt64
		username  sql.NullString
		password  sql.NullString
		enabled   sql.NullBool
	}
	err := s.db.QueryRowContext(ensureContext(ctx), s.bind(`SELECT type, host, port, username, password_encrypted, enabled
		FROM `+s.table("proxy_profiles")+`
		WHERE id = ?
		LIMIT 1`), proxyProfileID).Scan(
		&row.proxyType, &row.host, &row.port, &row.username, &row.password, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &ValidationError{Message: "账户绑定的代理配置不存在：" + proxyProfileID}
	}
	if err != nil {
		return "", err
	}
	if !row.enabled.Valid || !row.enabled.Bool {
		return "", &ValidationError{Message: "账户绑定的代理配置已停用：" + proxyProfileID}
	}
	host := strings.TrimSpace(row.host.String)
	port := row.port.Int64
	if host == "" || !row.port.Valid || port < 1 || port > 65535 {
		return "", &ValidationError{Message: "账户绑定的代理配置 host/port 无效：" + proxyProfileID}
	}
	// URL 构造与 modelcheckowner buildProxyClient 同一范式：scheme 小写化、
	// socks5 升级为 socks5h（远端解析）、仅允许 http/https/socks5h。
	scheme := strings.ToLower(strings.TrimSpace(row.proxyType.String))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return "", &ValidationError{Message: "账户绑定的代理协议不支持：" + strings.TrimSpace(row.proxyType.String)}
	}
	proxyURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.FormatInt(port, 10))}
	if user := strings.TrimSpace(row.username.String); user != "" {
		if strings.TrimSpace(row.password.String) == "" {
			return "", &ValidationError{Message: "账户绑定的代理配置缺少密码：" + proxyProfileID}
		}
		// 密码 envelope 与 credentials_encrypted 同一 v1 AES-GCM 体系
		// （decryptJSON == Node decryptJson），明文为 {"password": "..."}。
		fields := map[string]any{}
		if err := decryptJSON(s.secret, row.password.String, &fields); err != nil {
			return "", &ValidationError{Message: "账户绑定的代理密码解密失败：" + proxyProfileID}
		}
		plain, _ := fields["password"].(string)
		if strings.TrimSpace(plain) == "" {
			return "", &ValidationError{Message: "账户绑定的代理密码解密失败：" + proxyProfileID}
		}
		proxyURL.User = url.UserPassword(user, plain)
	}
	return proxyURL.String(), nil
}
