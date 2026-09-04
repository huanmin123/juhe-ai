package gatewaybody

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Per-request body state, mirroring the GatewayRawBodyRequest fields the
// Node body pipeline owns (request/body.ts + request/json-parser.ts):
// rawBody, body, gatewayRequestBody, gatewayParsedJsonBody(+Promise),
// gatewayUpstreamBodyCache and the in-flight lease.

// UpstreamBodyCache mirrors gatewayUpstreamBodyCache.
type UpstreamBodyCache struct {
	PassthroughBody []byte
}

// Request carries the gateway body state of one request across the pipeline.
// Capture populates it; downstream slices (adapters, dispatch) consume it.
type Request struct {
	// RawBody mirrors request.rawBody.
	RawBody []byte
	// Body mirrors request.body (nil after capture until materialization).
	Body any
	// State mirrors request.gatewayRequestBody.
	State *BodyState
	// UpstreamBodyCache mirrors request.gatewayUpstreamBodyCache.
	UpstreamBodyCache *UpstreamBodyCache
	// ContentTypeHeader carries the raw Content-Type header so
	// ReplaceGatewayJSONBody can mirror the Node header fallback.
	ContentTypeHeader string
	// Serialized carries the parsed-object association of RawBody
	// (serialized-json-body.ts WeakMap adaptation).
	Serialized *SerializedBody
	// Lease is the in-flight bytes lease; released via ReleaseInFlight.
	Lease *InFlightLease

	ctx             context.Context
	parsedAvailable bool
	parsedBody      any
	materialization *materialization
}

// RequestContext returns the request lifecycle context attached at capture
// time (abort wiring of the Node materialization promise).
func (req *Request) RequestContext() context.Context {
	if req.ctx != nil {
		return req.ctx
	}
	return context.Background()
}

// ReleaseInFlight mirrors releaseGatewayRequestBodyInFlightBytes.
func (req *Request) ReleaseInFlight() {
	req.Lease.Release()
	req.Lease = nil
}

func sameSlice(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

type materialization struct {
	raw    []byte
	wait   chan struct{}
	mu     sync.Mutex
	result jsonWorkerResult
}

// ParseRequestJSONBody mirrors parseGatewayRequestJsonBody: returns the
// parsed body, reusing an existing parsed object or a shared in-flight
// materialization for the same raw body. timeout <= 0 uses the Node
// 30s default; a shorter timeout fails with
// JSONWorkerMaterializationTimeoutError; ctx cancellation fails with
// ErrCanceled. Both copies come from Node.
func (p *JSONParser) ParseRequestJSONBody(ctx context.Context, req *Request, timeout time.Duration) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if req.Body != nil {
			if _, isBuffer := req.Body.([]byte); !isBuffer {
				bindGatewayRequestParsedObject(req, req.Body)
				req.parsedAvailable = true
				req.parsedBody = req.Body
				return req.Body, nil
			}
		}
		if req.parsedAvailable {
			bindGatewayRequestParsedObject(req, req.parsedBody)
			return req.parsedBody, nil
		}
		rawBody := req.RawBody
		if len(rawBody) == 0 {
			return nil, nil
		}
		existing := req.materialization
		if existing == nil || !sameSlice(existing.raw, rawBody) {
			requestCtx := req.RequestContext()
			if ctx.Err() != nil || requestCtx.Err() != nil {
				return nil, fmt.Errorf("%w", ErrCanceled)
			}
			existing = p.startMaterialization(requestCtx, rawBody)
			req.materialization = existing
		}
		result, err := p.awaitMaterialization(ctx, existing, timeout)
		if !sameSlice(req.RawBody, rawBody) {
			continue
		}
		if err != nil {
			return nil, err
		}
		req.Body = result
		req.parsedAvailable = true
		req.parsedBody = result
		bindGatewayRequestParsedObject(req, result)
		if req.State != nil && req.State.IsJSON {
			req.State.JSONParseStatus = JSONParseStatusParsed
		}
		if req.materialization == existing {
			req.materialization = nil
		}
		return result, nil
	}
}

// startMaterialization mirrors startGatewayRequestJsonMaterialization: one
// shared worker job per raw body, aborted with the request lifecycle.
func (p *JSONParser) startMaterialization(requestCtx context.Context, raw []byte) *materialization {
	mat := &materialization{raw: raw, wait: make(chan struct{})}
	go func() {
		value, err := p.ParseJSONBody(requestCtx, raw, GatewayRequestJSONMaterializationTimeout)
		mat.mu.Lock()
		mat.result = jsonWorkerResult{value: value, err: err}
		mat.mu.Unlock()
		close(mat.wait)
	}()
	return mat
}

// awaitMaterialization mirrors awaitGatewayRequestJsonMaterialization.
func (p *JSONParser) awaitMaterialization(ctx context.Context, mat *materialization, timeout time.Duration) (any, error) {
	normalized := timeout
	if timeout <= 0 {
		normalized = GatewayRequestJSONMaterializationTimeout
	}
	select {
	case <-mat.wait:
		return mat.readResult()
	default:
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w", ErrCanceled)
	}
	// Node: without a signal and with the default timeout the raw
	// materialization promise is awaited directly, so the 30s race belongs
	// to the worker job timeout (its own error copy).
	if normalized >= GatewayRequestJSONMaterializationTimeout && isContextWithoutCancellation(ctx) {
		<-mat.wait
		return mat.readResult()
	}
	timer := time.NewTimer(normalized)
	defer timer.Stop()
	select {
	case <-mat.wait:
		return mat.readResult()
	case <-ctx.Done():
		return nil, fmt.Errorf("%w", ErrCanceled)
	case <-timer.C:
		return nil, &JSONWorkerMaterializationTimeoutError{TimeoutMS: int(normalized / time.Millisecond)}
	}
}

