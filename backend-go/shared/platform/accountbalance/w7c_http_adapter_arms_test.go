package accountbalance

// w7c direct HTTP contract tests: every builtin adapter flow, getJSON
// diagnostic arms, the custom adapter path, proxy client selection, and
// endpoint joining rules. All upstreams are scripted doers; nothing dials the
// network.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func w7cQueryInput(t *testing.T, baseURL string, mutate func(*Input)) Input {
	t.Helper()
	input := w7cValidBalanceInput("w7c-http-acct")
	input.Trigger = TriggerManual
	input.BaseURL = baseURL
	if mutate != nil {
		mutate(&input)
	}
	return input
}

func w7cExecute(t *testing.T, input Input, client HTTPDoer, mutate func(*QueryOptions)) QueryResult {
	t.Helper()
	options := QueryOptions{Secret: "w7c-balance-secret", Client: client, Now: time.Now}
	if mutate != nil {
		mutate(&options)
	}
	result, err := ExecuteBalanceQuery(context.Background(), input, options)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return result
}

func TestW7CExecuteBalanceQueryLocalSetupArms(t *testing.T) {
	ctx := context.Background()
	good := &w7cScriptedHTTP{behavior: w7cFreshBehavior}

	if _, err := ExecuteBalanceQuery(ctx, w7cQueryInput(t, "https://example.test", nil), QueryOptions{Client: good}); err == nil {
		t.Fatal("invalid input must fail locally")
	}
	if _, err := ExecuteBalanceQuery(ctx, w7cQueryInput(t, "https://example.test", nil), QueryOptions{Secret: " "}); err == nil || !strings.Contains(err.Error(), "credential secret") {
		t.Fatalf("missing secret: %v", err)
	}
	// Envelope that decrypts but carries no api_key field.
	empty, err := NewCredentialEnvelope("w7c-balance-secret", "api_key", map[string]string{"api_key": "  "})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteBalanceQuery(ctx, w7cQueryInput(t, "https://example.test", func(i *Input) { i.APIKey = empty; i.Credential = empty }), QueryOptions{Secret: "w7c-balance-secret", Client: good}); err == nil || !strings.Contains(err.Error(), "缺少 API Key") {
		t.Fatalf("empty api key: %v", err)
	}
	// Wrong secret fails the envelope open.
	if _, err := ExecuteBalanceQuery(ctx, w7cQueryInput(t, "https://example.test", nil), QueryOptions{Secret: "other-secret", Client: good}); err == nil || !strings.Contains(err.Error(), "无法安全解封") {
		t.Fatalf("decrypt failure: %v", err)
	}
	// Timeout and size bounds are clamped, not rejected.
	result := w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), good, func(o *QueryOptions) {
		o.Timeout = -time.Second
		o.MaxResponseBytes = -1
	})
	if result.Snapshot.Status != StatusFresh {
		t.Fatalf("clamped options: %#v", result)
	}
}

