package main

// xai-grok-usage-refresh 组合根适配器（AI 账户 Grok 用量快照设计 §4，Go 新增
// 任务族，归档 Node 无对应 scheduled job）：为 provider_code='xai' 且
// type='oauth' 的账户周期性调用上游用量端点并落快照到 stats 库
// account_usage_snapshots（kind='xai_grok'，source='xai_grok_billing'）。
//   - 每轮全量扫描候选（xai oauth 账户数少，无 Node 式分页游标与候选租约，
//     进程内重叠由调度器 OverlapCoalesce 吸收）；
//   - 凭据经 V1 AES-GCM 封套解密取 access_token（xai oauth 凭据没有 api_key
//     字段，不做 J2 的 hasAtLeastOneAPIKey 过滤）；
//   - 出站走账户绑定代理（socks5→socks5h 远程 DNS），未绑定直连；
//   - 端点纯读、不标注幂等副作用；token 失效不主动换发（归既有凭据轮换
//     体系 oauth-keepalive-token-refresh / openai-oauth-access-token-refresh），
//     只记 failed；
//   - 单账户失败不影响其他账户，一轮内不重试。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

const (
	// xaiGrokUsageRefreshInterval 与注册表 xai-grok-usage-refresh 的 interval
	// 对齐（next_refresh_after 推进量；设计 §3/§4：10 分钟足够）。
	xaiGrokUsageRefreshInterval = 10 * time.Minute
	// xaiGrokUsageHTTPTimeout 是单端点请求超时（设计 §4：timeout 60s 覆盖
	// billing+settings 两个端点，单请求 15s 对齐 J2 查询超时）。
	xaiGrokUsageHTTPTimeout = 15 * time.Second
	// xaiGrokUsageMaxBodyBytes 是单响应体读取上限（billing/settings 均为
	// 小 JSON，1 MiB 已远超实测规模，只防异常上游无限流）。
	xaiGrokUsageMaxBodyBytes = 1 << 20
	// xaiGrokUsageConcurrency 是账户间并发上限（设计 §4：≤4）。
	xaiGrokUsageConcurrency = 4
	// xaiGrokErrorMessageLimit 是 last_error_message 的 rune 截断上限。
	xaiGrokErrorMessageLimit = 500
)

// xaiGrokSnapshotKind / xaiGrokSnapshotSource 是快照行的 kind/source 固定值
// （maintenance schema 的 kind CHECK 已含 'xai_grok'）。
const (
	xaiGrokSnapshotKind   = "xai_grok"
	xaiGrokSnapshotSource = "xai_grok_billing"
)

// xaiGrokBillingResponse 是 GET <base>/billing?format=credits 的解析形状
// （设计 §2.1 实测样例；字段取舍见 parseXAIGrokBilling）。
type xaiGrokBillingResponse struct {
	Config *xaiGrokBillingConfig `json:"config"`
}

type xaiGrokBillingConfig struct {
	CurrentPeriod      *xaiGrokBillingPeriod `json:"currentPeriod"`
	CreditUsagePercent *float64              `json:"creditUsagePercent"`
	OnDemandUsed       *xaiGrokBillingValue  `json:"onDemandUsed"`
	OnDemandCap        *xaiGrokBillingValue  `json:"onDemandCap"`
	ProductUsage       json.RawMessage       `json:"productUsage"`
	BillingPeriodEnd   string                `json:"billingPeriodEnd"`
}

type xaiGrokBillingPeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type xaiGrokBillingValue struct {
	Val float64 `json:"val"`
}

// xaiGrokBillingParsed 是 billing 响应的派生结果（周期 end 已应用
// billingPeriodEnd 回退，on-demand 备用百分比已计算）。
type xaiGrokBillingParsed struct {
	CreditUsedPercent   *float64
	PeriodType          string
	PeriodStart         string
	PeriodEnd           string
	ProductUsageJSON    string
	OnDemandUsedPercent *float64
}

