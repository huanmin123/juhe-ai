package accountbalance

// w12h 补充 arms：适配器解析矩阵、自定义 JSON Pointer、凭据分段解码、
// 运行时配置环境矩阵、直连 HTTP 的剩余诊断分支。
// 本文件补充登记的不可达/无法本机触发语句：
//   - direct_http.go getJSON 的 request_invalid 分支：balanceEndpoint 已先
//     校验 URL，构造必然成功；
//   - direct_input_reader.go 中 SET LOCAL / 只读事务 / rows.Err / 扫描与
//     提交错误分支：依赖真实 PostgreSQL 业务契约，SQLite 池无法承载。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const w12hDirectSecret = "w12h-direct-secret"

func w12hDirectCredential(t *testing.T) CredentialEnvelope {
	t.Helper()
	credential, err := NewCredentialEnvelope(w12hDirectSecret, "api_key", map[string]string{"api_key": "sk-w12h-direct"})
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func w12hDirectInput(t *testing.T, preferred Adapter, proxy *CredentialEnvelope) Input {
	t.Helper()
	now := time.Now().UTC()
	input := Input{
		AccountID: "w12h-direct", SystemAccountID: "w12h-sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://w12h-upstream.invalid", Config: QueryConfig{Adapter: Adapter("builtin"), PreferredBuiltinAdapter: preferred, IntervalMinutes: 5},
		APIKey: w12hDirectCredential(t), Proxy: proxy,
		Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	due := now.Add(-time.Minute)
	input.NextRefreshAt = &due
	return input
}

// w12hPathDoer 按请求路径回放脚本化响应，未登记路径返回传输错误。
type w12hPathDoer struct {
	responses map[string]func() (*http.Response, error)
}

func (d *w12hPathDoer) Do(request *http.Request) (*http.Response, error) {
	generate, ok := d.responses[request.URL.Path]
	if !ok {
		return nil, errors.New("w12h 未登记路径 " + request.URL.Path)
	}
	return generate()
}

func w12hJSONResponse(body string) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func w12hMustQuery(t *testing.T, input Input, doer HTTPDoer) (QueryResult, error) {
	t.Helper()
	return ExecuteBalanceQuery(context.Background(), input, QueryOptions{Secret: w12hDirectSecret, Client: doer, Timeout: 2 * time.Second, MaxResponseBytes: 1 << 20, Now: time.Now})
}

var _ = context.Background

func TestW12HDirectHTTPBasicArms(t *testing.T) {
	// 输入校验失败。
	bad := w12hDirectInput(t, "", nil)
	bad.APIKey = CredentialEnvelope{}
	if _, err := w12hMustQuery(t, bad, &w12hPathDoer{}); err == nil {
		t.Fatal("非法输入必须失败")
	}
	// API Key 为空时回退 Credential 字段。
	fallback := w12hDirectInput(t, "", nil)
	credential := w12hDirectCredential(t)
	fallback.APIKey = CredentialEnvelope{}
	fallback.Credential = credential
	if result, err := w12hMustQuery(t, fallback, &w12hPathDoer{}); err != nil || result.ErrorCode == "" {
		t.Fatalf("无上游必须给出错误码: %+v %v", result, err)
	}
	// 代理 envelope 解封失败。
	badProxy, err := NewCredentialEnvelope("other-secret", "proxy_url", map[string]string{"url": "socks5h://127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	withBadProxy := w12hDirectInput(t, "", &badProxy)
	if _, err := w12hMustQuery(t, withBadProxy, &w12hPathDoer{}); err == nil {
		t.Fatal("代理解封失败必须报错")
	}
	// 代理合法且注入客户端：直接复用注入客户端。
	goodProxy, err := NewCredentialEnvelope(w12hDirectSecret, "proxy_url", map[string]string{"url": "socks5h://127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	withProxy := w12hDirectInput(t, "", &goodProxy)
	if result, err := w12hMustQuery(t, withProxy, &w12hPathDoer{}); err != nil || result.ErrorCode == "" {
		t.Fatalf("无上游必须给出错误码: %+v %v", result, err)
	}
}

func TestW12HNewAPIAdapterArms(t *testing.T) {
	// usage 非对象 → adapter_mismatch，其余适配器全部传输失败 → 临时错误。
	doer := &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/usage/token/": func() (*http.Response, error) { return w12hJSONResponse(`"not-an-object"`) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("usage 非对象必须给出错误码: %+v %v", result, err)
	}
	// usage.data 缺失。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/usage/token/": func() (*http.Response, error) { return w12hJSONResponse(`{"data":null}`) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("usage.data 缺失必须给出错误码: %+v %v", result, err)
	}
	// unlimited 命中 → unsupported 快照。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/usage/token/": func() (*http.Response, error) { return w12hJSONResponse(`{"data":{"unlimited_quota":true}}`) },
	}}
	result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer)
	if err != nil || result.Snapshot.Status != StatusUnsupported {
		t.Fatalf("无限额度必须得到 unsupported: %+v %v", result, err)
	}
	// status 缺失 / 非对象 / data 缺失。
	for name, statusBody := range map[string]string{
		"missing":  "",
		"nonobj":   `"[1]"`,
		"bad-data": `{"data":"x"}`,
	} {
		responses := map[string]func() (*http.Response, error){
			"/api/usage/token/": func() (*http.Response, error) {
				return w12hJSONResponse(`{"data":{"unlimited_quota":false,"hard_limit_usd":"10","total_usage":"2"}}`)
			},
		}
		if statusBody == "" {
			delete(responses, "/api/status")
		} else {
			body := statusBody
			responses["/api/status"] = func() (*http.Response, error) { return w12hJSONResponse(body) }
		}
		doer := &w12hPathDoer{responses: responses}
		if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer); err != nil || result.ErrorCode == "" {
			t.Fatalf("status %s 必须给出错误码: %+v %v", name, result, err)
		}
	}
	// ParseNewAPI 解析错误：total_available 非数字。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/usage/token/": func() (*http.Response, error) {
			return w12hJSONResponse(`{"data":{"unlimited_quota":false,"total_available":"abc"}}`)
		},
		"/api/status": func() (*http.Response, error) { return w12hJSONResponse(`{"data":{"quota_per_unit":"5"}}`) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("total_available 非法必须给出错误码: %+v %v", result, err)
	}
	// 正常数据 → fresh。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/usage/token/": func() (*http.Response, error) {
			return w12hJSONResponse(`{"data":{"unlimited_quota":false,"total_available":"2"}}`)
		},
		"/api/status": func() (*http.Response, error) { return w12hJSONResponse(`{"data":{"quota_per_unit":"5"}}`) },
	}}
	result, err = w12hMustQuery(t, w12hDirectInput(t, AdapterNewAPI, nil), doer)
	if err != nil || result.Snapshot.Status != StatusFresh {
		t.Fatalf("NewAPI 正常数据必须 fresh: %+v %v", result, err)
	}
}