func TestW7CBuiltinAdapterFlows(t *testing.T) {
	// Sub2API first probe succeeds on /v1/usage.
	sub2api := w7cScriptedHTTP{behavior: func(*http.Request) (int, string) {
		return http.StatusOK, `{"unit":"USD","remaining":"12.5","planName":"钱包余额"}`
	}}
	result := w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &sub2api, nil)
	if result.Adapter != AdapterSub2API || result.Snapshot.Status != StatusFresh || result.Snapshot.Basis != BasisWallet {
		t.Fatalf("sub2api: %#v", result)
	}

	// NewAPI unlimited sentinel short-circuits before /api/status.
	newAPIUnlimited := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/usage/token/" {
			return http.StatusOK, `{"data":{"unlimited_quota":true}}`
		}
		return http.StatusOK, `{}`
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &newAPIUnlimited, nil)
	if result.Adapter != AdapterNewAPI || result.Snapshot.Status != StatusUnsupported || result.Snapshot.Basis != BasisAPIKeyQuota {
		t.Fatalf("newapi unlimited: %#v", result)
	}

	// NewAPI full flow: usage then status.
	newAPIFull := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/usage/token/" {
			return http.StatusOK, `{"data":{"total_available":500}}`
		}
		return http.StatusOK, `{"data":{"quota_per_unit":500000,"unlimited_quota":false}}`
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &newAPIFull, nil)
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "0.001000" {
		t.Fatalf("newapi fresh: %#v", result)
	}

	// OpenAI billing flow via preferred adapter: status, subscription, usage.
	billing := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/api/status":
			return http.StatusOK, `{"success":true,"data":{"quota_display_type":"usd"}}`
		case "/dashboard/billing/subscription":
			return http.StatusOK, `{"object":"billing_subscription","hard_limit_usd":120}`
		case "/dashboard/billing/usage":
			return http.StatusOK, `{"object":"list","total_usage":2000}`
		default:
			return http.StatusOK, `{}`
		}
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", func(i *Input) {
		i.Config.PreferredBuiltinAdapter = AdapterOpenAIBilling
	}), &billing, nil)
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "100.000000" || result.Snapshot.RawUnit != RawUnitUSD {
		t.Fatalf("billing flow: %#v", result)
	}

	// LiteLLM flow.
	litellm := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path == "/key/info" {
			return http.StatusOK, `{"info":{"max_budget":100,"spend":25.5}}`
		}
		return http.StatusOK, `{}`
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", func(i *Input) {
		i.Config.PreferredBuiltinAdapter = AdapterLiteLLM
	}), &litellm, nil)
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "74.500000" || result.Snapshot.Basis != BasisBudget {
		t.Fatalf("litellm flow: %#v", result)
	}

	// UserBalance flow with a base URL that carries a UI path: endpoints are
	// rooted at the origin.
	userBalance := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path != "/user/balance" {
			return http.StatusOK, `{"error":"wrong endpoint"}`
		}
		return http.StatusOK, `{"balance":"3.75"}`
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test/console", func(i *Input) {
		i.Config.PreferredBuiltinAdapter = AdapterUserBalance
	}), &userBalance, nil)
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "3.750000" {
		t.Fatalf("user balance flow: %#v", result)
	}
}

func TestW7CAdapterMismatchAndUnsupportedDiagnostics(t *testing.T) {
	// Every endpoint answers with an unrelated-but-valid JSON object: all five
	// adapters mismatch, and the result is a terminal unsupported snapshot.
	mismatch := w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusOK, `{"hello":"world"}` }}
	result := w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &mismatch, nil)
	if result.Adapter != Adapter("") || result.ErrorCode != "adapter_mismatch" || result.Snapshot.Status != StatusUnsupported {
		t.Fatalf("mismatch result: %#v", result)
	}
	if len(mismatch.paths) < 5 {
		t.Fatalf("adapter fallback order must try every builtin: %v", mismatch.paths)
	}

	// The billing status endpoint reporting a tokens display is a terminal
	// unsupported diagnostic for that adapter (driven directly because the
	// builtin fallback would continue with later adapters).
	tokensDisplay := w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path == "/api/status" {
			return http.StatusOK, `{"success":true,"data":{"quota_display_type":"tokens"}}`
		}
		return http.StatusOK, `{}`
	}}
	requester := &balanceRequester{ctx: context.Background(), input: w7cQueryInput(t, "https://example.test", nil), key: "k", doer: &tokensDisplay, maxBytes: defaultMaxBodyBytes}
	if _, diagnostic := requester.queryAdapter(AdapterOpenAIBilling); diagnostic == nil || diagnostic.code != "unsupported" || !strings.Contains(diagnostic.message, "TOKENS") {
		t.Fatalf("tokens display diagnostic: %#v", diagnostic)
	}

	// Unknown preferred adapter names are normalized away at config level, so
	// drive the default arm directly.
	if _, diagnostic := requester.queryAdapter(Adapter("nope")); diagnostic == nil || diagnostic.code != "adapter_invalid" {
		t.Fatalf("unknown adapter diagnostic: %#v", diagnostic)
	}
}

