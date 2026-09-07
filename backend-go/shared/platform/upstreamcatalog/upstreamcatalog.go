// Package upstreamcatalog fetches the live upstream model catalog of one
// account credential (the Go port of the archived Node
// account-test.service.ts discoverAccountUpstreamModels + account-test-request
// .ts accountTestModelsPathForProtocol + account-test-success-evidence.ts
// accountModelCatalogIdsFromPayload). It is a pure in-process HTTP reader: no
// storage, no gateway chain, no cross-process bridge. The comparison against
// the local provider model catalog stays with the caller.
//
// Protocol → models path mapping (account-test-request.ts:25-26,42-44):
//
//	gemini    -> {base}/v1beta/models
//	otherwise -> {base}/v1/models   (openai and anthropic both use /v1/models)
//
// Auth headers follow the upstream protocol conventions: Bearer for
// OpenAI-compatible bases, x-api-key (+ anthropic-version) for anthropic and
// x-goog-api-key for gemini. The response may use either catalog shape:
// {data:[{id}]} or {models:[{name:"models/<id>"}]} (the gemini listing).
package upstreamcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// DefaultFetchTimeout bounds one upstream models request (Node
// accountModelCatalogDiagnosticTimeoutMs resolves to the same single-attempt
// budget envelope; the direct reader keeps one bounded attempt).
const DefaultFetchTimeout = 15 * time.Second

// DefaultMaxBodyBytes bounds the models response body like every other
// platform upstream reader.
const DefaultMaxBodyBytes = 4 << 20

// HTTPDoer is injectable for tests (Mock 优先规范). Production callers may
// leave Doer nil and receive a shared upstreamhttp client (socks5/socks5h and
// http(s) proxy aware, no environment fallback).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// FetchOptions is one upstream models-catalog request contract.
type FetchOptions struct {
	// BaseURL is the account credentials base_url (host-only or
	// version-prefixed; the path is normalized per protocol).
	BaseURL string
	// ProtocolCode is the provider protocol profile protocol ("openai",
	// "anthropic", "gemini", ...). Unknown codes use the /v1/models shape.
	ProtocolCode string
	// Credential is the plaintext upstream API key (single key).
	Credential string
	// ProxyURL is the optional raw proxy URL ("" = direct).
	ProxyURL string
	// Doer overrides the shared HTTP client (tests).
	Doer HTTPDoer
	// Timeout bounds the request; <= 0 keeps DefaultFetchTimeout.
	Timeout time.Duration
	// MaxBodyBytes bounds the response body; <= 0 keeps DefaultMaxBodyBytes.
	MaxBodyBytes int64
}

// modelsPathForProtocol mirrors accountTestModelsPathForProtocol
// (account-test-request.ts:42-44): gemini uses /v1beta/models, everything
// else /v1/models.
func modelsPathForProtocol(protocolCode string) string {
	if strings.EqualFold(strings.TrimSpace(protocolCode), "gemini") {
		return "/v1beta/models"
	}
	return "/v1/models"
}

// versionRootForProtocol is the base-path normalization root per protocol
// (mirrors the gatewayopenai BuildUpstreamURL /v1 normalization idea, plus
// the gemini /v1beta root).
func versionRootForProtocol(protocolCode string) string {
	if strings.EqualFold(strings.TrimSpace(protocolCode), "gemini") {
		return "/v1beta"
	}
	return "/v1"
}

// upstreamModelsURL normalizes the account base URL against the protocol
// models path: the version root is appended only when the base does not
// already end with it, so both host-only and version-prefixed base URLs land
// on the same absolute URL.
func upstreamModelsURL(baseURL, protocolCode string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return "", errors.New("获取上游模型目录失败：账户缺少 base_url")
	}
	root := versionRootForProtocol(protocolCode)
	if !strings.HasSuffix(trimmed, root) {
		trimmed += root
	}
	return trimmed + strings.TrimPrefix(modelsPathForProtocol(protocolCode), root), nil
}

