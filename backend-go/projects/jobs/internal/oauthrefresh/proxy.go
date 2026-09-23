package oauthrefresh

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// proxyProfileRequestURL mirrors gateway oauthmgmt proxyProfileRequestURL
// (backend-go/projects/gateway/internal/oauthmgmt/proxy.go, branch for branch):
// resolve an account-bound proxy profile into the outbound URL for token
// requests. An empty profileID returns ("", nil) (unbound — keep the existing
// direct/environment behaviour); a missing or disabled profile, an invalid
// host/port, an unsupported scheme or an undecryptable password returns an
// error and never falls back to a direct dial.
func (s *Store) proxyProfileRequestURL(ctx context.Context, proxyProfileID string) (string, error) {
	ctx = ensureCtx(ctx)
	proxyProfileID = strings.TrimSpace(proxyProfileID)
	if proxyProfileID == "" {
		return "", nil
	}
	var (
		proxyType sql.NullString
		host      sql.NullString
		port      sql.NullInt64
		username  sql.NullString
		password  sql.NullString
		enabled   sql.NullBool
	)
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT type, host, port, username, password_encrypted, enabled
		FROM `+s.table("proxy_profiles")+`
		WHERE id = ?
		LIMIT 1`), proxyProfileID).Scan(
		&proxyType, &host, &port, &username, &password, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", proxyLocalConfigurationError("账户绑定的代理配置不存在：" + proxyProfileID)
	}
	if err != nil {
		return "", err
	}
	if !enabled.Valid || !enabled.Bool {
		return "", proxyLocalConfigurationError("账户绑定的代理配置已停用：" + proxyProfileID)
	}
	hostText := strings.TrimSpace(host.String)
	portNumber := port.Int64
	if hostText == "" || !port.Valid || portNumber < 1 || portNumber > 65535 {
		return "", proxyLocalConfigurationError("账户绑定的代理配置 host/port 无效：" + proxyProfileID)
	}
	// URL construction matches the gateway: lowercase scheme, socks5 upgraded
	// to socks5h (remote DNS resolution), only http/https/socks5h allowed.
	scheme := strings.ToLower(strings.TrimSpace(proxyType.String))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return "", proxyLocalConfigurationError("账户绑定的代理协议不支持：" + strings.TrimSpace(proxyType.String))
	}
	parsed := &url.URL{Scheme: scheme, Host: net.JoinHostPort(hostText, strconv.FormatInt(portNumber, 10))}
	if user := strings.TrimSpace(username.String); user != "" {
		if strings.TrimSpace(password.String) == "" {
			return "", proxyLocalConfigurationError("账户绑定的代理配置缺少密码：" + proxyProfileID)
		}
		// The password envelope rides the same v1 AES-GCM credentials system
		// (DecryptJSON) as every other sealed secret; the plaintext is
		// {"password": "..."}.
		fields := map[string]any{}
		if err := DecryptJSON(s.secret, password.String, &fields); err != nil {
			return "", proxyLocalConfigurationError("账户绑定的代理密码解密失败：" + proxyProfileID)
		}
		plain, _ := fields["password"].(string)
		if strings.TrimSpace(plain) == "" {
			return "", proxyLocalConfigurationError("账户绑定的代理密码解密失败：" + proxyProfileID)
		}
		parsed.User = url.UserPassword(user, plain)
	}
	return parsed.String(), nil
}

// proxyLocalConfigurationError wraps a proxy-resolution defect as the local
// configuration failure kind: both refresh families classify it as expected
// local evidence (never an upstream failure) and neither dials direct.
func proxyLocalConfigurationError(message string) *LocalConfigurationError {
	return &LocalConfigurationError{Message: message}
}