func TestW12HOpenAIBillingAdapterArms(t *testing.T) {
	usage := `{"object":"list","total_usage":"0.5"}`
	subscription := `{"object":"billing_subscription","hard_limit_usd":"10"}`
	statusOK := `{"success":true,"data":{"display_in_currency":true}}`
	// subscription / usage 缺失。
	for name, responses := range map[string]map[string]func() (*http.Response, error){
		"subscription-missing": {
			"/api/status":              func() (*http.Response, error) { return w12hJSONResponse(statusOK) },
			"/dashboard/billing/usage": func() (*http.Response, error) { return w12hJSONResponse(usage) },
		},
		"usage-missing": {
			"/api/status":                     func() (*http.Response, error) { return w12hJSONResponse(statusOK) },
			"/dashboard/billing/subscription": func() (*http.Response, error) { return w12hJSONResponse(subscription) },
		},
	} {
		doer := &w12hPathDoer{responses: responses}
		if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.ErrorCode == "" {
			t.Fatalf("%s 必须给出错误码: %+v %v", name, result, err)
		}
	}
	// ParseOpenAIBilling 解析错误：total_usage 非数字。
	doer := &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status":                     func() (*http.Response, error) { return w12hJSONResponse(statusOK) },
		"/dashboard/billing/subscription": func() (*http.Response, error) { return w12hJSONResponse(subscription) },
		"/dashboard/billing/usage":        func() (*http.Response, error) { return w12hJSONResponse(`{"object":"list","total_usage":"abc"}`) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("usage 非法必须给出错误码: %+v %v", result, err)
	}
	// 硬上限无限 → unsupported。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status": func() (*http.Response, error) {
			return w12hJSONResponse(`{"success":true,"data":{"display_in_currency":false,"quota_per_unit":"5"}}`)
		},
		"/dashboard/billing/subscription": func() (*http.Response, error) {
			return w12hJSONResponse(`{"object":"billing_subscription","hard_limit_usd":"100000000"}`)
		},
		"/dashboard/billing/usage": func() (*http.Response, error) { return w12hJSONResponse(usage) },
	}}
	billingResult, billingErr := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer)
	if billingErr != nil || billingResult.Snapshot.Status != StatusUnsupported {
		t.Fatalf("硬上限必须 unsupported: %+v %v", billingResult, billingErr)
	}
	// 状态接口 quota_per_unit 非数字。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status": func() (*http.Response, error) {
			return w12hJSONResponse(`{"success":true,"data":{"display_in_currency":false,"quota_per_unit":"abc"}}`)
		},
		"/dashboard/billing/subscription": func() (*http.Response, error) { return w12hJSONResponse(subscription) },
		"/dashboard/billing/usage":        func() (*http.Response, error) { return w12hJSONResponse(usage) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("quota_per_unit 非法必须给出错误码: %+v %v", result, err)
	}
	// 除数参与换算（status.Divisor 非空）。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status": func() (*http.Response, error) {
			return w12hJSONResponse(`{"success":true,"data":{"display_in_currency":false,"quota_per_unit":"5"}}`)
		},
		"/dashboard/billing/subscription": func() (*http.Response, error) { return w12hJSONResponse(subscription) },
		"/dashboard/billing/usage":        func() (*http.Response, error) { return w12hJSONResponse(usage) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.Snapshot.Status != StatusFresh {
		t.Fatalf("除数换算必须得到 fresh: %+v %v", result, err)
	}
	// 响应为空 / 非 JSON。
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status": func() (*http.Response, error) { return &http.Response{StatusCode: 200}, nil },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("空响应必须给出错误码: %+v %v", result, err)
	}
	doer = &w12hPathDoer{responses: map[string]func() (*http.Response, error){
		"/api/status": func() (*http.Response, error) { return w12hJSONResponse(`not json`) },
	}}
	if result, err := w12hMustQuery(t, w12hDirectInput(t, AdapterOpenAIBilling, nil), doer); err != nil || result.ErrorCode == "" {
		t.Fatalf("非 JSON 响应必须给出错误码: %+v %v", result, err)
	}
}

