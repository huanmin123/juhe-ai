package gatewaybody

import "testing"

// BUG-0280: the compaction flag is a request-level fact established by the
// ingress scan. ReplaceGatewayJSONBody rebuilds the BodyState, and the flag
// must survive that rebuild even when the rewritten body no longer carries
// the trigger item (protocol bridge rewrites, model mapping).
func TestReplaceGatewayJSONBodyPreservesCodexCompactionTrigger(t *testing.T) {
	t.Run("flag survives body replacement", func(t *testing.T) {
		req := &Request{State: &BodyState{ContentType: "application/json", CodexCompactionTrigger: true}}
		ReplaceGatewayJSONBody(req, map[string]any{"model": "gpt-5.6"})
		if !req.State.CodexCompactionTrigger {
			t.Fatalf("compaction flag must survive the body rebuild")
		}
		if req.State.JSONParseStatus != JSONParseStatusParsed {
			t.Fatalf("status = %v", req.State.JSONParseStatus)
		}
		if string(req.RawBody) != `{"model":"gpt-5.6"}` {
			t.Fatalf("raw body = %q", req.RawBody)
		}
	})

	t.Run("flag survives model rewrite", func(t *testing.T) {
		req := &Request{State: &BodyState{ContentType: "application/json", CodexCompactionTrigger: true}}
		ReplaceGatewayJSONBody(req, map[string]any{"model": "gpt-5.6"})
		if !ReplaceGatewayJSONBodyModel(req, "gpt-6-astra", nil) {
			t.Fatalf("expected model replacement")
		}
		if !req.State.CodexCompactionTrigger {
			t.Fatalf("compaction flag must survive the model rewrite")
		}
	})

	t.Run("absent flag stays false", func(t *testing.T) {
		req := &Request{State: &BodyState{ContentType: "application/json"}}
		ReplaceGatewayJSONBody(req, map[string]any{"model": "gpt-5.6"})
		if req.State.CodexCompactionTrigger {
			t.Fatalf("flag must stay false when it was never set")
		}
	})

	t.Run("nil state tolerated", func(t *testing.T) {
		fresh := &Request{ContentTypeHeader: "application/json"}
		ReplaceGatewayJSONBody(fresh, map[string]any{"a": 1.0})
		if fresh.State == nil || fresh.State.CodexCompactionTrigger {
			t.Fatalf("nil-state rewrite must build a state without the flag")
		}
	})
}
