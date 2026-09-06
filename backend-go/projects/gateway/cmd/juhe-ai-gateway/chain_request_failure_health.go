package main

// Request-failure account health-check dispatch assembly: the port of the
// archived response/request-failure-health-check.ts + the
// internal-api/account-health-check-dispatch.service.ts publish path.
//
// Node contract (request-failure-health-check.ts):
//   - trafficSource !== 'gateway' → no dispatch;
//   - one dispatched health check per request (the dispatched Symbol on the
//     express request throttles the candidate-failover loop and keeps the
//     failed-response and transport-failure branches from double firing);
//   - dispatchAccountHealthCheck(accountId, 'request_failure') publishes the
//     J1 probe request fact (internal-api service, fire-and-forget).
//
// Go two-process topology: the gateway process cannot import the jobs
// internal-api package (mirrors the compose_account_test_dispatch.go module
// boundary), so the publish rides the same loopback HMAC bridge pattern:
// POST {JobsInternalURL}/__aiinternal__/v1/account-health-check/dispatch,
// HMAC-SHA256 over "juhe-ai:account-health-check-dispatch:v1\n" + raw body,
// X-Juhe-Ai-Signature: v1=<hex>, loopback-only on the jobs side.
//
// Registered gap (logged, not silent): the Go jobs process has not mounted
// this health-check internal route yet (jobs internal/internalapi dispatch.go
// carries only the account-test pair). Until that route lands, every dispatch
// observes the jobs 404 → the bridge returns rejected → the dispatch is
// skipped and warned exactly like the unavailable-worker path of the
// account-test bridge. The wire contract below already mirrors the J1
// projection (jobs internalapi.HealthCheckSourceFence / the source_fence
// payload field names) so the route can adopt it without a gateway change.