func TestW12HCustomAndPointerArms(t *testing.T) {
	value := map[string]any{"total": "abc", "used": "1"}
	if _, err := ParseCustom(value, "", "/total", "/used", ""); err == nil {
		t.Fatal("total 非数字必须失败")
	}
	if _, err := ParseCustom(map[string]any{"total": "1", "used": "1"}, "", "/missing", "/used", ""); err == nil {
		t.Fatal("total 指针缺失必须失败")
	}
	if _, err := ParseCustom(map[string]any{"total": "5", "used": "1"}, "", "/total", "/used", "abc"); err == nil {
		t.Fatal("divisor 非数字必须失败")
	}
	if _, err := ParseCustom(map[string]any{"total": "5", "used": "1"}, "", "/total", "/used", "0"); err == nil {
		t.Fatal("divisor 非正必须失败")
	}
	// jsonPointer 边界。
	if got, err := jsonPointer("raw", ""); err != nil || got != "raw" {
		t.Fatalf("空指针必须原样返回: %v %v", got, err)
	}
	if _, err := jsonPointer("raw", "no-slash"); err == nil {
		t.Fatal("非 / 前缀必须失败")
	}
	if _, err := jsonPointer("scalar", "/field"); err == nil {
		t.Fatal("标量遍历必须失败")
	}
	// 状态接口 quota_per_unit 解析错误。
	if _, err := ParseOpenAIBillingStatus(map[string]any{"data": map[string]any{"display_in_currency": false, "quota_per_unit": "abc"}}); err == nil {
		t.Fatal("quota_per_unit 非数字必须失败")
	}
}

