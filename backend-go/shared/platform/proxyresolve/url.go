package proxyresolve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// BuildProfileURL renders the existing proxy URL contract: socks5 is upgraded
// to socks5h, http/https stay as-is, credentials are URI-encoded, and only a
// non-empty username carries auth. Callers inject the envelope decrypter.
func BuildProfileURL(ctx context.Context, profile Profile, decrypter PasswordDecrypter) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(profile.Type))
	switch protocol {
	case "socks5", "socks5h":
		protocol = "socks5h"
	case "http", "https":
	default:
		return "", fmt.Errorf("不支持的代理协议 %q", profile.Type)
	}
	host := strings.TrimSpace(profile.Host)
	if host == "" {
		return "", errors.New("代理 host 为空")
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return "", fmt.Errorf("代理端口非法: %d", profile.Port)
	}
	password := ""
	if strings.TrimSpace(profile.PasswordEncrypted) != "" {
		if decrypter == nil {
			return "", errors.New("代理密码解密器未配置")
		}
		decrypted, err := decrypter.DecryptProxyPassword(ctx, profile.PasswordEncrypted)
		if err != nil {
			return "", fmt.Errorf("代理密码不可解密: %w", err)
		}
		password = decrypted
	}
	var user *url.Userinfo
	if strings.TrimSpace(profile.Username) != "" {
		if password != "" {
			user = url.UserPassword(profile.Username, password)
		} else {
			user = url.User(profile.Username)
		}
	}
	parsed := &url.URL{
		Scheme: protocol,
		Host:   net.JoinHostPort(host, strconv.Itoa(profile.Port)),
		User:   user,
	}
	return parsed.String(), nil
}
