package main

import (
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 2026-09-18 热路径加固：派发循环此前对每个候选账户完整重新 json.Unmarshal
// 请求体（ParsedBodyAvailable 恒 false，物化路径无生产调用者）。本组测试钉住
// 一次物化、全请求复用的契约。

func newBodyParseTestRequest(rawBody string) *gatewaypreauth.GatewayRequest {
	return &gatewaypreauth.GatewayRequest{
		Body: &gatewaybody.Request{RawBody: []byte(rawBody)},
	}
}

func TestMaterializedParsedBodyParsesOnceAndReusesCache(t *testing.T) {
	parser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{})
	driver := newChainProviderDriver()
	driver.bodyParser = parser
	req := newBodyParseTestRequest(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	first := driver.materializedParsedJSONObjectBody(req)
	if first == nil || first["model"] != "gpt-test" {
		t.Fatalf("first materialization must return the parsed object, got %#v", first)
	}
	// The parse cache is visible through the public read accessor only after
	// a successful materialization.
	if gatewaybody.GatewayJSONObjectBody(req.Body) == nil {
		t.Fatal("materialization must populate the request-scoped parse cache")
	}

	second := driver.materializedParsedJSONObjectBody(req)
	if second == nil {
		t.Fatal("second call must hit the request-scoped cache")
	}
	// Parse-once: both calls return the identical map (not an equal copy).
	if reflect.ValueOf(second).Pointer() != reflect.ValueOf(first).Pointer() {
		t.Fatal("second call must return the same cached map instance")
	}
}

func TestMaterializedParsedBodyNilParserKeepsFallback(t *testing.T) {
	driver := newChainProviderDriver()
	req := newBodyParseTestRequest(`{"model":"gpt-test"}`)
	if parsed := driver.materializedParsedJSONObjectBody(req); parsed != nil {
		t.Fatalf("nil parser must keep the read-only path (nil), got %#v", parsed)
	}
}

func TestMaterializedParsedBodyNonObjectJSONReturnsNil(t *testing.T) {
	parser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{})
	driver := newChainProviderDriver()
	driver.bodyParser = parser
	req := newBodyParseTestRequest(`[1,2,3]`)
	if parsed := driver.materializedParsedJSONObjectBody(req); parsed != nil {
		t.Fatalf("non-object JSON must return nil (driver fallback contract), got %#v", parsed)
	}
}

func TestMaterializedParsedBodyInvalidJSONReturnsNil(t *testing.T) {
	parser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{})
	driver := newChainProviderDriver()
	driver.bodyParser = parser
	req := newBodyParseTestRequest(`{not-json`)
	if parsed := driver.materializedParsedJSONObjectBody(req); parsed != nil {
		t.Fatalf("invalid JSON must return nil, got %#v", parsed)
	}
}
