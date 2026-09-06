package gatewaydispatch

// The retry_with_compatibility_recovery bridge: the failure dispatcher
// returns the sanitized body variant and the engine must replay the SAME
// account with that body (Node upstream-dispatch.ts:1486-1490 mutates its
// local `body` before `continue`) and register the retry attempt under the
// recovery semantic-retry id (upstream-dispatch.ts:1257/1488
// activeSemanticRetryId) so the same-credential replay stays allowed.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// semanticRecoveryDispatcher delegates the request-error branch to the shared
// fake and answers every failed response with a sanitized-body recovery.
type semanticRecoveryDispatcher struct {
	inner         *fakeFailureDispatcher
	mu            sync.Mutex
	recoveryCalls int
}

func (d *semanticRecoveryDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	d.mu.Lock()
	d.recoveryCalls++
	d.mu.Unlock()
	if input.Response != nil && input.Response.Body != nil {
		_, _ = io.Copy(io.Discard, input.Response.Body)
		_ = input.Response.Body.Close()
	}
	// Mirror the codex encrypted-content cleanup: drop the reasoning item the
	// upstream just rejected.
	sanitized := bytes.ReplaceAll(
		input.RequestBody,
		[]byte(`{"type":"reasoning","encrypted_content":"rejected-payload"},`),
		nil,
	)
	if bytes.Equal(sanitized, input.RequestBody) {
		panic("recovery test expects a removable encrypted_content item")
	}
	return FailedUpstreamResponseResult{
		Action:      FailedResponseActionRetryWithCompatibilityRecovery,
		FailureKind: "compatibility_recovery",
		LastAttempt: &UpstreamAttempt{
			AccountID:   input.Account.ID,
			AccountName: input.Account.Name,
			UpstreamURL: input.UpstreamURL,
			Status:      input.Response.Status(),
			HasStatus:   true,
		},
		Recovery: CompatibilityRecovery{
			Body:            sanitized,
			SemanticRetryID: "codex_encrypted_content_cleanup:invalid_encrypted_content",
		},
	}, nil
}

func (d *semanticRecoveryDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	return d.inner.HandleUpstreamRequestError(ctx, input)
}

func (d *semanticRecoveryDispatcher) IsOpaqueUpstreamFailoverAllowed(_ *gatewaypreauth.GatewayRequest) bool {
	return false
}

func TestFetchFirstAvailableUpstreamCompatibilityRecoveryReplaysSanitizedBody(t *testing.T) {
	var mu sync.Mutex
	var upstreamBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamBodies = append(upstreamBodies, string(raw))
		mu.Unlock()
		if strings.Contains(string(raw), "encrypted_content") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Encrypted content could not be decoded","code":"invalid_encrypted_content"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-after-recovery"}`))
	}))
	defer server.Close()

	const requestBody = `{"model":"gpt-test","stream":false,"input":[{"type":"reasoning","encrypted_content":"rejected-payload"},{"type":"message","role":"user","content":"hi"}]}`
	engine, driver, inner := newTestEngine(t)
	recovery := &semanticRecoveryDispatcher{inner: inner}
	engine.FailureDispatcher = recovery
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/responses"},
	}
	driver.partsBody = requestBody
	req := newTestRequest(t, requestBody)
	accounts := testAccounts("a-1")
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, accounts))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if !result.Response.OK() {
		t.Fatalf("status = %d want 200 after the recovery replay", result.Response.Status())
	}
	if recovery.recoveryCalls != 1 {
		t.Fatalf("recovery decisions = %d want 1", recovery.recoveryCalls)
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream hits = %d want 2 (rejected body + sanitized replay)", len(upstreamBodies))
	}
	if !strings.Contains(upstreamBodies[0], "encrypted_content") {
		t.Fatalf("first attempt body must be the original: %s", upstreamBodies[0])
	}
	if strings.Contains(upstreamBodies[1], "encrypted_content") {
		t.Fatalf("retry attempt must replay the sanitized body: %s", upstreamBodies[1])
	}
	if !strings.Contains(upstreamBodies[1], `"type":"message"`) {
		t.Fatalf("sanitized body must keep the surviving input items: %s", upstreamBodies[1])
	}
	// The selected result carries the recovered request body (Node
	// dispatchResult requestBody = body), not the original.
	if strings.Contains(string(result.RequestBody), "encrypted_content") {
		t.Fatalf("selected result request body must be the recovered variant: %s", result.RequestBody)
	}
}