func TestW7CGetJSONTransportArms(t *testing.T) {
	// Transport failure is temporary and skips to the next adapter.
	failing := w7cFailingDoer{err: errors.New("connection refused")}
	result := w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), failing, nil)
	if !result.Temporary || result.ErrorCode != "transport_error" || result.Snapshot.Status != StatusUnsupported {
		t.Fatalf("transport failure: %#v", result)
	}

	// HTTP 500 is a terminal per-adapter diagnostic (not temporary); the last
	// tried adapter (user balance) provides the final reported diagnostic.
	serverError := w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusInternalServerError, "{}" }}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &serverError, nil)
	if result.Temporary || result.ErrorCode != "http_500" {
		t.Fatalf("http 500: %#v", result)
	}

	// Oversized body: bounded read rejects with response_too_large.
	huge := w7cScriptedHTTP{behavior: func(*http.Request) (int, string) {
		return http.StatusOK, `{"unit":"USD","remaining":"` + strings.Repeat("1", 4096) + `"}`
	}}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &huge, func(o *QueryOptions) { o.MaxResponseBytes = 64 })
	if result.ErrorCode != "response_too_large" || !strings.Contains(result.ErrorMessage, "超过大小限制") {
		t.Fatalf("oversized body: %#v", result)
	}

	// Trailing JSON after the first value is invalid.
	trailing := w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusOK, `{"unit":"USD"} {"x":1}` }}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), &trailing, nil)
	if result.ErrorCode != "invalid_json" {
		t.Fatalf("trailing json: %#v", result)
	}

	// A response body read failure is temporary.
	broken := w7cBrokenBodyDoer{}
	result = w7cExecute(t, w7cQueryInput(t, "https://example.test", nil), broken, nil)
	if !result.Temporary || result.ErrorCode != "response_read_error" || !strings.Contains(result.ErrorMessage, "读取失败") {
		t.Fatalf("broken body: %#v", result)
	}
}

type w7cFailingDoer struct{ err error }

func (d w7cFailingDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

type w7cBrokenBodyDoer struct{}

func (w7cBrokenBodyDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(w7cErrorReader{})}, nil
}

type w7cErrorReader struct{}

func (w7cErrorReader) Read([]byte) (int, error) { return 0, errors.New("read reset by peer") }

func TestW7CCustomAdapterArms(t *testing.T) {
	// Success over the remaining pointer with a divisor.
	customInput := w7cQueryInput(t, "https://example.test", func(i *Input) {
		i.Config = QueryConfig{Adapter: Adapter("custom"), IntervalMinutes: 5, Custom: &CustomConfig{
			Path: "/api/credit", RemainingPointer: "/data/credit", Divisor: "100",
		}}
	})
	client := &w7cScriptedHTTP{behavior: func(r *http.Request) (int, string) {
		if r.URL.Path != "/api/credit" {
			return http.StatusOK, `{"nope":1}`
		}
		return http.StatusOK, `{"data":{"credit":1250}}`
	}}
	result := w7cExecute(t, customInput, client, nil)
	if result.Adapter != Adapter("custom") || result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "12.500000" {
		t.Fatalf("custom remaining: %#v", result)
	}

	// total/used pointer pair.
	customTotal := w7cQueryInput(t, "https://example.test", func(i *Input) {
		i.Config = QueryConfig{Adapter: Adapter("custom"), IntervalMinutes: 5, Custom: &CustomConfig{
			Path: "/api/wallet", TotalPointer: "/data/total", UsedPointer: "/data/used",
		}}
	})
	client = &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusOK, `{"data":{"total":900,"used":400}}` }}
	result = w7cExecute(t, customTotal, client, nil)
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "500.000000" {
		t.Fatalf("custom total/used: %#v", result)
	}

	// Field resolution failure is a terminal diagnostic.
	customMissing := w7cQueryInput(t, "https://example.test", func(i *Input) {
		i.Config = QueryConfig{Adapter: Adapter("custom"), IntervalMinutes: 5, Custom: &CustomConfig{
			Path: "/api/credit", RemainingPointer: "/data/absent",
		}}
	})
	result = w7cExecute(t, customMissing, client, nil)
	if result.ErrorCode != "fields_invalid" || result.Snapshot.Status != StatusUnsupported {
		t.Fatalf("custom missing field: %#v", result)
	}

	// queryCustom(nil) arm (unreachable through ExecuteBalanceQuery because
	// config validation rejects a custom adapter without a custom section).
	requester := &balanceRequester{ctx: context.Background(), input: customInput, key: "k", doer: &w7cScriptedHTTP{behavior: w7cFreshBehavior}, maxBytes: defaultMaxBodyBytes}
	missing := requester.queryCustom(nil)
	if missing.ErrorCode != "config_invalid" || missing.Snapshot.Status != StatusUnsupported {
		t.Fatalf("nil custom config: %#v", missing)
	}
	// Custom path that fails endpoint validation.
	badPath := requester.queryCustom(&CustomConfig{Path: "https://evil.test/x"})
	if badPath.ErrorCode != "config_invalid" && badPath.ErrorCode != "endpoint_invalid" {
		t.Fatalf("cross-origin custom path: %#v", badPath)
	}
}