func TestW12HModelAndRuntimeArms(t *testing.T) {
	now := time.Now().UTC()
	// ToInput：已删除候选。
	deleted := Candidate{AccountID: "w12h-m1", SystemAccountID: "sys", InputVersion: 1, ConfigRevision: 1, Deleted: true}
	if _, err := deleted.ToInput(TriggerPeriodic, now, time.Minute); err == nil {
		t.Fatal("已删除候选必须拒绝")
	}
	// keyCount 密文回退 + interval 缺省 + ExpiresAt 缺省。
	fallback := Candidate{AccountID: "w12h-m2", SystemAccountID: "sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", BaseURL: "https://w12h.invalid",
		Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 0},
		APIKey: CredentialEnvelope{Kind: "api_key", Ciphertext: "cipher"}, IssuedAt: now}
	input, err := fallback.ToInput(TriggerPeriodic, now, time.Minute)
	if err != nil {
		t.Fatalf("密文回退必须成功: %v", err)
	}
	if input.Config.IntervalMinutes != 5 {
		t.Fatalf("interval 必须缺省为 5: %d", input.Config.IntervalMinutes)
	}
	if input.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt 必须按 ttl 兜底")
	}
	// Input.Validate 的 interval 缺省分支。
	input.Config.IntervalMinutes = 0
	due := now.Add(time.Minute)
	input.NextRefreshAt = &due
	if err := input.Validate(now.Add(time.Second)); err != nil {
		t.Fatalf("interval=0 必须可校验: %v", err)
	}

	// 运行时配置环境矩阵。
	disabled := map[string]string{"JUHE_AI_ACCOUNT_BALANCE_ENABLED": "false"}
	if cfg, err := LoadRuntimeConfig(func(string) string { return "" }); err != nil || cfg.Enabled {
		t.Fatalf("禁用配置必须直接返回: %+v %v", cfg, err)
	}
	_ = disabled
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_ENABLED":            "true",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":         "go",
			"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":           "w12h-runtime",
			"JUHE_AI_ACCOUNT_BALANCE_STORE":              "postgres",
			"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":       "postgres://u:p@127.0.0.1:5432/j2",
			"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": "postgres://u:p@127.0.0.1:5432/juhe_ai",
			"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET":  "w12h-runtime-secret",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET":   "w12h-runtime-jobs-http-secret-0123456789abcdef",
		}
	}
	lookup := func(env map[string]string) func(string) string {
		return func(key string) string { return env[key] }
	}
	if _, err := LoadRuntimeConfig(lookup(base())); err != nil {
		t.Fatalf("合法配置必须通过: %v", err)
	}
	idle := base()
	idle["JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_IDLE_CONNS"] = "0"
	if _, err := LoadRuntimeConfig(lookup(idle)); err == nil {
		t.Fatal("jobs 空闲连接 0 必须失败")
	}
	inputOpen := base()
	inputOpen["JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_OPEN_CONNS"] = "-1"
	if _, err := LoadRuntimeConfig(lookup(inputOpen)); err == nil {
		t.Fatal("输入连接 -1 必须失败")
	}
	inputIdle := base()
	inputIdle["JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS"] = "0"
	if _, err := LoadRuntimeConfig(lookup(inputIdle)); err == nil {
		t.Fatal("输入空闲连接 0 必须失败")
	}
	swapped := base()
	swapped["JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_OPEN_CONNS"] = "1"
	swapped["JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_IDLE_CONNS"] = "5"
	if _, err := LoadRuntimeConfig(lookup(swapped)); err == nil {
		t.Fatal("空闲大于最大必须失败")
	}
	inputSwapped := base()
	inputSwapped["JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_OPEN_CONNS"] = "1"
	inputSwapped["JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS"] = "5"
	if _, err := LoadRuntimeConfig(lookup(inputSwapped)); err == nil {
		t.Fatal("输入空闲大于最大必须失败")
	}

	// integerValue 的默认分支（字符串 interval）。
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": "ten"}); err == nil {
		t.Fatal("字符串 interval 必须失败")
	}
}

func TestW12HCredentialSegmentArms(t *testing.T) {
	// tag / ciphertext 段 base64 解码失败。
	if _, err := DecryptV1Envelope("w12h-direct-secret", "v1:AAAAAAAAAAAAAAAA:!!:AAAA"); err == nil {
		t.Fatal("tag 段解码失败必须报错")
	}
	if _, err := DecryptV1Envelope("w12h-direct-secret", "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAAAAAAAA:!!"); err == nil {
		t.Fatal("ciphertext 段解码失败必须报错")
	}
	// 空 secret 让 EncryptV1Envelope 失败并从 NewCredentialEnvelope 传播。
	if _, err := NewCredentialEnvelope("  ", "api_key", map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("空 secret 必须失败")
	}
}

func TestW12HDecimalTextArms(t *testing.T) {
	for _, value := range []any{float64(1.5), float32(2.5), int64(7), json.Number("9")} {
		if _, err := parseDecimal(value, "field"); err != nil {
			t.Fatalf("数值 %v 必须可解析: %v", value, err)
		}
	}
	if _, err := parseDecimal("1.2x", "field"); err == nil {
		t.Fatal("小数段非法必须失败")
	}
}
