package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// xaiGrokFixtureNow 是刷新运行态测试的固定时钟（next_refresh_after 断言用）。
var xaiGrokFixtureNow = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func float64Ptr(value float64) *float64 { return &value }

// TestParseXAIGrokBilling 表驱动 billing 响应解析（设计 §2.1 字段取舍）。
func TestParseXAIGrokBilling(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    *xaiGrokBillingParsed
		wantErr bool
	}{
		{
			name: "完整响应",
			body: `{"config":{
				"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00","end":"2026-09-28T02:34:03.052517+00:00"},
				"creditUsagePercent":14.0,
				"onDemandCap":{"val":40},"onDemandUsed":{"val":10},
				"productUsage":[{"product":"GrokBuild","usagePercent":13.0},{"product":"GrokChat","usagePercent":1.0},{"product":"GrokImagine"}],
				"isUnifiedBillingUser":true,
				"billingPeriodStart":"2026-09-21T02:34:03.052517+00:00","billingPeriodEnd":"2026-09-28T02:34:03.052517+00:00"
			}}`,
			want: &xaiGrokBillingParsed{
				CreditUsedPercent:   float64Ptr(14.0),
				PeriodType:          "USAGE_PERIOD_TYPE_WEEKLY",
				PeriodStart:         "2026-09-21T02:34:03.052517+00:00",
				PeriodEnd:           "2026-09-28T02:34:03.052517+00:00",
				ProductUsageJSON:    `[{"product":"GrokBuild","usagePercent":13.0},{"product":"GrokChat","usagePercent":1.0},{"product":"GrokImagine"}]`,
				OnDemandUsedPercent: float64Ptr(25),
			},
		},
		{
			name: "缺 creditUsagePercent 时其余字段保留且不写主用量",
			body: `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","start":"2026-09-01T00:00:00+00:00","end":"2026-10-01T00:00:00+00:00"}}}`,
			want: &xaiGrokBillingParsed{
				PeriodType:  "USAGE_PERIOD_TYPE_MONTHLY",
				PeriodStart: "2026-09-01T00:00:00+00:00",
				PeriodEnd:   "2026-10-01T00:00:00+00:00",
			},
		},
		{
			name: "currentPeriod.end 缺失回退 billingPeriodEnd",
			body: `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00"},"billingPeriodEnd":"2026-09-28T02:34:03.052517+00:00"}}`,
			want: &xaiGrokBillingParsed{
				PeriodType:  "USAGE_PERIOD_TYPE_WEEKLY",
				PeriodStart: "2026-09-21T02:34:03.052517+00:00",
				PeriodEnd:   "2026-09-28T02:34:03.052517+00:00",
			},
		},
		{
			name: "onDemand 任一 val 为 0 不算备用百分比",
			body: `{"config":{"creditUsagePercent":7,"onDemandCap":{"val":0},"onDemandUsed":{"val":0}}}`,
			want: &xaiGrokBillingParsed{CreditUsedPercent: float64Ptr(7)},
		},
		{
			name: "productUsage 为 null 不写",
			body: `{"config":{"creditUsagePercent":1,"productUsage":null}}`,
			want: &xaiGrokBillingParsed{CreditUsedPercent: float64Ptr(1)},
		},
		{
			name:    "非法 JSON 报错",
			body:    `not-json`,
			wantErr: true,
		},
		{
			name:    "缺少 config 对象报错",
			body:    `{"error":"unexpected"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := parseXAIGrokBilling([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("必须报错，得到 %+v", parsed)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if parsed.CreditUsedPercent == nil != (tt.want.CreditUsedPercent == nil) {
				t.Fatalf("CreditUsedPercent 存在性不符: %+v", parsed)
			}
			if tt.want.CreditUsedPercent != nil && *parsed.CreditUsedPercent != *tt.want.CreditUsedPercent {
				t.Fatalf("CreditUsedPercent = %v want %v", *parsed.CreditUsedPercent, *tt.want.CreditUsedPercent)
			}
			if parsed.OnDemandUsedPercent == nil != (tt.want.OnDemandUsedPercent == nil) {
				t.Fatalf("OnDemandUsedPercent 存在性不符: %+v", parsed)
			}
			if tt.want.OnDemandUsedPercent != nil && *parsed.OnDemandUsedPercent != *tt.want.OnDemandUsedPercent {
				t.Fatalf("OnDemandUsedPercent = %v want %v", *parsed.OnDemandUsedPercent, *tt.want.OnDemandUsedPercent)
			}
			if parsed.PeriodType != tt.want.PeriodType || parsed.PeriodStart != tt.want.PeriodStart || parsed.PeriodEnd != tt.want.PeriodEnd {
				t.Fatalf("周期字段不符: %+v want %+v", parsed, tt.want)
			}
			if parsed.ProductUsageJSON != tt.want.ProductUsageJSON {
				t.Fatalf("ProductUsageJSON = %q want %q", parsed.ProductUsageJSON, tt.want.ProductUsageJSON)
			}
		})
	}
}

// TestParseXAIGrokSettingsTier 表驱动 settings 套餐名解析（设计 §2.2）。
func TestParseXAIGrokSettingsTier(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "实测套餐名", body: `{"subscription_tier_display":"SuperGrok Heavy"}`, want: "SuperGrok Heavy"},
		{name: "缺字段", body: `{"theme":"dark"}`, want: ""},
		{name: "空串与空白归一", body: `{"subscription_tier_display":"  "}`, want: ""},
		{name: "非字符串类型忽略", body: `{"subscription_tier_display":3}`, want: ""},
		{name: "非法 JSON 报错", body: `{`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier, err := parseXAIGrokSettingsTier([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("必须报错，得到 %q", tier)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if tier != tt.want {
				t.Fatalf("tier = %q want %q", tier, tt.want)
			}
		})
	}
}

// TestBuildXAIGrokSnapshotPayload 锁定 snapshot_json 的 grok_ 前缀键形状与
// 可选字段省略规则（设计 §3）。
func TestBuildXAIGrokSnapshotPayload(t *testing.T) {
	billing, err := parseXAIGrokBilling([]byte(`{"config":{
		"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00","end":"2026-09-28T02:34:03.052517+00:00"},
		"creditUsagePercent":14.0,
		"onDemandCap":{"val":40},"onDemandUsed":{"val":10},
		"productUsage":[{"product":"GrokBuild","usagePercent":13.0}]
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := buildXAIGrokSnapshotPayload(billing, "SuperGrok Heavy")
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(serialized, &decoded); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"grok_credit_used_percent", "grok_period_type", "grok_period_start", "grok_period_end",
		"grok_subscription_tier", "grok_product_usage_json", "grok_on_demand_used_percent",
	}
	for _, key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("快照 JSON 缺少键 %s: %s", key, serialized)
		}
	}
	if decoded["grok_credit_used_percent"] != 14.0 {
		t.Fatalf("grok_credit_used_percent = %v", decoded["grok_credit_used_percent"])
	}
	if decoded["grok_product_usage_json"] != `[{"product":"GrokBuild","usagePercent":13.0}]` {
		t.Fatalf("grok_product_usage_json 必须是原样 JSON 字符串: %v", decoded["grok_product_usage_json"])
	}
	if decoded["grok_on_demand_used_percent"] != 25.0 {
		t.Fatalf("grok_on_demand_used_percent = %v", decoded["grok_on_demand_used_percent"])
	}

	// 可选字段缺省：无 tier / 无备用百分比 / 无 productUsage 时键整体省略。
	lean := &xaiGrokBillingParsed{CreditUsedPercent: float64Ptr(3)}
	leanPayload, err := buildXAIGrokSnapshotPayload(lean, "")
	if err != nil {
		t.Fatal(err)
	}
	leanSerialized, err := json.Marshal(leanPayload)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"grok_subscription_tier", "grok_period_type", "grok_product_usage_json", "grok_on_demand_used_percent", "grok_period_start", "grok_period_end"} {
		if strings.Contains(string(leanSerialized), key) {
			t.Fatalf("可选键 %s 在缺省时必须省略: %s", key, leanSerialized)
		}
	}
	if !strings.Contains(string(leanSerialized), `"grok_credit_used_percent":3`) {
		t.Fatalf("主用量键必须保留: %s", leanSerialized)
	}
	if _, err := buildXAIGrokSnapshotPayload(nil, ""); err == nil {
		t.Fatal("nil billing 必须报错")
	}
}

