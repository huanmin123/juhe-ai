package accountbalance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

const (
	defaultBalanceTimeout = 15 * time.Second
	defaultMaxBodyBytes   = 256 << 10
)

// HTTPDoer is injectable for tests.  Production callers may leave Client nil
// and receive a direct, no-environment-fallback HTTP transport.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type QueryOptions struct {
	Secret           string
	Client           HTTPDoer
	Timeout          time.Duration
	MaxResponseBytes int64
	Now              func() time.Time
}

type QueryResult struct {
	Snapshot     Snapshot
	Adapter      Adapter
	Temporary    bool
	ErrorCode    string
	ErrorMessage string
	Attempts     int
}

type queryDiagnostic struct {
	code      string
	message   string
	temporary bool
}

func (e *queryDiagnostic) Error() string { return e.message }

// ExecuteBalanceQuery performs one bounded direct query.  It does not call
// Node, gateway, IPC, Redis, or a business SQLite database.  A returned error
// means the input/transport setup failed locally; upstream HTTP and parsing
// diagnostics are represented in QueryResult so a single account cannot abort
// a batch.
func ExecuteBalanceQuery(ctx context.Context, input Input, options QueryOptions) (QueryResult, error) {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	if err := input.Validate(now().UTC()); err != nil {
		return QueryResult{}, err
	}
	if strings.TrimSpace(options.Secret) == "" {
		return QueryResult{}, errors.New("account-balance direct HTTP 缺少 credential secret")
	}
	timeout := options.Timeout
	if timeout <= 0 || timeout > defaultBalanceTimeout {
		timeout = defaultBalanceTimeout
	}
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 || maxBytes > defaultMaxBodyBytes {
		maxBytes = defaultMaxBodyBytes
	}
	var keyPayload struct {
		APIKey string `json:"api_key"`
	}
	credential := input.APIKey
	if strings.TrimSpace(credential.Ciphertext) == "" {
		credential = input.Credential
	}
	if err := openCredential(options.Secret, credential, "api_key", &keyPayload); err != nil {
		return QueryResult{}, fmt.Errorf("account-balance API Key envelope 无法安全解封: %w", err)
	}
	keyPayload.APIKey = strings.TrimSpace(keyPayload.APIKey)
	if keyPayload.APIKey == "" {
		return QueryResult{}, errors.New("account-balance API Key envelope 缺少 API Key")
	}
	doer, err := balanceHTTPClient(options, input.Proxy, timeout)
	if err != nil {
		return QueryResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requester := &balanceRequester{ctx: ctx, input: input, key: keyPayload.APIKey, doer: doer, maxBytes: maxBytes}
	if input.Config.Adapter == "custom" {
		result := requester.queryCustom(input.Config.Custom)
		return result, nil
	}
	return requester.queryBuiltin(input.Config.PreferredBuiltinAdapter)
}

type balanceRequester struct {
	ctx      context.Context
	input    Input
	key      string
	doer     HTTPDoer
	maxBytes int64
}

func (r *balanceRequester) queryBuiltin(preferred Adapter) (QueryResult, error) {
	order := []Adapter{AdapterSub2API, AdapterNewAPI, AdapterOpenAIBilling, AdapterLiteLLM, AdapterUserBalance}
	if preferred != "" && isBuiltinAdapter(preferred) {
		order = append([]Adapter{preferred}, removeAdapter(order, preferred)...)
	}
	var last *queryDiagnostic
	var lastTemporary *queryDiagnostic
	for _, adapter := range order {
		result, diagnostic := r.queryAdapter(adapter)
		result.Attempts++
		if diagnostic == nil {
			result.Adapter = adapter
			return result, nil
		}
		if diagnostic.temporary {
			lastTemporary = diagnostic
			last = diagnostic
			continue
		}
		last = diagnostic
	}
	if last == nil {
		last = &queryDiagnostic{code: "unsupported", message: "没有可用的内置余额适配器"}
	}
	if lastTemporary != nil {
		last = lastTemporary
	}
	return QueryResult{
		Adapter: Adapter(""), Temporary: last.temporary,
		ErrorCode: last.code, ErrorMessage: last.message,
		Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: last.message},
	}, nil
}