// parseXAIGrokBilling 解析 billing 响应（纯函数）：
//   - 主用量 config.creditUsagePercent，缺失则快照不写该字段（不算失败）；
//   - 周期 config.currentPeriod.{type,start,end}，end 缺失回退
//     config.billingPeriodEnd（设计 §2.1）；
//   - 分产品 config.productUsage 原样存 JSON 文本（缺失/null 不写）；
//   - onDemandUsed/onDemandCap 两者 val 均 >0 时算 used/cap*100 备用百分比。
func parseXAIGrokBilling(body []byte) (*xaiGrokBillingParsed, error) {
	var response xaiGrokBillingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("billing 响应不是合法 JSON: %w", err)
	}
	if response.Config == nil {
		return nil, errors.New("billing 响应缺少 config 对象")
	}
	config := response.Config
	parsed := &xaiGrokBillingParsed{CreditUsedPercent: config.CreditUsagePercent}
	if config.CurrentPeriod != nil {
		parsed.PeriodType = strings.TrimSpace(config.CurrentPeriod.Type)
		parsed.PeriodStart = strings.TrimSpace(config.CurrentPeriod.Start)
		parsed.PeriodEnd = strings.TrimSpace(config.CurrentPeriod.End)
	}
	if parsed.PeriodEnd == "" {
		parsed.PeriodEnd = strings.TrimSpace(config.BillingPeriodEnd)
	}
	if text := strings.TrimSpace(string(config.ProductUsage)); text != "" && text != "null" {
		parsed.ProductUsageJSON = string(config.ProductUsage)
	}
	if config.OnDemandUsed != nil && config.OnDemandCap != nil &&
		config.OnDemandUsed.Val > 0 && config.OnDemandCap.Val > 0 {
		percent := config.OnDemandUsed.Val / config.OnDemandCap.Val * 100
		parsed.OnDemandUsedPercent = &percent
	}
	return parsed, nil
}

// parseXAIGrokSettingsTier 解析 settings 响应的套餐名字段（设计 §2.2）；
// 缺失或为空返回 ""（快照不写 grok_subscription_tier）。
func parseXAIGrokSettingsTier(body []byte) (string, error) {
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		return "", fmt.Errorf("settings 响应不是合法 JSON: %w", err)
	}
	tier, _ := settings["subscription_tier_display"].(string)
	return strings.TrimSpace(tier), nil
}

// xaiGrokSnapshotPayload 是 account_usage_snapshots.snapshot_json 的持久化
// 形状（grok_ 前缀对齐 codex_* 惯例，设计 §3）；可选字段 omitempty 不写。
type xaiGrokSnapshotPayload struct {
	GrokCreditUsedPercent   *float64 `json:"grok_credit_used_percent,omitempty"`
	GrokPeriodType          string   `json:"grok_period_type,omitempty"`
	GrokPeriodStart         string   `json:"grok_period_start,omitempty"`
	GrokPeriodEnd           string   `json:"grok_period_end,omitempty"`
	GrokSubscriptionTier    string   `json:"grok_subscription_tier,omitempty"`
	GrokProductUsageJSON    string   `json:"grok_product_usage_json,omitempty"`
	GrokOnDemandUsedPercent *float64 `json:"grok_on_demand_used_percent,omitempty"`
}

// buildXAIGrokSnapshotPayload 组装快照 JSON 载荷（纯函数）；subscriptionTier
// 为空时不写套餐名字段。
func buildXAIGrokSnapshotPayload(billing *xaiGrokBillingParsed, subscriptionTier string) (*xaiGrokSnapshotPayload, error) {
	if billing == nil {
		return nil, errors.New("billing 解析结果为空")
	}
	return &xaiGrokSnapshotPayload{
		GrokCreditUsedPercent:   billing.CreditUsedPercent,
		GrokPeriodType:          billing.PeriodType,
		GrokPeriodStart:         billing.PeriodStart,
		GrokPeriodEnd:           billing.PeriodEnd,
		GrokSubscriptionTier:    subscriptionTier,
		GrokProductUsageJSON:    billing.ProductUsageJSON,
		GrokOnDemandUsedPercent: billing.OnDemandUsedPercent,
	}, nil
}

// truncateXAIGrokErrorMessage 把错误文案 rune 截断到上限（异常上游的响应体
// 片段不撑爆 last_error_message）。
func truncateXAIGrokErrorMessage(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= xaiGrokErrorMessageLimit {
		return text
	}
	return string(runes[:xaiGrokErrorMessageLimit])
}