// TestTruncateXAIGrokErrorMessage 锁定 rune 截断语义。
func TestTruncateXAIGrokErrorMessage(t *testing.T) {
	short := "HTTP 500：boom"
	if got := truncateXAIGrokErrorMessage(short); got != short {
		t.Fatalf("短文案不得截断: %q", got)
	}
	long := strings.Repeat("错", xaiGrokErrorMessageLimit+50)
	got := truncateXAIGrokErrorMessage(long)
	if runeCount := len([]rune(got)); runeCount != xaiGrokErrorMessageLimit {
		t.Fatalf("截断后 rune 数 = %d want %d", runeCount, xaiGrokErrorMessageLimit)
	}
}

// seedXAIGrokUsageAccount 写入一个 xai oauth 候选账户（credentials 可为封套
// 或任意原文，覆盖解密失败分支）。
func seedXAIGrokUsageAccount(t *testing.T, ctx context.Context, db *sql.DB, secret, credentialsJSON string) {
	t.Helper()
	envelope := credentialsJSON
	if credentialsJSON != "not-an-envelope" {
		sealed, err := accountbalance.EncryptV1Envelope(secret, []byte(credentialsJSON))
		if err != nil {
			t.Fatal(err)
		}
		envelope = sealed
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO accounts (id, system_account_id, provider_code, type, status, credentials_encrypted, updated_at)
VALUES ('acc-grok-1', 'sys-1', 'xai', 'oauth', 'active', ?, ?)
`, envelope, xaiGrokFixtureNow.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

// newXaiGrokTestRuntime 组装指向已建 schema 的 SQLite 业务/统计库的运行态。
func newXaiGrokTestRuntime(t *testing.T, biz, stats *sql.DB, secret string) *xaiGrokUsageRuntime {
	t.Helper()
	return &xaiGrokUsageRuntime{
		business: &businessDB{db: biz, postgres: false},
		statsDB:  stats,
		statsPG:  false,
		secret:   secret,
		nowFunc:  func() time.Time { return xaiGrokFixtureNow },
	}
}

// readXAIGrokSnapshotRow 读回快照行做断言。
func readXAIGrokSnapshotRow(t *testing.T, ctx context.Context, statsDB *sql.DB) (snapshotJSON, refreshStatus, lastAttempt, lastSuccess, nextRefresh, lastError sql.NullString) {
	t.Helper()
	err := statsDB.QueryRowContext(ctx, `
SELECT snapshot_json, refresh_status, last_attempt_at, last_success_at, next_refresh_after, last_error_message
FROM account_usage_snapshots WHERE kind = 'xai_grok' AND account_id = 'acc-grok-1'
`).Scan(&snapshotJSON, &refreshStatus, &lastAttempt, &lastSuccess, &nextRefresh, &lastError)
	if err != nil {
		t.Fatalf("读取 xai_grok 快照行失败: %v", err)
	}
	return
}

// TestXAIGrokUsageRuntimeRoundHappyPath：fake 上游命中 billing+settings →
// 一轮成功落 ok 快照，请求头与 payload 字段逐项断言；重复一轮保持单行
// （UPSERT 幂等）。
func TestXAIGrokUsageRuntimeRoundHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	root := t.TempDir()
	businessDB := mustOpenSQLite(t, filepath.Join(root, "business.sqlite3"))
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, filepath.Join(root, "stats.sqlite3"))
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"

	var gotAuth, gotTokenAuth, gotAccept string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTokenAuth = r.Header.Get("x-xai-token-auth")
		gotAccept = r.Header.Get("accept")
		switch r.URL.Path {
		case "/v1/billing":
			if r.URL.RawQuery != "format=credits" {
				t.Errorf("billing 查询串 = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"config":{
				"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00","end":"2026-09-28T02:34:03.052517+00:00"},
				"creditUsagePercent":14.0,
				"onDemandCap":{"val":0},"onDemandUsed":{"val":0},
				"productUsage":[{"product":"GrokBuild","usagePercent":13.0},{"product":"GrokChat","usagePercent":1.0}]
			}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{"subscription_tier_display":"SuperGrok Heavy"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	seedXAIGrokUsageAccount(t, ctx, businessDB, secret,
		`{"access_token":"tok-xai","base_url":"`+upstream.URL+`/v1"}`)

	runtime := newXaiGrokTestRuntime(t, businessDB, statsDB, secret)
	summary, err := runtime.runRound(ctx)
	if err != nil {
		t.Fatalf("runRound: %v", err)
	}
	if summary.Scanned != 1 || summary.OK != 1 || summary.Failed != 0 {
		t.Fatalf("轮次聚合不符: %+v", summary)
	}
	if gotAuth != "Bearer tok-xai" || gotTokenAuth != "xai-grok-cli" || gotAccept != "application/json" {
		t.Fatalf("上游请求头不符: auth=%q tokenAuth=%q accept=%q", gotAuth, gotTokenAuth, gotAccept)
	}

	snapshotJSON, refreshStatus, lastAttempt, lastSuccess, nextRefresh, lastError :=
		readXAIGrokSnapshotRow(t, ctx, statsDB)
	if refreshStatus.String != "ok" {
		t.Fatalf("refresh_status = %q", refreshStatus.String)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON.String), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["grok_credit_used_percent"] != 14.0 {
		t.Fatalf("grok_credit_used_percent = %v", payload["grok_credit_used_percent"])
	}
	if payload["grok_period_type"] != "USAGE_PERIOD_TYPE_WEEKLY" || payload["grok_period_end"] != "2026-09-28T02:34:03.052517+00:00" {
		t.Fatalf("周期字段不符: %v", payload)
	}
	if payload["grok_subscription_tier"] != "SuperGrok Heavy" {
		t.Fatalf("grok_subscription_tier = %v", payload["grok_subscription_tier"])
	}
	if _, hasOnDemand := payload["grok_on_demand_used_percent"]; hasOnDemand {
		t.Fatal("onDemand val 全 0 时不得写备用百分比")
	}
	if lastAttempt.String != xaiGrokFixtureNow.Format(time.RFC3339Nano) ||
		lastSuccess.String != xaiGrokFixtureNow.Format(time.RFC3339Nano) {
		t.Fatalf("attempt/success 时间不符: %q %q", lastAttempt.String, lastSuccess.String)
	}
	wantNext := xaiGrokFixtureNow.Add(xaiGrokUsageRefreshInterval).Format(time.RFC3339Nano)
	if nextRefresh.String != wantNext {
		t.Fatalf("next_refresh_after = %q want %q", nextRefresh.String, wantNext)
	}
	if lastError.Valid && lastError.String != "" {
		t.Fatalf("成功轮必须清空 last_error_message: %q", lastError.String)
	}

	// 第二轮：UPSERT 幂等（仍单行）。
	summary, err = runtime.runRound(ctx)
	if err != nil {
		t.Fatalf("第二轮 runRound: %v", err)
	}
	if summary.OK != 1 {
		t.Fatalf("第二轮聚合不符: %+v", summary)
	}
	var rowCount int
	if err := statsDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_usage_snapshots WHERE kind = 'xai_grok'`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("UPSERT 后必须仍为单行，得到 %d", rowCount)
	}
}