func removeAdapter(values []Adapter, target Adapter) []Adapter {
	result := make([]Adapter, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func (r *balanceRequester) queryAdapter(adapter Adapter) (QueryResult, *queryDiagnostic) {
	switch adapter {
	case AdapterSub2API:
		value, diagnostic := r.getJSON("/v1/usage")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		snapshot, err := ParseSub2API(value)
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		return QueryResult{Snapshot: snapshot}, nil
	case AdapterNewAPI:
		// Match the Node contract: usage is authoritative for the unlimited
		// sentinel and avoids an unnecessary status request in that case.
		usage, diagnostic := r.getJSON("/api/usage/token/")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		usageRoot, err := object(usage, "New API 响应")
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		usageData, err := object(usageRoot["data"], "New API data")
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		if usageData["unlimited_quota"] == true {
			return QueryResult{Snapshot: Snapshot{Status: StatusUnsupported, Basis: BasisAPIKeyQuota}}, nil
		}
		statusValue, diagnostic := r.getJSON("/api/status")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		statusRoot, err := object(statusValue, "余额状态响应")
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		statusData, err := object(statusRoot["data"], "余额状态 data")
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		snapshot, err := ParseNewAPI(usage, statusData["quota_per_unit"])
		if err != nil || snapshot.Status == StatusUnsupported {
			if err != nil {
				return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
			}
			return QueryResult{}, &queryDiagnostic{code: "unsupported", message: snapshot.ErrorMessage}
		}
		return QueryResult{Snapshot: snapshot}, nil
	case AdapterOpenAIBilling:
		statusValue, diagnostic := r.getJSON("/api/status")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		status, err := ParseOpenAIBillingStatus(statusValue)
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		if status.Snapshot != nil {
			return QueryResult{}, &queryDiagnostic{code: "unsupported", message: status.Snapshot.ErrorMessage}
		}
		subscription, diagnostic := r.getJSON("/dashboard/billing/subscription")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		usage, diagnostic := r.getJSON("/dashboard/billing/usage")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		divisor := any(nil)
		if status.Divisor != "" {
			divisor = status.Divisor
		}
		snapshot, err := ParseOpenAIBilling(subscription, usage, divisor, status.RawUnit)
		if err != nil || snapshot.Status == StatusUnsupported {
			if err != nil {
				return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
			}
			return QueryResult{}, &queryDiagnostic{code: "unsupported", message: snapshot.ErrorMessage}
		}
		return QueryResult{Snapshot: snapshot}, nil
	case AdapterLiteLLM:
		value, diagnostic := r.getJSON("/key/info")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		snapshot, err := ParseLiteLLM(value)
		if err != nil || snapshot.Status == StatusUnsupported {
			if err != nil {
				return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
			}
			return QueryResult{}, &queryDiagnostic{code: "unsupported", message: snapshot.ErrorMessage}
		}
		return QueryResult{Snapshot: snapshot}, nil
	case AdapterUserBalance:
		value, diagnostic := r.getJSON("/user/balance")
		if diagnostic != nil {
			return QueryResult{}, diagnostic
		}
		snapshot, err := ParseUserBalance(value)
		if err != nil {
			return QueryResult{}, &queryDiagnostic{code: "adapter_mismatch", message: err.Error()}
		}
		return QueryResult{Snapshot: snapshot}, nil
	default:
		return QueryResult{}, &queryDiagnostic{code: "adapter_invalid", message: "未知内置余额适配器"}
	}
}

func (r *balanceRequester) queryCustom(config *CustomConfig) QueryResult {
	if config == nil {
		return QueryResult{Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: "自定义余额配置缺失"}, ErrorCode: "config_invalid", ErrorMessage: "自定义余额配置缺失"}
	}
	value, diagnostic := r.getJSON(config.Path)
	if diagnostic != nil {
		return QueryResult{Temporary: diagnostic.temporary, ErrorCode: diagnostic.code, ErrorMessage: diagnostic.message, Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: diagnostic.message}}
	}
	snapshot, err := ParseCustom(value, config.RemainingPointer, config.TotalPointer, config.UsedPointer, config.Divisor)
	if err != nil {
		return QueryResult{ErrorCode: "fields_invalid", ErrorMessage: err.Error(), Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: err.Error()}}
	}
	return QueryResult{Adapter: Adapter("custom"), Snapshot: snapshot}
}

