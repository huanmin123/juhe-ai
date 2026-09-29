package main

// 生图 URL 下载代理端口装配（BUG-0232 关联事实的落地）：上游
// /v1/images/edits（grok-imagine）只返回 imgen.x.ai 临时图片链接，国内
// 单机直连不可达——按生图绑定账户的 proxy_profile 构造出站 HTTP 客户端。
// 未绑定代理、代理停用或解析失败一律返回 nil（直连默认），代理配置问题不
// 升级为生图失败（与 buildProxyClient / proxyProfileRequestURL 同一构造范
// 式：scheme 小写化、socks5 升级 socks5h、密码走 v1 AES-GCM 信封）。

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
// 绑定变更，无缓存失效面）。
func newChatImageDownloadProxy(db *sql.DB, postgres bool, secret string, warn func(message string)) func(accountID string) *http.Client {
	bind := func(query string) string { return accountscore.SQLBind(postgres, query) }
	table := func(name string) string {
		if postgres {
			return "juhe_business." + name
		}
		return name
	}
	return func(accountID string) *http.Client {
		client, err := chatImageProxyClientFor(db, bind, table, secret, accountID)
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

func chatImageProxyClientFor(db *sql.DB, bind func(string) string, table func(string) string, secret, accountID string) (*http.Client, error) {
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
	client, err := upstreamhttp.SharedClient(proxyURL.String(), upstreamhttp.TransportOptions{})
	if err != nil {
		return nil, err
	}
	return client, nil
}