// TestXAIGrokUsageRuntimeFailures 表驱动失败路径：失败都落 failed 状态机，
// 且单账户失败不影响轮次成功返回。
func TestXAIGrokUsageRuntimeFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	tests := []struct {
		name           string
		credentials    string
		billingBody    string
		billingStatus  int
		settingsStatus int
		wantErrParts   []string
	}{
		{
			name:          "billing HTTP 500",
			credentials:   `{"access_token":"tok-xai","base_url":"__UPSTREAM__/v1"}`,
			billingStatus: 500,
			wantErrParts:  []string{"billing 查询失败", "HTTP 500"},
		},
		{
			name:         "凭据解密失败",
			credentials:  "not-an-envelope",
			wantErrParts: []string{"凭据解密失败"},
		},
		{
			name:           "settings HTTP 500 不推翻 billing 快照",
			credentials:    `{"access_token":"tok-xai","base_url":"__UPSTREAM__/v1"}`,
			settingsStatus: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			businessDB := mustOpenSQLite(t, filepath.Join(root, "business.sqlite3"))
			defer businessDB.Close()
			statsDB := mustOpenSQLite(t, filepath.Join(root, "stats.sqlite3"))
			defer statsDB.Close()
			if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
				t.Fatal(err)
			}
			secret := "0123456789abcdef0123456789abcdef"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/billing":
					if tt.billingStatus != 0 {
						w.WriteHeader(tt.billingStatus)
						_, _ = w.Write([]byte("upstream exploded"))
						return
					}
					_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":14.0,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00","end":"2026-09-28T02:34:03.052517+00:00"}}}`))
				case "/v1/settings":
					if tt.settingsStatus != 0 {
						w.WriteHeader(tt.settingsStatus)
						_, _ = w.Write([]byte("settings exploded"))
						return
					}
					_, _ = w.Write([]byte(`{"subscription_tier_display":"SuperGrok Heavy"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			credentials := strings.ReplaceAll(tt.credentials, "__UPSTREAM__", upstream.URL)
			seedXAIGrokUsageAccount(t, ctx, businessDB, secret, credentials)

			runtime := newXaiGrokTestRuntime(t, businessDB, statsDB, secret)
			summary, err := runtime.runRound(ctx)
			if err != nil {
				t.Fatalf("单账户失败不得使轮次失败: %v", err)
			}
			if summary.Scanned != 1 || summary.OK+summary.Failed != 1 {
				t.Fatalf("轮次聚合不符: %+v", summary)
			}

			snapshotJSON, refreshStatus, _, lastSuccess, _, lastError :=
				readXAIGrokSnapshotRow(t, ctx, statsDB)
			switch {
			case len(tt.wantErrParts) > 0:
				if summary.Failed != 1 {
					t.Fatalf("失败轮次聚合不符: %+v", summary)
				}
				if refreshStatus.String != "failed" {
					t.Fatalf("refresh_status = %q want failed", refreshStatus.String)
				}
				for _, part := range tt.wantErrParts {
					if !strings.Contains(lastError.String, part) {
						t.Fatalf("last_error_message 缺少 %q: %q", part, lastError.String)
					}
				}
				// snapshot_json 列 NOT NULL：首次失败落 '{}' 占位而非 NULL。
				if !snapshotJSON.Valid || snapshotJSON.String != "{}" {
					t.Fatalf("首次失败应落 '{}' 占位 snapshot_json: %v", snapshotJSON)
				}
				if lastSuccess.Valid && lastSuccess.String != "" {
					t.Fatalf("首次失败不得写 last_success_at: %q", lastSuccess.String)
				}
			default:
				// settings 失败分支：billing 快照照常 ok，只是不写套餐名。
				if summary.OK != 1 {
					t.Fatalf("settings 失败轮次聚合不符: %+v", summary)
				}
				if refreshStatus.String != "ok" {
					t.Fatalf("refresh_status = %q want ok", refreshStatus.String)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(snapshotJSON.String), &payload); err != nil {
					t.Fatal(err)
				}
				if _, hasTier := payload["grok_subscription_tier"]; hasTier {
					t.Fatal("settings 不可用时不得写套餐名")
				}
				if payload["grok_credit_used_percent"] != 14.0 {
					t.Fatalf("billing 快照必须照常写入: %v", payload)
				}
			}
		})
	}
}