// requestHeadersForProtocol mirrors the upstream auth conventions the gateway
// adapters apply: Bearer for OpenAI-compatible bases, x-api-key +
// anthropic-version for anthropic, x-goog-api-key for gemini.
func requestHeadersForProtocol(protocolCode, credential string) http.Header {
	header := http.Header{}
	header.Set("Accept", "application/json")
	switch strings.ToLower(strings.TrimSpace(protocolCode)) {
	case "anthropic":
		header.Set("x-api-key", credential)
		header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		header.Set("x-goog-api-key", credential)
	default:
		header.Set("Authorization", "Bearer "+credential)
	}
	return header
}

// FetchUpstreamModelIDs performs one bounded upstream models request and
// returns the deduplicated model IDs (accountModelCatalogIdsFromPayload
// semantics: data[].id for the OpenAI shape, models[].name with the models/
// prefix stripped for the gemini shape; empty values dropped).
func FetchUpstreamModelIDs(ctx context.Context, options FetchOptions) ([]string, error) {
	credential := strings.TrimSpace(options.Credential)
	if credential == "" {
		return nil, errors.New("获取上游模型目录失败：账户缺少 API Key")
	}
	endpoint, err := upstreamModelsURL(options.BaseURL, options.ProtocolCode)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	maxBytes := options.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	doer := options.Doer
	if doer == nil {
		doer, err = upstreamhttp.SharedClient(options.ProxyURL, upstreamhttp.TransportOptions{ResponseHeaderTimeout: timeout})
		if err != nil {
			if errors.Is(err, upstreamhttp.ErrProxySchemeUnsupported) {
				return nil, errors.New("获取上游模型目录失败：代理协议不受支持")
			}
			return nil, errors.New("获取上游模型目录失败：代理 URL 无效")
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("获取上游模型目录失败：请求构造失败")
	}
	for key, values := range requestHeadersForProtocol(options.ProtocolCode, credential) {
		for _, value := range values {
			request.Header.Set(key, value)
		}
	}
	response, err := doer.Do(request)
	if err != nil {
		return nil, errors.New("获取上游模型目录失败：上游请求失败")
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("获取上游模型目录失败：上游响应为空")
	}
	defer response.Body.Close()
	body, readErr := upstreamhttp.ReadBounded(response.Body, maxBytes)
	if readErr != nil {
		if errors.Is(readErr, upstreamhttp.ErrResponseBodyTooLarge) {
			return nil, errors.New("获取上游模型目录失败：上游响应超过大小限制")
		}
		return nil, errors.New("获取上游模型目录失败：上游响应读取失败")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("获取上游模型目录失败：上游返回 HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("获取上游模型目录失败：上游响应不是有效 JSON")
	}
	return modelIDsFromPayload(payload), nil
}

// modelIDsFromPayload ports accountModelCatalogIdsFromPayload
// (account-test-success-evidence.ts:43-52): data[].id or models[].name (the
// models/ prefix stripped), deduplicated in first-seen order, empty dropped.
func modelIDsFromPayload(payload any) []string {
	record, _ := payload.(map[string]any)
	if record == nil {
		return []string{}
	}
	values := []string{}
	if data, ok := record["data"].([]any); ok {
		for _, item := range data {
			entry, _ := item.(map[string]any)
			if entry == nil {
				continue
			}
			values = append(values, textValue(entry["id"]))
		}
	} else if models, ok := record["models"].([]any); ok {
		for _, item := range models {
			entry, _ := item.(map[string]any)
			if entry == nil {
				continue
			}
			name := textValue(entry["name"])
			name = strings.TrimPrefix(name, "models/")
			values = append(values, name)
		}
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		ids = append(ids, trimmed)
	}
	return ids
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}

// IntersectModelIDs ports intersectAccountUpstreamModelCatalogs
// (account-model-catalog-refresh.service.ts:102-112): the multi-Key pool
// semantics keep only the models every key catalog shares, preserving the
// first catalog's order. A nil/empty input yields an empty intersection like
// the Node firstCatalog guard.
func IntersectModelIDs(catalogs [][]string) []string {
	if len(catalogs) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	order := []string{}
	for _, id := range catalogs[0] {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, id)
	}
	for _, catalog := range catalogs[1:] {
		present := map[string]bool{}
		for _, id := range catalog {
			present[id] = true
		}
		kept := order[:0]
		for _, id := range order {
			if present[id] {
				kept = append(kept, id)
			}
		}
		order = kept
		if len(order) == 0 {
			break
		}
	}
	return order
}
