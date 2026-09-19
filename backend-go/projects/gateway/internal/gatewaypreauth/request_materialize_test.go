package gatewaypreauth

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

// 2026-09-18 热路径加固：MaterializedParsedJSONObjectBody 首次调用经共享有界
// parser 物化请求体并写入 request 级缓存，后续调用（每个候选账户 attempt）
// 复用同一对象，不再逐次全量 Unmarshal。

func TestMaterializedParsedJSONObjectBodyCachesOnRequest(t *testing.T) {
	parser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{})
	req := &GatewayRequest{Body: &gatewaybody.Request{RawBody: []byte(`{"model":"m1","stream":false}`)}}

	first := req.MaterializedParsedJSONObjectBody(parser)
	if first == nil || first["model"] != "m1" {
		t.Fatalf("first materialization must return the parsed object, got %#v", first)
	}
	if gatewaybody.GatewayJSONObjectBody(req.Body) == nil {
		t.Fatal("materialization must populate the request-scoped parse cache")
	}

	second := req.MaterializedParsedJSONObjectBody(parser)
	if second == nil {
		t.Fatal("second call must hit the request-scoped cache")
	}
	// Same cached map: a mutation through the first reference is visible via
	// the second (reference identity, not an equal copy).
	first["__probe"] = true
	if second["__probe"] != true {
		t.Fatal("second call must reuse the cached map instance")
	}
}

func TestMaterializedParsedJSONObjectBodyGuards(t *testing.T) {
	parser := gatewaybody.NewJSONParser(gatewaybody.JSONParserOptions{})

	// nil parser keeps the read-only path.
	req := &GatewayRequest{Body: &gatewaybody.Request{RawBody: []byte(`{"a":1}`)}}
	if got := req.MaterializedParsedJSONObjectBody(nil); got != nil {
		t.Fatalf("nil parser must return nil, got %#v", got)
	}

	// Empty raw body.
	empty := &GatewayRequest{Body: &gatewaybody.Request{}}
	if got := empty.MaterializedParsedJSONObjectBody(parser); got != nil {
		t.Fatalf("empty body must return nil, got %#v", got)
	}

	// Non-object JSON falls back to nil (driver-side contract unchanged).
	scalar := &GatewayRequest{Body: &gatewaybody.Request{RawBody: []byte(`"scalar"`)}}
	if got := scalar.MaterializedParsedJSONObjectBody(parser); got != nil {
		t.Fatalf("non-object JSON must return nil, got %#v", got)
	}

	// nil request / nil body are inert.
	var nilReq *GatewayRequest
	if got := nilReq.MaterializedParsedJSONObjectBody(parser); got != nil {
		t.Fatalf("nil request must return nil, got %#v", got)
	}
	nilBody := &GatewayRequest{}
	if got := nilBody.MaterializedParsedJSONObjectBody(parser); got != nil {
		t.Fatalf("nil body must return nil, got %#v", got)
	}
}