// TestXAIGrokUsageRuntimeFailurePreservesLastGoodSnapshot：成功后失败 →
// failed 状态推进但保留上次成功 payload 与 last_success_at；恢复成功 →
// 全量替换并清错。
func TestXAIGrokUsageRuntimeFailurePreservesLastGoodSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	ctx := context.Background()
	root := t.TempDir()
	businessDB := mustOpenSQLite(t, filepath.Join(root, "business.sqlite3"))
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, filepath.Join(root, "stats.sqlite3"))
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	healthy := true
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing":
			if !healthy {
				w.WriteHeader(503)
				_, _ = w.Write([]byte("degraded"))
				return
			}
			_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":14.0}}`))
		case "/v1/settings":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	seedXAIGrokUsageAccount(t, ctx, businessDB, secret,
		`{"access_token":"tok-xai","base_url":"`+upstream.URL+`/v1"}`)
	runtime := newXaiGrokTestRuntime(t, businessDB, statsDB, secret)

	if _, err := runtime.runRound(ctx); err != nil {
		t.Fatal(err)
	}
	healthy = false
	if _, err := runtime.runRound(ctx); err != nil {
		t.Fatal(err)
	}
	snapshotJSON, refreshStatus, _, lastSuccess, _, lastError :=
		readXAIGrokSnapshotRow(t, ctx, statsDB)
	if refreshStatus.String != "failed" || !strings.Contains(lastError.String, "HTTP 503") {
		t.Fatalf("失败轮状态不符: %q %q", refreshStatus.String, lastError.String)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON.String), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["grok_credit_used_percent"] != 14.0 {
		t.Fatalf("失败轮必须保留上次成功 payload: %v", payload)
	}
	if lastSuccess.String != xaiGrokFixtureNow.Format(time.RFC3339Nano) {
		t.Fatalf("失败轮必须保留 last_success_at: %q", lastSuccess.String)
	}

	healthy = true
	if _, err := runtime.runRound(ctx); err != nil {
		t.Fatal(err)
	}
	snapshotJSON, refreshStatus, _, _, _, lastError = readXAIGrokSnapshotRow(t, ctx, statsDB)
	if refreshStatus.String != "ok" || lastError.Valid && lastError.String != "" {
		t.Fatalf("恢复轮必须清错: %q %q", refreshStatus.String, lastError.String)
	}
	var restored map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON.String), &restored); err != nil {
		t.Fatal(err)
	}
	if restored["grok_credit_used_percent"] != 14.0 {
		t.Fatalf("恢复轮 payload 不符: %v", restored)
	}
}