func TestW7CBalanceEndpointRules(t *testing.T) {
	if _, err := balanceEndpoint("https://example.test", ""); err == nil {
		t.Fatal("empty path must be rejected")
	}
	if _, err := balanceEndpoint("https://example.test", "usage"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if _, err := balanceEndpoint("https://example.test", "//evil.test/x"); err == nil {
		t.Fatal("protocol-relative path must be rejected")
	}
	if _, err := balanceEndpoint("ftp://example.test", "/x"); err == nil {
		t.Fatal("non-http base must be rejected")
	}
	if _, err := balanceEndpoint("https://user:pass@example.test", "/x"); err == nil {
		t.Fatal("base with user info must be rejected")
	}
	if _, err := balanceEndpoint("not a url", "/x"); err == nil {
		t.Fatal("unparsable base must be rejected")
	}
	endpoint, err := balanceEndpoint("https://example.test/console?q=1", "/api/credit?full=1")
	if err != nil || endpoint != "https://example.test/api/credit?full=1" {
		t.Fatalf("joined endpoint: %q %v", endpoint, err)
	}
}

func TestW7CBalanceHTTPClientProxyArms(t *testing.T) {
	options := QueryOptions{Secret: "w7c-balance-secret"}

	// nil proxy with an injected client passes through.
	doer, err := balanceHTTPClient(options, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = doer

	// Proxy envelope of the wrong kind fails closed.
	wrongKind := CredentialEnvelope{Kind: "api_key", Ciphertext: "v1:x:y:z"}
	if _, err := balanceHTTPClient(options, &wrongKind, time.Second); err == nil || !strings.Contains(err.Error(), "无法安全解封") {
		t.Fatalf("wrong kind proxy: %v", err)
	}

	// Unsupported proxy scheme.
	ftp, err := NewCredentialEnvelope(options.Secret, "proxy_url", map[string]string{"url": "ftp://proxy.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := balanceHTTPClient(options, &ftp, time.Second); err == nil || !strings.Contains(err.Error(), "协议不受支持") {
		t.Fatalf("unsupported scheme: %v", err)
	}

	// Malformed proxy URL.
	broken, err := NewCredentialEnvelope(options.Secret, "proxy_url", map[string]string{"url": "http://[::1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := balanceHTTPClient(options, &broken, time.Second); err == nil || !strings.Contains(err.Error(), "proxy URL 无效") {
		t.Fatalf("malformed proxy url: %v", err)
	}

	// Valid proxy with an injected client is validated but not rebuilt.
	socks, err := NewCredentialEnvelope(options.Secret, "proxy_url", map[string]string{"url": "socks5://user:pass@127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := balanceHTTPClient(options, &socks, time.Second); err != nil {
		t.Fatalf("valid proxy: %v", err)
	}

	// Valid proxy without an injected client builds a real transport.
	doer, err = balanceHTTPClient(QueryOptions{Secret: options.Secret}, &socks, time.Second)
	if err != nil || doer == nil {
		t.Fatalf("proxy transport: %v", err)
	}
}

func TestW7CQueryDiagnosticErrorAndRemoveAdapter(t *testing.T) {
	diagnostic := &queryDiagnostic{message: "boom"}
	if diagnostic.Error() != "boom" {
		t.Fatalf("diagnostic error: %q", diagnostic.Error())
	}
	order := removeAdapter([]Adapter{AdapterSub2API, AdapterNewAPI, AdapterSub2API}, AdapterSub2API)
	if len(order) != 1 || order[0] != AdapterNewAPI {
		t.Fatalf("removeAdapter: %#v", order)
	}
}
