package main

// Image-permission downgrade warn-log regression (image-permission-preflight.ts:
// 62-68) with the preauth service test collaborators.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// test collaborators
// ---------------------------------------------------------------------------

// chainPreauthStubRuntimeCache satisfies gatewaypreauth.RuntimeCacheReader
// without touching a database (the gate only reads the Observability seam).
type chainPreauthStubRuntimeCache struct{}

func (chainPreauthStubRuntimeCache) ReadCachedGatewayRuntimeAsync(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}
func (chainPreauthStubRuntimeCache) ReadCachedGatewayRuntimeStaticAsync(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}
func (chainPreauthStubRuntimeCache) SelectGatewayRuntimeForDispatchAsync(_ context.Context, static gatewayruntimecache.GatewayRuntime, _ gatewayruntimecache.SelectGatewayRuntimeInput) (gatewayruntimecache.GatewayRuntime, error) {
	return static, nil
}
func (chainPreauthStubRuntimeCache) ReadCachedGatewaySettingsAsync(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}
func (chainPreauthStubRuntimeCache) ResolveCachedGroupUsageAccessMetadataAsync(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListFreshOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListRecoverableUnavailableOpenAIAccountsForGroupAsync(context.Context, string, string, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions, *int64) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedActiveResponseInspectionPoliciesForAccountsAsync(context.Context, []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}
func (chainPreauthStubRuntimeCache) ListCachedProviderModelCatalogAsync(context.Context, gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	return nil, nil
}

type capturedSpeedFirstStage struct {
	stage   string
	fields  map[string]any
	outcome string
}

type capturedWarnLine struct {
	event   string
	fields  map[string]any
	message string
}

// chainCapturedObservability records the request-stage lines and warn events
// the preflight adapters emit.
type chainCapturedObservability struct {
	// mu guards the captured slices: stages may be logged from goroutines
	// concurrent with the assertions.
	mu     sync.Mutex
	stages []capturedSpeedFirstStage
	warns  []capturedWarnLine
}

func (o *chainCapturedObservability) Logger() gatewaypreauth.Logger { return o }
func (o *chainCapturedObservability) Warn(event string, fields map[string]any, message string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.warns = append(o.warns, capturedWarnLine{event: event, fields: fields, message: message})
}
func (o *chainCapturedObservability) TraceID() string { return "trace_chain_test" }
func (o *chainCapturedObservability) CreateTraceID() string {
	return "trace_chain_test"
}
func (o *chainCapturedObservability) SanitizeURLForLog(value string) string { return value }
func (o *chainCapturedObservability) LogRequestStage(stage string, fields map[string]any, outcome string, _ time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stages = append(o.stages, capturedSpeedFirstStage{stage: stage, fields: fields, outcome: outcome})
}

// snapshotStages copies the captured stages under the lock: assertions may
// run while another goroutine still logs.
func (o *chainCapturedObservability) snapshotStages() []capturedSpeedFirstStage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]capturedSpeedFirstStage(nil), o.stages...)
}

// ---------------------------------------------------------------------------
// image-permission downgrade warn log (image-permission-preflight.ts:62-68)
// ---------------------------------------------------------------------------

func TestChainImagePreflightLogsDowngradedWarn(t *testing.T) {
	obs := &chainCapturedObservability{}
	service, err := gatewaypreauth.New(gatewaypreauth.Service{
		RuntimeCache:  chainPreauthStubRuntimeCache{},
		Observability: obs,
		Clock:         gatewaypreauth.SystemClock{},
	})
	if err != nil {
		t.Fatalf("create preauth service: %v", err)
	}
	preflight := &chainImagePreflight{preauth: service}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","tools":[{"type":"image_generation"}]}`))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	rawBody := []byte(`{"model":"gpt-test","tools":[{"type":"image_generation"}]}`)
	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	req.Body = &gatewaybody.Request{
		RawBody:           rawBody,
		Body:              parsed,
		ContentTypeHeader: "application/json",
		State:             &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON, ContentType: "application/json"},
	}
	capture := &chainTestAuditCapture{}
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

	result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
		Req:             req,
		Res:             res,
		AuditCapture:    capture,
		APIKeyRecord:    &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
		RequestLane:     string(gatewayproto.LaneImage),
		SystemAccountID: "sys_owner",
		APIKeyID:        "key_1",
		GroupID:         "group_1",
		Endpoint:        "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Completed {
		t.Fatalf("downgraded request must continue, result=%+v", result)
	}
	if result.RequestLane != string(gatewaypreauth.RequestLaneText) {
		t.Fatalf("requestLane=%q want text", result.RequestLane)
	}
	if len(obs.warns) != 1 {
		t.Fatalf("warns=%d want 1: %+v", len(obs.warns), obs.warns)
	}
	warn := obs.warns[0]
	if warn.event != "gateway_image_generation_tool_downgraded" {
		t.Fatalf("event=%q", warn.event)
	}
	if warn.message != "系统账户未开启图像生成，已移除 Responses auto 图像生成工具并按文本请求继续" {
		t.Fatalf("message=%q", warn.message)
	}
	if warn.fields["removedToolCount"] != 1 || warn.fields["systemAccountId"] != "sys_owner" ||
		warn.fields["apiKeyId"] != "key_1" || warn.fields["groupId"] != "group_1" {
		t.Fatalf("fields=%+v", warn.fields)
	}
}

// chainTestAuditCapture is the minimal gatewaypreauth.AuditCaptureContext.
type chainTestAuditCapture struct {
	metadata []string
}

func (c *chainTestAuditCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (c *chainTestAuditCapture) AddGatewayMetadata(label string, _ map[string]any) {
	c.metadata = append(c.metadata, label)
}
func (c *chainTestAuditCapture) Finalize(gatewaypreauth.AuditFinalizeInput) {}