// isContextWithoutCancellation reports whether ctx is the plain Background
// context (the "no signal" case of awaitGatewayRequestJsonMaterialization).
func isContextWithoutCancellation(ctx context.Context) bool {
	return ctx == context.Background()
}

func (mat *materialization) readResult() (any, error) {
	mat.mu.Lock()
	defer mat.mu.Unlock()
	return mat.result.value, mat.result.err
}

func bindGatewayRequestParsedObject(req *Request, parsed any) {
	if len(req.RawBody) == 0 {
		return
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return
	}
	req.Serialized = BindGatewaySerializedJSONObject(req.RawBody, object)
}

// GatewayJSONObjectBody mirrors gatewayJsonObjectBody: the request body when
// it is a JSON object, falling back to the parsed cache.
func GatewayJSONObjectBody(req *Request) map[string]any {
	if req == nil {
		return nil
	}
	if req.Body != nil {
		if _, isBuffer := req.Body.([]byte); !isBuffer {
			if object, ok := req.Body.(map[string]any); ok {
				return object
			}
			return nil
		}
	}
	if req.parsedAvailable {
		if object, ok := req.parsedBody.(map[string]any); ok {
			return object
		}
	}
	return nil
}

// ReplaceGatewayJSONBody mirrors replaceGatewayJsonBody.
func ReplaceGatewayJSONBody(req *Request, body map[string]any) {
	contentType := ""
	if req.State != nil {
		contentType = req.State.ContentType
	}
	if contentType == "" {
		if req.ContentTypeHeader != "" {
			contentType = req.ContentTypeHeader
		} else {
			contentType = "application/json"
		}
	}
	serialized := SerializeGatewayJSONObject(body)
	req.RawBody = serialized.Raw
	req.Body = body
	req.parsedAvailable = true
	req.parsedBody = body
	req.materialization = nil
	req.UpstreamBodyCache = nil
	req.Serialized = serialized
	req.State = CreateBodyState(BodyStateInput{
		RawBody:         serialized.Raw,
		ContentType:     contentType,
		JSONParseStatus: JSONParseStatusParsed,
		ParsedBody:      body,
	})
}

// ReplaceGatewayJSONBodyModel mirrors replaceGatewayJsonBodyModel.
func ReplaceGatewayJSONBodyModel(req *Request, model string, body map[string]any) bool {
	targetModel := trimJSSpace(model)
	if targetModel == "" {
		return false
	}
	currentBody := body
	if currentBody == nil {
		currentBody = GatewayJSONObjectBody(req)
	}
	if currentBody == nil {
		return false
	}
	nextBody := copyJSONObject(currentBody)
	nextBody["model"] = targetModel
	ReplaceGatewayJSONBody(req, nextBody)
	return true
}

// GatewayRequestBodyForcesImageGeneration mirrors
// gatewayRequestBodyForcesImageGeneration.
func GatewayRequestBodyForcesImageGeneration(req *Request) bool {
	if req != nil && req.State != nil && req.State.ImageGenerationForced {
		return true
	}
	if req == nil {
		return false
	}
	return RequestBodyForcesImageGeneration(req.Body)
}

// DowngradeGatewayAutoImageGenerationTool mirrors
// downgradeGatewayAutoImageGenerationTool.
func DowngradeGatewayAutoImageGenerationTool(req *Request) ImageGenerationToolDowngradeResult {
	result, nextBody := DowngradeAutoImageGenerationToolsInBody(GatewayJSONObjectBody(req))
	if result.Downgraded && nextBody != nil {
		ReplaceGatewayJSONBody(req, nextBody)
	}
	return result
}

// BuildGatewayRequestBodySummary mirrors buildGatewayRequestBodySummary.
func BuildGatewayRequestBodySummary(req *Request) map[string]any {
	if req == nil {
		return nil
	}
	state := req.State
	if state == nil || state.RawBodyBytes <= state.JSONParseWarningBytes {
		return nil
	}
	summary := map[string]any{
		"rawBodyBytes":          state.RawBodyBytes,
		"contentType":           state.ContentType,
		"jsonParseStatus":       string(state.JSONParseStatus),
		"jsonParseWarningBytes": state.JSONParseWarningBytes,
		"imageGeneration":       state.ImageGeneration,
		"imageGenerationForced": state.ImageGenerationForced,
	}
	if state.Model != nil {
		summary["model"] = *state.Model
	} else if object := GatewayJSONObjectBody(req); object != nil {
		if model, ok := object["model"].(string); ok {
			summary["model"] = model
		}
	}
	if state.Stream != nil {
		summary["stream"] = *state.Stream
	} else if object := GatewayJSONObjectBody(req); object != nil {
		if stream, ok := object["stream"].(bool); ok {
			summary["stream"] = stream
		}
	}
	return map[string]any{"_gatewayBody": summary}
}