// TestXAIGrokProxyRequestURL 直测代理档案解析（socks5→socks5h、停用/缺失
// 报错、空 profile 直连）。
func TestXAIGrokProxyRequestURL(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	businessDB := mustOpenSQLite(t, filepath.Join(root, "business.sqlite3"))
	defer businessDB.Close()
	statsDB := mustOpenSQLite(t, filepath.Join(root, "stats.sqlite3"))
	defer statsDB.Close()
	if err := balanceFixtureSchema(ctx, businessDB, statsDB); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	passwordEnvelope, err := accountbalance.EncryptV1Envelope(secret, []byte(`{"password":"pw-xai"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := businessDB.ExecContext(ctx, `
INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted, enabled) VALUES
('proxy-socks', 'socks5', 'proxy.local', 1080, 'alice', ?, 1),
('proxy-disabled', 'http', 'proxy.local', 8080, '', NULL, 0)
`, passwordEnvelope); err != nil {
		t.Fatal(err)
	}
	runtime := newXaiGrokTestRuntime(t, businessDB, statsDB, secret)

	if got, err := runtime.xaiGrokProxyRequestURL(ctx, ""); err != nil || got != "" {
		t.Fatalf("空 profile 必须直连: %q %v", got, err)
	}
	got, err := runtime.xaiGrokProxyRequestURL(ctx, "proxy-socks")
	if err != nil {
		t.Fatal(err)
	}
	if got != "socks5h://alice:pw-xai@proxy.local:1080" {
		t.Fatalf("socks5 代理 URL = %q", got)
	}
	if _, err := runtime.xaiGrokProxyRequestURL(ctx, "proxy-missing"); err == nil {
		t.Fatal("缺失代理档案必须报错")
	}
	if _, err := runtime.xaiGrokProxyRequestURL(ctx, "proxy-disabled"); err == nil {
		t.Fatal("停用代理档案必须报错")
	}
}