import (
	"bytes"
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// chainHealthDispatchSignatureDomain mirrors the bridge signature domain
// (compose_account_test_dispatch.go pattern; the jobs-side verification
// reuses the same domain once the route mounts).
const chainHealthDispatchSignatureDomain = "juhe-ai:account-health-check-dispatch:v1\n"

// chainHealthDispatchPath is the loopback route the bridge posts to
// (/__aiinternal__ prefix + v1 route, mirroring the account-test pair).
const chainHealthDispatchPath = "/__aiinternal__/v1/account-health-check/dispatch"

// chainRequestFailureReason mirrors AccountHealthCheckTriggerReason
// 'request_failure' — the only reason this port dispatches.
const chainRequestFailureReason = "request_failure"

// chainJobsHealthDispatchBridge posts one signed health-check dispatch to the
// jobs internal-api loopback origin.
type chainJobsHealthDispatchBridge struct {
	baseURL string
	secret  string
	client  *http.Client
}

// chainHealthDispatchRequest is the wire body: version + accountId + reason,
// with the optional sourceFence projection of the probe envelope.
type chainHealthDispatchRequest struct {
	Version     int                             `json:"version"`
	AccountID   string                          `json:"accountId"`
	Reason      string                          `json:"reason"`
	TraceID     string                          `json:"traceId,omitempty"`
	SourceFence *chainHealthDispatchSourceFence `json:"sourceFence,omitempty"`
}

// chainHealthDispatchSourceFence mirrors the jobs internalapi
// HealthCheckSourceFence narrow projection (snake_case, matching the J1
// request-file source_fence field names).
type chainHealthDispatchSourceFence struct {
	StateKey         string `json:"state_key"`
	AccountID        string `json:"account_id"`
	SourceGeneration int64  `json:"source_generation"`
	SourceFenceID    string `json:"source_fence_id"`
	RuntimeKey       string `json:"runtime_key"`
	ProbeGeneration  int64  `json:"probe_generation"`
	ConfigRevision   int64  `json:"config_revision"`
}

// signChainHealthDispatch mirrors the account-test bridge signature scheme
// byte-for-byte over the health-check domain.
func signChainHealthDispatch(secret string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(chainHealthDispatchSignatureDomain))
	_, _ = mac.Write(rawBody)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// dispatchOutcome mirrors the Node AccountHealthCheckDispatchOutcome pair
// (outcome queued|rejected + decisionCode queued|dispatch_rejected|
// input_unavailable) collapsed onto the boolean + reason the probe service
// consumes.
func (b *chainJobsHealthDispatchBridge) dispatch(accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	if b == nil || strings.TrimSpace(b.baseURL) == "" || strings.TrimSpace(b.secret) == "" {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	payload := chainHealthDispatchRequest{
		Version:     1,
		AccountID:   normalizedID,
		Reason:      strings.TrimSpace(reason),
		TraceID:     strings.TrimSpace(traceID),
		SourceFence: sourceFence,
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("健康检查派发请求体编码失败", "event", "account_health_check_dispatch_encode_failed", "error", err)
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.baseURL, "/")+chainHealthDispatchPath, bytes.NewReader(rawBody))
	if err != nil {
		slog.Warn("健康检查派发请求构造失败", "event", "account_health_check_dispatch_unavailable", "error", err)
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", signChainHealthDispatch(b.secret, rawBody))
	client := b.client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("健康检查派发未送达 jobs internal-api",
				"event", "account_health_check_dispatch_unavailable", "accountId", normalizedID, "error", err)
		}
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusAccepted {
		slog.Warn("jobs internal-api 拒绝健康检查派发",
			"event", "account_health_check_dispatch_rejected",
			"accountId", normalizedID, "statusCode", response.StatusCode)
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchQueued, DecisionCode: "queued", TargetRole: "go-jobs"}
}

// chainRequestDispatchMarks approximates the Node per-request dispatched
// Symbol (a WeakMap entry dying with the request object): Go has no weak map,
// so the marks live in a bounded FIFO keyed by the request view pointer. The
// bound (4096) far exceeds the live request window of one process; a mark
// evicted only after thousands of later requests can at worst admit one
// duplicate health-check dispatch, which the jobs side dedupes idempotently.
type chainRequestDispatchMarks struct {
	mu    sync.Mutex
	cap   int
	keys  map[*gatewaypreauth.GatewayRequest]*list.Element
	order *list.List
}

func newChainRequestDispatchMarks(capacity int) *chainRequestDispatchMarks {
	if capacity <= 0 {
		capacity = 4096
	}
	return &chainRequestDispatchMarks{cap: capacity, keys: map[*gatewaypreauth.GatewayRequest]*list.Element{}, order: list.New()}
}

// marked reports whether the request already dispatched its health check.
func (m *chainRequestDispatchMarks) marked(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.keys[req]
	return ok
}

// mark records the request; the oldest entries fall out at the capacity.
func (m *chainRequestDispatchMarks) mark(req *gatewaypreauth.GatewayRequest) {
	if req == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.keys[req]; ok {
		return
	}
	m.keys[req] = m.order.PushBack(req)
	for len(m.keys) > m.cap {
		oldest := m.order.Front()
		if oldest == nil {
			break
		}
		delete(m.keys, m.order.Remove(oldest).(*gatewaypreauth.GatewayRequest))
	}
}

// chainRequestFailureHealthDispatcher is the port of
// dispatchRequestFailureAccountHealthCheck: the gateway-traffic gate, the
// per-request throttle and the bridge publish in the Node order.
type chainRequestFailureHealthDispatcher struct {
	bridge *chainJobsHealthDispatchBridge
	marks  *chainRequestDispatchMarks
}

func newChainRequestFailureHealthDispatcher(baseURL, secret string, client *http.Client) *chainRequestFailureHealthDispatcher {
	return &chainRequestFailureHealthDispatcher{
		bridge: &chainJobsHealthDispatchBridge{baseURL: baseURL, secret: secret, client: client},
		marks:  newChainRequestDispatchMarks(4096),
	}
}

// DispatchRequestFailureAccountHealthCheck mirrors
// dispatchRequestFailureAccountHealthCheck(req, trafficSource, accountId):
// non-gateway traffic and an already-dispatched request both return false
// without touching the bridge; a queued dispatch marks the request.
func (d *chainRequestFailureHealthDispatcher) DispatchRequestFailureAccountHealthCheck(req *gatewaypreauth.GatewayRequest, trafficSource, accountID string) bool {
	if d == nil || d.bridge == nil {
		return false
	}
	if trafficSource != gatewayTrafficSource {
		return false
	}
	if d.marks.marked(req) {
		return false
	}
	outcome := d.bridge.dispatch(accountID, chainRequestFailureReason, "", nil)
	if outcome.Outcome == gatewaycodex.HealthDispatchRejected {
		return false
	}
	d.marks.mark(req)
	return true
}

// dispatchWithOutcome adapts the dispatcher onto the
// gatewaycodex.AccountHealthCheckDispatchFunc seam (the
// TurnAvoidanceProbeService DefaultDispatch): the probe envelope carries the
// source fence so the jobs worker settles the exact registered fence.
func (d *chainRequestFailureHealthDispatcher) dispatchWithOutcome(accountID, reason, traceID string, sourceFence *gatewaycodex.SourceProbeFence) gatewaycodex.HealthCheckDispatchOutcome {
	if d == nil || d.bridge == nil {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	var fence *chainHealthDispatchSourceFence
	if sourceFence != nil {
		fence = &chainHealthDispatchSourceFence{
			StateKey:         sourceFence.StateKey,
			AccountID:        sourceFence.AccountID,
			SourceGeneration: sourceFence.SourceGeneration,
			SourceFenceID:    sourceFence.SourceFenceID,
			RuntimeKey:       sourceFence.RuntimeKey,
			ProbeGeneration:  sourceFence.ProbeGeneration,
			ConfigRevision:   sourceFence.ConfigRevision,
		}
	}
	return d.bridge.dispatch(accountID, reason, traceID, fence)
}