type requestAuth struct {
	name  string
	value string
}

func (r *balanceRequester) getJSON(path string, auth ...requestAuth) (any, *queryDiagnostic) {
	endpoint, err := balanceEndpoint(r.input.BaseURL, path)
	if err != nil {
		return nil, &queryDiagnostic{code: "endpoint_invalid", message: err.Error()}
	}
	request, err := http.NewRequestWithContext(r.ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &queryDiagnostic{code: "request_invalid", message: "余额请求构造失败"}
	}
	request.Header.Set("Accept", "application/json")
	if len(auth) == 0 {
		auth = []requestAuth{{name: "Authorization", value: "Bearer " + r.key}}
	}
	if auth[0].name != "" {
		request.Header.Set(auth[0].name, auth[0].value)
	}
	response, err := r.doer.Do(request)
	if err != nil {
		return nil, &queryDiagnostic{code: "transport_error", message: "余额上游请求失败", temporary: true}
	}
	if response == nil || response.Body == nil {
		return nil, &queryDiagnostic{code: "response_invalid", message: "余额上游响应为空", temporary: true}
	}
	defer response.Body.Close()
	body, readErr := upstreamhttp.ReadBounded(response.Body, r.maxBytes)
	if readErr != nil {
		if errors.Is(readErr, upstreamhttp.ErrResponseBodyTooLarge) {
			return nil, &queryDiagnostic{code: "response_too_large", message: "余额上游响应超过大小限制"}
		}
		return nil, &queryDiagnostic{code: "response_read_error", message: "余额上游响应读取失败", temporary: true}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &queryDiagnostic{code: fmt.Sprintf("http_%d", response.StatusCode), message: fmt.Sprintf("余额上游返回 HTTP %d", response.StatusCode)}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &queryDiagnostic{code: "invalid_json", message: "余额上游响应不是有效 JSON"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, &queryDiagnostic{code: "invalid_json", message: "余额上游响应不是有效 JSON"}
	}
	return value, nil
}

func balanceEndpoint(base, path string) (string, error) {
	if strings.TrimSpace(path) == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", errors.New("余额查询路径必须是同源绝对路径")
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.User != nil {
		return "", errors.New("余额 Base URL 无效")
	}
	pathURL, err := url.Parse(path)
	if err != nil || pathURL.IsAbs() || pathURL.Host != "" {
		return "", errors.New("余额查询路径不得跨 Origin")
	}
	// Node's account-balance query is origin-scoped: an account Base URL may
	// contain a UI/API path, but each adapter endpoint is rooted at the origin.
	baseURL.Path = pathURL.Path
	baseURL.RawPath = ""
	baseURL.RawQuery = pathURL.RawQuery
	baseURL.Fragment = ""
	return baseURL.String(), nil
}

func balanceHTTPClient(options QueryOptions, proxy *CredentialEnvelope, timeout time.Duration) (HTTPDoer, error) {
	if proxy == nil {
		if options.Client != nil {
			return options.Client, nil
		}
		return upstreamhttp.SharedClient("", upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := openCredential(options.Secret, *proxy, "proxy_url", &payload); err != nil {
		return nil, fmt.Errorf("account-balance proxy envelope 无法安全解封: %w", err)
	}
	if _, err := upstreamhttp.ParseProxyURL(payload.URL); err != nil {
		if errors.Is(err, upstreamhttp.ErrProxySchemeUnsupported) {
			return nil, errors.New("account-balance proxy 协议不受支持")
		}
		return nil, errors.New("account-balance proxy URL 无效")
	}
	if options.Client != nil {
		// An injected client owns its transport; still validate the encrypted
		// proxy envelope above so tests cannot accidentally bypass malformed
		// production input.
		return options.Client, nil
	}
	return upstreamhttp.SharedClient(payload.URL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
}