// decryptXAIGrokCredentials 解密凭据：明文 JSON（测试/未启用凭据封套的本地
// 库）直接接受，否则走 V1 AES-GCM 封套（对齐 balanceDetectRuntime 解密顺序）。
func decryptXAIGrokCredentials(secret, envelope string) (map[string]any, error) {
	var credentials map[string]any
	if err := json.Unmarshal([]byte(envelope), &credentials); err == nil {
		return credentials, nil
	}
	plain, err := accountbalance.DecryptV1Envelope(secret, envelope)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(plain, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func credentialText(credentials map[string]any, key string) string {
	if value, ok := credentials[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// xaiGrokUsageRuntime 承载刷新任务族的业务/统计库句柄与出站语境。
type xaiGrokUsageRuntime struct {
	business *businessDB
	statsDB  *sql.DB
	statsPG  bool
	secret   string
	nowFunc  func() time.Time
	logger   *slog.Logger
	// clientFactory 可注入测试客户端工厂；nil 时按账户代理经
	// upstreamhttp.SharedClient 取进程级共享客户端（支持 socks5h）。
	clientFactory func(proxyURL string) (*http.Client, error)
}

func (r *xaiGrokUsageRuntime) clientFor(proxyURL string) (*http.Client, error) {
	if r.clientFactory != nil {
		return r.clientFactory(proxyURL)
	}
	return upstreamhttp.SharedClient(proxyURL, upstreamhttp.TransportOptions{})
}

// xaiGrokUsageAccount 是候选账户行的窄投影。
type xaiGrokUsageAccount struct {
	id                   string
	systemAccountID      string
	credentialsEncrypted string
	proxyProfileID       sql.NullString
}

// listCandidates 全量查询活跃 xai oauth 账户（设计 §4：每轮全量处理）。
// status='active'：disabled/expired 账户的 token 多半已失效，刷新只会白耗
// 出站超时并制造失败噪音；账户重新启用后下一轮自动恢复采集。
func (r *xaiGrokUsageRuntime) listCandidates(ctx context.Context) ([]xaiGrokUsageAccount, error) {
	query := fmt.Sprintf(`
      SELECT id, system_account_id, credentials_encrypted, proxy_profile_id
      FROM %s
      WHERE provider_code = ?
        AND type = 'oauth'
        AND status = 'active'
        AND deleted_at IS NULL
      ORDER BY id ASC
    `, r.business.table("accounts"))
	rows, err := r.business.db.QueryContext(ctx, query, textParam("xai"))
	if err != nil {
		return nil, fmt.Errorf("读取 Grok 用量快照候选失败: %w", err)
	}
	defer rows.Close()
	accounts := []xaiGrokUsageAccount{}
	for rows.Next() {
		var (
			account     xaiGrokUsageAccount
			id, sysID   sql.NullString
			credentials sql.NullString
		)
		if err := rows.Scan(&id, &sysID, &credentials, &account.proxyProfileID); err != nil {
			return nil, err
		}
		account.id = id.String
		account.systemAccountID = sysID.String
		account.credentialsEncrypted = credentials.String
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

// xaiGrokProxyRequestURL 照 oauthrefresh.Store.proxyProfileRequestURL
// （branch for branch）把账户绑定的代理档案解析为出站 URL：空 profileID 直连
// （""）；缺失/停用/host 非法/协议不支持/密码解密失败一律报错且不回落直连。
// 原函数是 oauthrefresh 包私有实现，此处照抄保持 jobs cmd 包内聚；密码封套
// 与 balanceDetectRuntime.proxyEnvelope 同一 V1 解密封装（明文 {"password":
// "..."}）。socks5 升级 socks5h（远程 DNS 解析）。
func (r *xaiGrokUsageRuntime) xaiGrokProxyRequestURL(ctx context.Context, proxyProfileID string) (string, error) {
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
	query := fmt.Sprintf(`SELECT type, host, port, username, password_encrypted, enabled
		FROM %s
		WHERE id = ?
		LIMIT 1`, r.business.table("proxy_profiles"))
	err := r.business.db.QueryRowContext(ctx, query, textParam(proxyProfileID)).Scan(
		&proxyType, &host, &port, &username, &password, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("账户绑定的代理配置不存在：%s", proxyProfileID)
	}
	if err != nil {
		return "", err
	}
	if !enabled.Valid || !enabled.Bool {
		return "", fmt.Errorf("账户绑定的代理配置已停用：%s", proxyProfileID)
	}
	hostText := strings.TrimSpace(host.String)
	portNumber := port.Int64
	if hostText == "" || !port.Valid || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("账户绑定的代理配置 host/port 无效：%s", proxyProfileID)
	}
	scheme := strings.ToLower(strings.TrimSpace(proxyType.String))
	if scheme == "socks5" {
		scheme = "socks5h"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5h" {
		return "", fmt.Errorf("账户绑定的代理协议不支持：%s", strings.TrimSpace(proxyType.String))
	}
	parsed := &url.URL{Scheme: scheme, Host: net.JoinHostPort(hostText, strconv.FormatInt(portNumber, 10))}
	if user := strings.TrimSpace(username.String); user != "" {
		if strings.TrimSpace(password.String) == "" {
			return "", fmt.Errorf("账户绑定的代理配置缺少密码：%s", proxyProfileID)
		}
		plain, err := accountbalance.DecryptV1Envelope(r.secret, password.String)
		if err != nil {
			return "", fmt.Errorf("账户绑定的代理密码解密失败：%s", proxyProfileID)
		}
		var fields map[string]any
		if err := json.Unmarshal(plain, &fields); err != nil {
			return "", fmt.Errorf("账户绑定的代理密码解密失败：%s", proxyProfileID)
		}
		plainText, _ := fields["password"].(string)
		if strings.TrimSpace(plainText) == "" {
			return "", fmt.Errorf("账户绑定的代理密码解密失败：%s", proxyProfileID)
		}
		parsed.User = url.UserPassword(user, plainText)
	}
	return parsed.String(), nil
}

// fetchXAIGrokJSON GET 一个上游用量端点（单请求 15s 超时；请求头对齐
// xai-grok-cli 客户端画像，设计 §2.1）。非 2xx 报错并携带状态码与截断的
// 响应体片段；响应体经 ReadBounded 限读。
func (r *xaiGrokUsageRuntime) fetchXAIGrokJSON(ctx context.Context, client *http.Client, accessToken, endpointURL string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, xaiGrokUsageHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("x-xai-token-auth", "xai-grok-cli")
	request.Header.Set("accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := upstreamhttp.ReadBounded(response.Body, xaiGrokUsageMaxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("读取上游用量响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("上游用量端点返回 HTTP %d：%s",
			response.StatusCode, truncateXAIGrokErrorMessage(string(body)))
	}
	return body, nil
}

// xaiGrokUsageRoundSummary 是一轮刷新的聚合结果。
type xaiGrokUsageRoundSummary struct {
	Scanned int
	OK      int
	Failed  int
}

// runRound 执行一轮全量刷新：查候选 → 并发 ≤4 逐账户刷新 → 聚合。
// 候选查询失败返回错误交调度退避；单账户失败只计数不影响其他账户。
func (r *xaiGrokUsageRuntime) runRound(ctx context.Context) (xaiGrokUsageRoundSummary, error) {
	accounts, err := r.listCandidates(ctx)
	if err != nil {
		return xaiGrokUsageRoundSummary{}, err
	}
	summary := xaiGrokUsageRoundSummary{Scanned: len(accounts)}
	if len(accounts) == 0 {
		return summary, nil
	}
	semaphore := make(chan struct{}, xaiGrokUsageConcurrency)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for index := range accounts {
		if ctx.Err() != nil {
			break
		}
		account := accounts[index]
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-semaphore }()
			ok, refreshErr := r.refreshAccount(ctx, account)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				summary.OK++
				return
			}
			summary.Failed++
			if r.logger != nil {
				r.logger.Warn("Grok OAuth 用量快照刷新失败",
					"event", "xai_grok_usage_refresh_account_failed",
					"account_id", account.id,
					"error", refreshErr)
			}
		}()
	}
	wg.Wait()
	return summary, nil
}

// refreshAccount 刷新单账户：任何失败先落 failed 状态机再原样返回错误
// （ok=false）；成功落 ok 快照（ok=true）。快照写入自身失败按账户失败上报。
func (r *xaiGrokUsageRuntime) refreshAccount(ctx context.Context, account xaiGrokUsageAccount) (bool, error) {
	payload, refreshErr := r.attemptAccount(ctx, account)
	if refreshErr == nil {
		if upsertErr := r.upsertSuccess(ctx, account, payload); upsertErr != nil {
			return false, upsertErr
		}
		return true, nil
	}
	if upsertErr := r.upsertFailure(ctx, account, truncateXAIGrokErrorMessage(refreshErr.Error())); upsertErr != nil {
		return false, fmt.Errorf("%v（快照失败状态写入也失败: %w）", refreshErr, upsertErr)
	}
	return false, refreshErr
}

// attemptAccount 取凭据 → 代理解析 → billing + settings → 解析为快照载荷。
func (r *xaiGrokUsageRuntime) attemptAccount(ctx context.Context, account xaiGrokUsageAccount) (*xaiGrokSnapshotPayload, error) {
	credentials, err := decryptXAIGrokCredentials(r.secret, account.credentialsEncrypted)
	if err != nil {
		return nil, fmt.Errorf("凭据解密失败: %w", err)
	}
	accessToken := credentialText(credentials, "access_token")
	if accessToken == "" {
		return nil, errors.New("凭据缺少 access_token")
	}
	baseURL := strings.TrimRight(credentialText(credentials, "base_url"), "/")
	if baseURL == "" {
		return nil, errors.New("凭据缺少 base_url")
	}
	proxyURL, err := r.xaiGrokProxyRequestURL(ctx, account.proxyProfileID.String)
	if err != nil {
		return nil, err
	}
	client, err := r.clientFor(proxyURL)
	if err != nil {
		return nil, err
	}
	billingBody, err := r.fetchXAIGrokJSON(ctx, client, accessToken, baseURL+"/billing?format=credits")
	if err != nil {
		return nil, fmt.Errorf("billing 查询失败: %w", err)
	}
	billing, err := parseXAIGrokBilling(billingBody)
	if err != nil {
		return nil, err
	}
	// settings 只取套餐名展示字段（设计 §2.2）：获取/解析失败不推翻 billing
	// 快照，按缺失处理（不写 grok_subscription_tier），warn 留痕。
	subscriptionTier := ""
	settingsBody, settingsErr := r.fetchXAIGrokJSON(ctx, client, accessToken, baseURL+"/settings")
	if settingsErr != nil {
		r.warnSettingsUnavailable(account, settingsErr)
	} else {
		subscriptionTier, err = parseXAIGrokSettingsTier(settingsBody)
		if err != nil {
			r.warnSettingsUnavailable(account, err)
			subscriptionTier = ""
		}
	}
	return buildXAIGrokSnapshotPayload(billing, subscriptionTier)
}

func (r *xaiGrokUsageRuntime) warnSettingsUnavailable(account xaiGrokUsageAccount, err error) {
	if r.logger == nil {
		return
	}
	r.logger.Warn("Grok OAuth settings 查询不可用，快照不写套餐名",
		"event", "xai_grok_usage_settings_unavailable",
		"account_id", account.id,
		"error", err)
}

// upsertSuccess 写入 ok 快照：snapshot_json/refresh_status/last_success_at
// 全量替换并清空 last_error_message；ON CONFLICT 不更新 created_at。
func (r *xaiGrokUsageRuntime) upsertSuccess(ctx context.Context, account xaiGrokUsageAccount, payload *xaiGrokSnapshotPayload) error {
	serialized, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := r.nowFunc().UTC()
	upsert := fmt.Sprintf(`
    INSERT INTO %s (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, '%s', '%s', ?, 'ok', ?, ?, ?, NULL, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      source = excluded.source,
      snapshot_json = excluded.snapshot_json,
      refresh_status = excluded.refresh_status,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      next_refresh_after = excluded.next_refresh_after,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `, statsTable(r.statsPG, "account_usage_snapshots"), xaiGrokSnapshotKind, xaiGrokSnapshotSource)
	_, err = r.statsDB.ExecContext(ctx, upsert,
		textParam(account.systemAccountID), textParam(account.id), textParam(string(serialized)),
		timeParam(r.statsPG, now), timeParam(r.statsPG, now), timeParam(r.statsPG, now.Add(xaiGrokUsageRefreshInterval)),
		timeParam(r.statsPG, now), timeParam(r.statsPG, now))
	if err != nil {
		return fmt.Errorf("写入 Grok 用量快照失败: %w", err)
	}
	return nil
}

// upsertFailure 写入 failed 状态机（设计 §4：任何失败 refresh_status='failed'
// +last_error_message，两种情况都写 last_attempt_at/next_refresh_after/
// updated_at）。已有行不触碰 snapshot_json/last_success_at/source——保留最近
// 一次成功 payload 供前端展示；无历史行（首次即失败）落 '{}' 占位
// （snapshot_json 列 NOT NULL，不能写 NULL）。
func (r *xaiGrokUsageRuntime) upsertFailure(ctx context.Context, account xaiGrokUsageAccount, errorMessage string) error {
	now := r.nowFunc().UTC()
	upsert := fmt.Sprintf(`
    INSERT INTO %s (
      system_account_id, account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
    ) VALUES (?, ?, '%s', '%s', '{}', 'failed', ?, NULL, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
      refresh_status = excluded.refresh_status,
      last_attempt_at = excluded.last_attempt_at,
      next_refresh_after = excluded.next_refresh_after,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `, statsTable(r.statsPG, "account_usage_snapshots"), xaiGrokSnapshotKind, xaiGrokSnapshotSource)
	_, err := r.statsDB.ExecContext(ctx, upsert,
		textParam(account.systemAccountID), textParam(account.id),
		timeParam(r.statsPG, now), timeParam(r.statsPG, now.Add(xaiGrokUsageRefreshInterval)),
		textParam(errorMessage), timeParam(r.statsPG, now), timeParam(r.statsPG, now))
	if err != nil {
		return fmt.Errorf("写入 Grok 用量快照失败状态失败: %w", err)
	}
	return nil
}

// wireXAIGrokUsageFamily 装配 xai-grok-usage-refresh。任一依赖缺失→登记
// disabled 并说明（不阻塞其他 job）。
func (a *workerAssembly) wireXAIGrokUsageFamily(ctx context.Context) error {
	name := "xai-grok-usage-refresh"
	if a.config.Driver != "postgres" && a.config.StatsSQLitePath == "" {
		a.registerDisabledJob(name, "缺 JUHE_AI_STATS_DATABASE_PATH（xai_grok 快照库）")
		return nil
	}
	if a.config.Secret == "" {
		a.registerDisabledJob(name, "缺 JUHE_AI_SECRET（凭据封套解密不可用）")
		return nil
	}
	business, err := openBusinessDB(a, "xai-grok-usage-business")
	if err != nil {
		return err
	}
	statsPG := a.config.Driver == "postgres"
	statsDB := business.db
	if !statsPG {
		if statsDB, err = a.openSQLite(a.config.StatsSQLitePath, "xai-grok-usage-stats"); err != nil {
			_ = business.close()
			return err
		}
	}
	if err := ensureAccountUsageSnapshotsTable(ctx, statsDB, statsPG); err != nil {
		a.registerDisabledJob(name, "统计库快照表校验失败："+err.Error())
		_ = business.close()
		return nil
	}
	runtime := &xaiGrokUsageRuntime{
		business: business,
		statsDB:  statsDB,
		statsPG:  statsPG,
		secret:   a.config.Secret,
		nowFunc:  func() time.Time { return time.Now().UTC() },
		logger:   a.logger,
	}
	a.addCloser(business.close)
	a.scheduleWiredJob(name, func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		summary, runErr := runtime.runRound(taskCtx)
		if runErr != nil {
			return jobsched.TaskResult{}, runErr
		}
		a.logger.Info("Grok OAuth 用量快照刷新完成",
			"event", "xai_grok_usage_refresh_completed",
			"scanned", summary.Scanned,
			"ok", summary.OK,
			"failed", summary.Failed)
		return jobsched.TaskResult{}, nil
	})
	return nil
}
