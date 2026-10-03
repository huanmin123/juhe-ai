package gatewaycodex

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

// BUG-0280 cross-package contract: after a bridge rewrite drops the literal
// compaction_trigger item from the body, recognition must still hold via the
// request-level flag that survived the BodyState rebuild.
func TestCodexCompactionExpectedSurvivesBridgeBodyRewrite(t *testing.T) {
	t.Run("recognition survives a trigger-free rewrite", func(t *testing.T) {
		req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
		req.Body = &gatewaybody.Request{
			RawBody: []byte(`{"input":[{"type":"compaction_trigger"}]}`),
			State:   &gatewaybody.BodyState{ContentType: "application/json", CodexCompactionTrigger: true},
		}
		gatewaybody.ReplaceGatewayJSONBody(req.Body, map[string]any{"model": "gpt-6-astra"})
		if !req.Body.State.CodexCompactionTrigger {
			t.Fatalf("flag must survive the rebuild")
		}
		if !CodexCompactionExpectedForRequest(req) {
			t.Fatalf("compaction recognition must survive a bridge rewrite that dropped the trigger item")
		}
	})

	t.Run("rewritten body without the flag stays unrecognized", func(t *testing.T) {
		req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
		req.Body = &gatewaybody.Request{
			RawBody: []byte(`{"model":"gpt-5.6"}`),
			State:   &gatewaybody.BodyState{ContentType: "application/json"},
		}
		gatewaybody.ReplaceGatewayJSONBody(req.Body, map[string]any{"model": "gpt-6-astra"})
		if CodexCompactionExpectedForRequest(req) {
			t.Fatalf("rewrite without the flag must not be treated as compaction")
		}
	})
}
