package gatewaycodex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of codex-responses/chat-bridge-state.ts: the Responses↔Chat bridge
// session state (preflight, restore, completion persistence, compact
// snapshot) plus the per-account dispatch gating.

type jsonRecord = map[string]any

// Restore / compaction failure outcomes mirror the Node string unions.
const (
	RestoreOutcomeNotFound         = "not_found"
	RestoreOutcomeExpired          = "expired"
	RestoreOutcomeBoundaryMismatch = "boundary_mismatch"
	RestoreOutcomeChainTooDeep     = "chain_too_deep"
	RestoreOutcomeChainBroken      = "chain_broken"
	RestoreOutcomePayloadUnavail   = "payload_unavailable"
)

// Request kinds / previous response kinds / dispatch modes.
const (
	RequestKindResponses = "responses"
	RequestKindCompact   = "compact"

	PreviousKindNone     = "none"
	PreviousKindInternal = "internal"
	PreviousKindExternal = "external"

	CompactDispatchBridge = "bridge"
	CompactDispatchNative = "native"
)

const (
	codexCompactionReferencePrefix     = "juhecmp.v2."
	codexInlineCompactionSummaryPrefix = "juhecmp.v1."
	maxContextChainDepth               = 64
)

var internalBridgeResponseIDPattern = regexp.MustCompile(`^resp_(?:chat_bridge|deepseek_bridge|glm_bridge|openai_bridge|openai_compatible_bridge|hybrid_chat_bridge)_`)

// CodexResponsesChatBridgeCompletion mirrors
// CodexResponsesChatBridgeCompletion.
type CodexResponsesChatBridgeCompletion struct {
	ResponseID  string
	CreatedAt   time.Time
	Model       string
	OutputItems []any
	Response    jsonRecord
}

// CodexResponsesContextRequestState mirrors
// CodexResponsesContextRequestState.
type CodexResponsesContextRequestState struct {
	RequestKind          string
	Boundary             CodexContextStateBoundary
	CanonicalBody        jsonRecord
	CurrentBody          jsonRecord
	CurrentInput         any
	MaterializedInput    any
	PreviousResponseID   string
	PreviousResponseKind string
	SessionID            string
	Restored             bool
	// MaterializedCurrentInputStartIndex nil mirrors undefined.
	MaterializedCurrentInputStartIndex *int
	ActiveBridgeAccountID              string
	LastRenderedBody                   jsonRecord
	CompactDispatchMode                string
}

// CodexResponsesChatBridgeInputRestoreResult mirrors
// CodexResponsesChatBridgeInputRestoreResult.
type CodexResponsesChatBridgeInputRestoreResult struct {
	// Outcome is 'found' | 'no_previous' | <restore failure> |
	// 'payload_unavailable'.
	Outcome       string
	Input         []any
	SessionID     string
	ResponseCount int
	ResponseID    string
}

// CodexResponsesChatBridgeCompactSnapshotResult mirrors the snapshot result.
type CodexResponsesChatBridgeCompactSnapshotResult struct {
	CompactID        string
	EncryptedContent string
}

// ChatBridgeStateConfig carries the configured storage roots.
type ChatBridgeStateConfig struct {
	CodexContextRoot string
}

// ChatBridgeStateService is the codex-responses chat bridge state slice.
type ChatBridgeStateService struct {
	config   ChatBridgeStateConfig
	store    CodexContextRowStore
	segments *SegmentStore
	clock    Clock
	// CreateID mirrors randomUUID (compact ids, fence ids).
	CreateID IDGenerator
	Logger   gatewaypreauth.Logger
	Sink     gatewaypreauth.ResponseSink
}

// NewChatBridgeStateService wires the service; every dependency stays
// explicit so tests can inject mocks.
func NewChatBridgeStateService(config ChatBridgeStateConfig, store CodexContextRowStore, segments *SegmentStore, clock Clock) (*ChatBridgeStateService, error) {
	segmentsStore := segments
	if segmentsStore == nil {
		var err error
		segmentsStore, err = NewSegmentStore(SegmentStoreConfig{Root: config.CodexContextRoot})
		if err != nil {
			return nil, err
		}
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &ChatBridgeStateService{
		config:   config,
		store:    store,
		segments: segmentsStore,
		clock:    clock,
		CreateID: RandomUUID,
	}, nil
}

// ---------------------------------------------------------------------------
// per-request state registry (Node: requestStateSymbol on the express req)
// ---------------------------------------------------------------------------

// ContextRequestStateRegistry mirrors the request-local symbol storage. The
// server layer owns one registry; Release drops the entry when the request
// finishes (the Node symbol is reclaimed by GC).
type ContextRequestStateRegistry struct {
	mu     sync.Mutex
	states map[*gatewaypreauth.GatewayRequest]*CodexResponsesContextRequestState
}

// NewContextRequestStateRegistry builds an empty registry.
func NewContextRequestStateRegistry() *ContextRequestStateRegistry {
	return &ContextRequestStateRegistry{states: map[*gatewaypreauth.GatewayRequest]*CodexResponsesContextRequestState{}}
}

// Get mirrors getCodexResponsesContextState.
func (r *ContextRequestStateRegistry) Get(req *gatewaypreauth.GatewayRequest) (*CodexResponsesContextRequestState, bool) {
	if r == nil || req == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[req]
	return state, ok
}

// Set mirrors setCodexResponsesContextStateForRequest.
func (r *ContextRequestStateRegistry) Set(req *gatewaypreauth.GatewayRequest, state *CodexResponsesContextRequestState) {
	if r == nil || req == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.states == nil {
		r.states = map[*gatewaypreauth.GatewayRequest]*CodexResponsesContextRequestState{}
	}
	r.states[req] = state
}

// Release mirrors the GC reclamation of the Node request symbol.
func (r *ContextRequestStateRegistry) Release(req *gatewaypreauth.GatewayRequest) {
	if r == nil || req == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, req)
}

// ---------------------------------------------------------------------------
// preflight
// ---------------------------------------------------------------------------

// ContextStatePreflightInput mirrors applyCodexResponsesContextStatePreflight's
// input bag (the G05 port shape).
type ContextStatePreflightInput struct {
	Req             *gatewaypreauth.GatewayRequest
	Res             gatewaypreauth.GatewayResponseWriter
	AuditCapture    gatewaypreauth.AuditCaptureContext
	UsageContext    gatewaypreauth.GatewayFailureUsageContext
	StartedAt       int64
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	GroupAccess     gatewayruntimecache.GroupUsageAccessMetadata
	Signal          context.Context
}

// ApplyContextStatePreflight mirrors applyCodexResponsesContextStatePreflight.
// completed=true means the request finished inside the preflight.
func (s *ChatBridgeStateService) ApplyContextStatePreflight(ctx context.Context, registry *ContextRequestStateRegistry, input ContextStatePreflightInput) (bool, error) {
	requestKind := ""
	if isOpenAIResponsesPostRequest(input.Req) {
		requestKind = RequestKindResponses
	} else if isOpenAIResponsesCompactPostRequest(input.Req) {
		requestKind = RequestKindCompact
	}
	if requestKind == "" {
		return false, nil
	}
	body, err := s.parseGatewayJSONObject(input.Req)
	if err != nil {
		return false, err
	}
	canonicalBody := cloneJSONMapShallow(body)
	boundary := codexContextBoundary(input.SystemAccountID, input.APIKeyID, input.GroupID, input.GroupAccess)
	previousResponseID := normalizedOptionalText(body["previous_response_id"])
	if requestKind == RequestKindCompact {
		previousResponseKind := PreviousKindNone
		if previousResponseID != "" {
			if isInternalCodexBridgeResponseID(previousResponseID) {
				previousResponseKind = PreviousKindInternal
			} else {
				previousResponseKind = PreviousKindExternal
			}
		}
		registry.Set(input.Req, &CodexResponsesContextRequestState{
			RequestKind:          requestKind,
			Boundary:             boundary,
			CanonicalBody:        canonicalBody,
			CurrentBody:          canonicalBody,
			CurrentInput:         body["input"],
			MaterializedInput:    body["input"],
			PreviousResponseID:   previousResponseID,
			PreviousResponseKind: previousResponseKind,
			Restored:             false,
		})
		input.AuditCapture.AddGatewayMetadata("codex_responses_context_state", map[string]any{
			"mode":               fmt.Sprintf("compact_%s_previous_response", previousResponseKind),
			"previousResponseId": previousResponseID,
		})
		return false, nil
	}

	compactReferenceResult, err := s.resolveCodexCompactionReferencesInInput(ctx, body["input"], boundary)
	if err != nil {
		return false, err
	}
	if compactReferenceResult.outcome != "" {
		s.sendCodexBridgeStateFailure(input, compactReferenceFailure(compactReferenceResult.outcome))
		return true, nil
	}
	materializedCurrentInput := compactReferenceResult.input
	baseState := CodexResponsesContextRequestState{
		RequestKind:          RequestKindResponses,
		Boundary:             boundary,
		CanonicalBody:        canonicalBody,
		CurrentBody:          cloneJSONMapShallowWithInput(body, materializedCurrentInput),
		CurrentInput:         materializedCurrentInput,
		MaterializedInput:    materializedCurrentInput,
		PreviousResponseID:   previousResponseID,
		PreviousResponseKind: PreviousKindNone,
		Restored:             false,
	}
	if previousResponseID == "" {
		registry.Set(input.Req, &baseState)
		input.AuditCapture.AddGatewayMetadata("codex_responses_chat_bridge_state", map[string]any{
			"mode": "new_session",
		})
		return false, nil
	}
	if !isInternalCodexBridgeResponseID(previousResponseID) {
		baseState.PreviousResponseKind = PreviousKindExternal
		registry.Set(input.Req, &baseState)
		input.AuditCapture.AddGatewayMetadata("codex_responses_context_state", map[string]any{
			"mode":               "external_previous_response",
			"previousResponseId": previousResponseID,
		})
		return false, nil
	}

	now := s.clock.Now()
	readResult, err := ReadCodexContextResponseStateChain(ctx, s.store, ResponseChainReadInput{
		ResponseID:       previousResponseID,
		Boundary:         boundary,
		MaxDepth:         maxContextChainDepth,
		Now:              ISOFormat(now),
		RefreshExpiresAt: expiresAtFromISO(now),
	})
	if err != nil {
		return false, err
	}
	if readResult.Outcome != CodexContextOutcomeFound {
		input.AuditCapture.AddGatewayMetadata("codex_responses_chat_bridge_state", map[string]any{
			"mode":               "restore_failed",
			"previousResponseId": previousResponseID,
			"reason":             readResult.Outcome,
		})
		s.sendCodexBridgeStateFailure(input, stateRestoreFailure(readResult.Outcome))
		return true, nil
	}

	restoredInput, restoreErr := s.restoreFromReadResult(readResult, materializedCurrentInput)
	if restoreErr != nil {
		s.warn("codex_responses_chat_bridge_state_restore_failed", map[string]any{
			"previousResponseId": previousResponseID,
			"sessionId":          readResult.SessionID,
		}, restoreErr, "Codex Responses Chat bridge 状态恢复失败")
		input.AuditCapture.AddGatewayMetadata("codex_responses_chat_bridge_state", map[string]any{
			"mode":               "restore_failed",
			"previousResponseId": previousResponseID,
			"sessionId":          readResult.SessionID,
			"reason":             "payload_unavailable",
		})
		s.sendCodexBridgeStateFailure(input, gatewayFailure{
			statusCode: 404,
			_type:      "invalid_request_error",
			code:       "codex_bridge_previous_response_state_unavailable",
			message:    "previous_response_id 对应的服务端上下文文件不存在、已过期或校验失败",
		})
		return true, nil
	}
	startIndex := len(restoredInput) - len(responsesInputAsItems(materializedCurrentInput))
	if startIndex < 0 {
		startIndex = 0
	}
	baseState.MaterializedInput = restoredInput
	baseState.MaterializedCurrentInputStartIndex = &startIndex
	baseState.PreviousResponseKind = PreviousKindInternal
	baseState.SessionID = readResult.SessionID
	baseState.Restored = true
	registry.Set(input.Req, &baseState)
	input.AuditCapture.AddGatewayMetadata("codex_responses_chat_bridge_state", map[string]any{
		"mode":                  "restored",
		"previousResponseId":    previousResponseID,
		"sessionId":             readResult.SessionID,
		"restoredResponseCount": len(readResult.Responses),
	})
	return false, nil
}

// restoreFromReadResult mirrors readPayloadChain + restore + size assert.
func (s *ChatBridgeStateService) restoreFromReadResult(readResult CodexContextResponseChainReadResult, materializedCurrentInput any) ([]any, error) {
	payloads := make([]chatBridgeStatePayloadV2, 0, len(readResult.Responses))
	for _, row := range readResult.Responses {
		raw, err := s.segments.ReadSegmentPayload(row.CodexContextPayloadReference)
		if err != nil {
			return nil, err
		}
		payload, err := decodeStatePayload(raw)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	restoredInput := restoreResponsesInputFromPayloads(payloads, materializedCurrentInput)
	if err := assertRestoredInputSize(restoredInput); err != nil {
		return nil, err
	}
	return restoredInput, nil
}

// ---------------------------------------------------------------------------
// completion persistence
// ---------------------------------------------------------------------------

// CodexResponsesChatBridgeCompletionHandler mirrors
// CodexResponsesChatBridgeCompletionHandler.
type CodexResponsesChatBridgeCompletionHandler func(completion CodexResponsesChatBridgeCompletion)

// CompletionHandlerForRequest mirrors
// codexResponsesChatBridgeCompletionHandlerForRequest. nil mirrors the Node
// undefined result.
func (s *ChatBridgeStateService) CompletionHandlerForRequest(
	registry *ContextRequestStateRegistry,
	req *gatewaypreauth.GatewayRequest,
	account gatewayruntimecache.OpenAIAccountSecret,
	model string,
) CodexResponsesChatBridgeCompletionHandler {
	requestState, ok := registry.Get(req)
	if !ok || requestState.ActiveBridgeAccountID != account.ID {
		return nil
	}
	return func(completion CodexResponsesChatBridgeCompletion) {
		now := s.clock.Now()
		sessionID := requestState.SessionID
		if sessionID == "" {
			sessionID = completion.ResponseID
		}
		payload := chatBridgeStatePayloadV2{
			SchemaVersion:      2,
			ResponseID:         completion.ResponseID,
			SessionID:          sessionID,
			PreviousResponseID: optionalJSONString(requestState.PreviousResponseID),
			CreatedAt:          ISOFormat(now),
			Boundary:           requestState.Boundary,
			Request: chatBridgeStateRequestSection{
				Model:        optionalJSONString(normalizedOptionalText(requestState.CurrentBody["model"])),
				Instructions: requestState.CurrentBody["instructions"],
				Input:        requestState.CurrentInput,
			},
			OutputItems: completion.OutputItems,
		}
		stored, err := s.segments.WriteSegmentPayload(context.Background(), payload.SessionID, payload, now)
		if err == nil {
			err = SaveCodexContextResponseStateIndex(context.Background(), s.store, CodexContextResponseStateIndex{
				CodexContextStateBoundary:    requestState.Boundary,
				CodexContextPayloadReference: stored,
				ResponseID:                   completion.ResponseID,
				SessionID:                    sessionID,
				PreviousResponseID:           requestState.PreviousResponseID,
				UpstreamAccountID:            account.ID,
				Model:                        normalizedOptionalText(requestState.CurrentBody["model"]),
				UpstreamModel:                orElse(model, completion.Model),
				CreatedAt:                    ISOFormat(now),
				UpdatedAt:                    ISOFormat(now),
				LastUsedAt:                   ISOFormat(now),
				ExpiresAt:                    expiresAtFromISO(now),
			})
		}
		if err != nil {
			s.warn("codex_responses_chat_bridge_state_save_failed", map[string]any{
				"responseId":         completion.ResponseID,
				"previousResponseId": requestState.PreviousResponseID,
				"accountId":          account.ID,
			}, err, "Codex Responses Chat bridge 状态保存失败")
			return
		}
		s.info("codex_responses_chat_bridge_state_saved", map[string]any{
			"event":                     "codex_responses_chat_bridge_state_saved",
			"responseId":                completion.ResponseID,
			"previousResponseId":        requestState.PreviousResponseID,
			"sessionId":                 sessionID,
			"accountId":                 account.ID,
			"providerCode":              account.ProviderCode,
			"providerProtocolProfileId": account.ProviderProtocolProfileID,
		}, "Codex Responses Chat bridge 状态已保存")
	}
}

// ---------------------------------------------------------------------------
// compact snapshot + restore
// ---------------------------------------------------------------------------

// RestoreChatBridgeInputForCompact mirrors
// restoreCodexResponsesChatBridgeInputForCompact.
func (s *ChatBridgeStateService) RestoreChatBridgeInputForCompact(ctx context.Context, input struct {
	PreviousResponseID string
	Boundary           CodexContextStateBoundary
	CurrentInput       any
}) (CodexResponsesChatBridgeInputRestoreResult, error) {
	if input.PreviousResponseID == "" {
		return CodexResponsesChatBridgeInputRestoreResult{
			Outcome:       "no_previous",
			Input:         responsesInputAsItems(input.CurrentInput),
			ResponseCount: 0,
		}, nil
	}
	now := s.clock.Now()
	readResult, err := ReadCodexContextResponseStateChain(ctx, s.store, ResponseChainReadInput{
		ResponseID:       input.PreviousResponseID,
		Boundary:         input.Boundary,
		MaxDepth:         maxContextChainDepth,
		Now:              ISOFormat(now),
		RefreshExpiresAt: expiresAtFromISO(now),
	})
	if err != nil {
		return CodexResponsesChatBridgeInputRestoreResult{}, err
	}
	if readResult.Outcome != CodexContextOutcomeFound {
		return CodexResponsesChatBridgeInputRestoreResult{
			Outcome:    readResult.Outcome,
			ResponseID: readResult.ResponseID,
			SessionID:  readResult.SessionID,
		}, nil
	}
	restoredInput, restoreErr := s.restoreFromReadResult(readResult, input.CurrentInput)
	if restoreErr != nil {
		s.warn("codex_responses_chat_bridge_compact_restore_failed", map[string]any{
			"previousResponseId": input.PreviousResponseID,
			"sessionId":          readResult.SessionID,
		}, restoreErr, "Codex Responses Chat bridge compact 状态恢复失败")
		return CodexResponsesChatBridgeInputRestoreResult{
			Outcome:    RestoreOutcomePayloadUnavail,
			ResponseID: input.PreviousResponseID,
			SessionID:  readResult.SessionID,
		}, nil
	}
	return CodexResponsesChatBridgeInputRestoreResult{
		Outcome:       "found",
		Input:         restoredInput,
		SessionID:     readResult.SessionID,
		ResponseCount: len(readResult.Responses),
	}, nil
}

// CreateChatBridgeCompactSnapshotInput mirrors
// createCodexResponsesChatBridgeCompactSnapshot's input.
type CreateChatBridgeCompactSnapshotInput struct {
	SessionID         string
	SourceResponseID  string
	Boundary          CodexContextStateBoundary
	Summary           string
	UpstreamAccountID string
	Model             string
	UpstreamModel     string
	CreatedAt         *time.Time
}

// CreateChatBridgeCompactSnapshot mirrors
// createCodexResponsesChatBridgeCompactSnapshot.
func (s *ChatBridgeStateService) CreateChatBridgeCompactSnapshot(ctx context.Context, input CreateChatBridgeCompactSnapshotInput) (CodexResponsesChatBridgeCompactSnapshotResult, error) {
	now := s.clock.Now()
	if input.CreatedAt != nil {
		now = *input.CreatedAt
	}
	compactID := fmt.Sprintf("cmp_%s_%s", base36(now.UnixMilli()), randomHex8())
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = input.SourceResponseID
	}
	if sessionID == "" {
		sessionID = compactID
	}
	summaryDigest := digestText(input.Summary)
	payload := compactSnapshotPayloadV2{
		SchemaVersion:    2,
		CompactID:        compactID,
		SessionID:        sessionID,
		SourceResponseID: optionalJSONString(input.SourceResponseID),
		CreatedAt:        ISOFormat(now),
		Boundary:         input.Boundary,
		Summary:          input.Summary,
	}
	stored, err := s.segments.WriteSegmentPayload(ctx, payload.SessionID, payload, now)
	if err != nil {
		return CodexResponsesChatBridgeCompactSnapshotResult{}, err
	}
	if err := SaveCodexContextCompactStateIndex(ctx, s.store, CodexContextCompactStateIndex{
		CodexContextStateBoundary:    input.Boundary,
		CodexContextPayloadReference: stored,
		CompactID:                    compactID,
		SessionID:                    sessionID,
		SourceResponseID:             input.SourceResponseID,
		SummaryDigest:                summaryDigest,
		UpstreamAccountID:            input.UpstreamAccountID,
		Model:                        input.Model,
		UpstreamModel:                input.UpstreamModel,
		CreatedAt:                    ISOFormat(now),
		UpdatedAt:                    ISOFormat(now),
		LastUsedAt:                   ISOFormat(now),
		ExpiresAt:                    expiresAtFromISO(now),
	}); err != nil {
		return CodexResponsesChatBridgeCompactSnapshotResult{}, err
	}
	return CodexResponsesChatBridgeCompactSnapshotResult{
		CompactID:        compactID,
		EncryptedContent: codexCompactionReferencePrefix + compactID + "." + summaryDigest,
	}, nil
}

// resolveCodexCompactionReferencesInInput mirrors the same-named Node
// helper. A non-empty outcome marks the failure outcome.
func (s *ChatBridgeStateService) resolveCodexCompactionReferencesInInput(ctx context.Context, input any, boundary CodexContextStateBoundary) (struct {
	outcome string
	input   any
	changed bool
}, error) {
	result := struct {
		outcome string
		input   any
		changed bool
	}{}
	items, isArray := input.([]any)
	if !isArray {
		result.input = input
		return result, nil
	}
	resolved := make([]any, 0, len(items))
	changed := false
	for _, item := range items {
		record, isObject := item.(jsonRecord)
		if !isObject || !isCodexCompactionInputItem(record) {
			resolved = append(resolved, item)
			continue
		}
		encryptedContent := normalizedOptionalText(record["encrypted_content"])
		if !strings.HasPrefix(encryptedContent, codexCompactionReferencePrefix) {
			resolved = append(resolved, item)
			continue
		}
		reference, ok := parseCodexCompactionReference(encryptedContent)
		if !ok {
			result.outcome = RestoreOutcomePayloadUnavail
			return result, nil
		}
		summaryResult, err := s.readCodexCompactionSnapshotSummary(ctx, reference.compactID, reference.digest, boundary)
		if err != nil {
			return result, err
		}
		if summaryResult.outcome != CodexContextOutcomeFound {
			result.outcome = summaryResult.outcome
			return result, nil
		}
		next := cloneJSONMap(record)
		next["type"] = "compaction_summary"
		next["encrypted_content"] = encodeInlineCodexCompactionSummary(summaryResult.summary)
		resolved = append(resolved, next)
		changed = true
	}
	if changed {
		result.input = resolved
	} else {
		result.input = input
	}
	result.changed = changed
	return result, nil
}

func (s *ChatBridgeStateService) readCodexCompactionSnapshotSummary(ctx context.Context, compactID, digest string, boundary CodexContextStateBoundary) (struct {
	outcome string
	summary string
}, error) {
	result := struct {
		outcome string
		summary string
	}{}
	now := s.clock.Now()
	readResult, err := ReadCodexContextCompactState(ctx, s.store, CompactStateReadInput{
		CompactID:        compactID,
		Boundary:         boundary,
		Now:              ISOFormat(now),
		RefreshExpiresAt: expiresAtFromISO(now),
	})
	if err != nil {
		return result, err
	}
	if readResult.Outcome != CodexContextOutcomeFound {
		result.outcome = readResult.Outcome
		return result, nil
	}
	if readResult.Compact.SummaryDigest != digest {
		result.outcome = RestoreOutcomePayloadUnavail
		return result, nil
	}
	raw, err := s.segments.ReadSegmentPayload(readResult.Compact.CodexContextPayloadReference)
	if err != nil {
		s.warn("codex_responses_chat_bridge_compact_snapshot_read_failed", map[string]any{
			"compactId": compactID,
		}, err, "Codex Responses Chat bridge compact snapshot 读取失败")
		result.outcome = RestoreOutcomePayloadUnavail
		return result, nil
	}
	payload, err := decodeCompactSnapshotPayload(raw)
	if err != nil {
		s.warn("codex_responses_chat_bridge_compact_snapshot_read_failed", map[string]any{
			"compactId": compactID,
		}, err, "Codex Responses Chat bridge compact snapshot 读取失败")
		result.outcome = RestoreOutcomePayloadUnavail
		return result, nil
	}
	if payload.CompactID != compactID || digestText(payload.Summary) != digest {
		result.outcome = RestoreOutcomePayloadUnavail
		return result, nil
	}
	result.outcome = CodexContextOutcomeFound
	result.summary = payload.Summary
	return result, nil
}

// ---------------------------------------------------------------------------
// account gating + per-account preparation
// ---------------------------------------------------------------------------

// CodexResponsesContextAllowsAccount mirrors codexResponsesContextAllowsAccount.
func (s *ChatBridgeStateService) CodexResponsesContextAllowsAccount(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, account gatewayruntimecache.OpenAIAccountSecret) bool {
	state, ok := registry.Get(req)
	if !ok {
		return true
	}
	compactAccountKind := s.codexResponsesCompactAccountKind(registry, req, account)
	explicitBridge := compactAccountKind == CompactAccountKindBridge
	if state.RequestKind != RequestKindCompact && CodexCompactionExpectedForRequest(req) {
		return compactAccountKind == CompactAccountKindNative
	}
	if state.RequestKind == RequestKindCompact {
		if state.CompactDispatchMode == CompactDispatchNative {
			return compactAccountKind == CompactAccountKindNative
		}
		if state.PreviousResponseKind == PreviousKindInternal {
			return explicitBridge
		}
		if state.PreviousResponseKind == PreviousKindExternal {
			return compactAccountKind == CompactAccountKindNative
		}
		return compactAccountKind != CompactAccountKindUnsupported
	}
	return !(state.PreviousResponseKind == PreviousKindExternal && explicitBridge)
}

// HasExplicitCodexResponsesChatBridgeRuntimeAccount mirrors
// hasExplicitCodexResponsesChatBridgeRuntimeAccount.
func (s *ChatBridgeStateService) HasExplicitCodexResponsesChatBridgeRuntimeAccount(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, accounts []gatewayruntimecache.OpenAIAccountSecret) bool {
	for _, account := range accounts {
		if s.codexResponsesCompactAccountKind(registry, req, account) == CompactAccountKindBridge {
			return true
		}
	}
	return false
}

// Compact account kinds.
const (
	CompactAccountKindNative      = "native"
	CompactAccountKindBridge      = "bridge"
	CompactAccountKindUnsupported = "unsupported"
)

func (s *ChatBridgeStateService) codexResponsesCompactAccountKind(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, account gatewayruntimecache.OpenAIAccountSecret) string {
	state, _ := registry.Get(req)
	model := ""
	if state != nil {
		model = normalizedOptionalText(state.CanonicalBody["model"])
	}
	if model == "" {
		resolvedModel, _ := gatewaypreauth.RequestModel(req)
		model = resolvedModel
	}
	mapping := resolveResponsesModelMapping(account, model)
	if isOpenAIResponsesToChatCompletionsModelMapping(mapping) {
		return CompactAccountKindBridge
	}
	if !isOpenAIProtocolProfile(account) {
		return CompactAccountKindUnsupported
	}
	if mapping != nil && mapping.UpstreamEndpointFamily != gatewayopenai.FamilyResponses {
		return CompactAccountKindUnsupported
	}
	return CompactAccountKindNative
}

// PrepareCodexResponsesContextForAccount mirrors
// prepareCodexResponsesContextForAccount.
func (s *ChatBridgeStateService) PrepareCodexResponsesContextForAccount(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, account gatewayruntimecache.OpenAIAccountSecret) (bool, error) {
	state, ok := registry.Get(req)
	if !ok {
		return false, nil
	}
	explicitBridge := s.codexResponsesCompactAccountKind(registry, req, account) == CompactAccountKindBridge
	if state.RequestKind == RequestKindCompact {
		if state.CompactDispatchMode == CompactDispatchNative && explicitBridge {
			return false, gatewaypreauth.NewGatewayRequestValidationError(
				"原生 Responses compact 请求不能发送给 Chat bridge 账号",
				gatewaypreauth.WithValidationErrorCode("native_responses_compact_requires_native_account"),
				gatewaypreauth.WithValidationErrorAccountScoped(),
			)
		}
		return false, nil
	}
	s.synchronizeCodexResponsesDispatchBaseline(registry, req, state)
	if explicitBridge && state.PreviousResponseKind == PreviousKindExternal {
		return false, fmt.Errorf("外部 previous_response_id 只能发送给原生 Responses 账号")
	}
	input := state.MaterializedInput
	if !explicitBridge {
		transformed, err := nativeResponsesInputFromMaterialized(state.MaterializedInput)
		if err != nil {
			return false, err
		}
		input = transformed
	}
	body := cloneJSONMapShallow(state.CanonicalBody)
	body["input"] = input
	if state.PreviousResponseKind == PreviousKindInternal {
		delete(body, "previous_response_id")
	}
	if explicitBridge {
		state.ActiveBridgeAccountID = account.ID
	} else {
		state.ActiveBridgeAccountID = ""
	}
	currentBody := currentGatewayJSONBody(req)
	bodyChanged := explicitBridge || jsonRefNotEqual(body["input"], inputOf(currentBody)) ||
		(state.PreviousResponseKind == PreviousKindInternal && mapHasKey(currentBody, "previous_response_id"))
	if bodyChanged {
		state.LastRenderedBody = body
		if req.Body != nil {
			gatewaybody.ReplaceGatewayJSONBody(req.Body, body)
		}
	} else if currentBody != nil {
		state.LastRenderedBody = currentBody
	} else {
		state.LastRenderedBody = state.CanonicalBody
	}
	return explicitBridge, nil
}

// PrepareCodexResponsesCompactDispatchForAccounts mirrors
// prepareCodexResponsesCompactDispatchForAccounts.
func (s *ChatBridgeStateService) PrepareCodexResponsesCompactDispatchForAccounts(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, accounts []gatewayruntimecache.OpenAIAccountSecret) bool {
	state, ok := registry.Get(req)
	if !ok || state.RequestKind != RequestKindCompact {
		return false
	}
	bridgeAccountCount := 0
	nativeAccountCount := 0
	for _, account := range accounts {
		switch s.codexResponsesCompactAccountKind(registry, req, account) {
		case CompactAccountKindBridge:
			bridgeAccountCount++
		case CompactAccountKindNative:
			nativeAccountCount++
		}
	}
	if state.PreviousResponseKind == PreviousKindExternal {
		state.CompactDispatchMode = CompactDispatchNative
		return false
	}
	if state.PreviousResponseKind == PreviousKindInternal {
		state.CompactDispatchMode = CompactDispatchBridge
		return bridgeAccountCount > 0
	}
	if nativeAccountCount > 0 {
		state.CompactDispatchMode = CompactDispatchNative
		return false
	}
	state.CompactDispatchMode = CompactDispatchBridge
	return bridgeAccountCount > 0
}

func (s *ChatBridgeStateService) synchronizeCodexResponsesDispatchBaseline(registry *ContextRequestStateRegistry, req *gatewaypreauth.GatewayRequest, state *CodexResponsesContextRequestState) {
	currentBody := currentGatewayJSONBody(req)
	if currentBody == nil || jsonRefEqual(currentBody, any(state.LastRenderedBody)) {
		return
	}
	if state.LastRenderedBody != nil {
		state.MaterializedInput = currentBody["input"]
		state.CurrentInput = currentInputFromMaterializedMutation(state, currentBody["input"])
	}
	state.CanonicalBody = cloneJSONMapShallow(currentBody)
	state.CurrentBody = cloneJSONMapShallow(currentBody)
}

func currentInputFromMaterializedMutation(state *CodexResponsesContextRequestState, materializedInput any) any {
	startIndex := state.MaterializedCurrentInputStartIndex
	if state.PreviousResponseKind != PreviousKindInternal || startIndex == nil {
		return materializedInput
	}
	items, isArray := materializedInput.([]any)
	if !isArray {
		return materializedInput
	}
	start := *startIndex
	if start > len(items) {
		start = len(items)
	}
	if start < 0 {
		start = 0
	}
	return cloneJSONSlice(items[start:])
}

func currentGatewayJSONBody(req *gatewaypreauth.GatewayRequest) jsonRecord {
	if req == nil || req.Body == nil {
		return nil
	}
	return gatewaybody.GatewayJSONObjectBody(req.Body)
}

// nativeResponsesInputFromMaterialized mirrors nativeResponsesInputFromMaterialized.
func nativeResponsesInputFromMaterialized(value any) (any, error) {
	items, isArray := value.([]any)
	if !isArray {
		return value, nil
	}
	changed := false
	transformed := make([]any, 0, len(items))
	for _, item := range items {
		next, err := nativeResponsesItemFromMaterialized(item)
		if err != nil {
			return nil, err
		}
		if jsonRefNotEqual(next, item) {
			changed = true
		}
		transformed = append(transformed, next)
	}
	if changed {
		return transformed, nil
	}
	return value, nil
}

func nativeResponsesItemFromMaterialized(item any) (any, error) {
	record, isObject := item.(jsonRecord)
	if !isObject {
		return item, nil
	}
	itemType, _ := record["type"].(string)
	if itemType != "compaction" && itemType != "compaction_summary" {
		return item, nil
	}
	encryptedContent := normalizedOptionalText(record["encrypted_content"])
	if !strings.HasPrefix(encryptedContent, codexInlineCompactionSummaryPrefix) {
		return item, nil
	}
	summary := decodeInlineCodexCompactionSummary(encryptedContent)
	if summary == "" {
		return nil, fmt.Errorf("内部压缩摘要无法解析，禁止发送到原生 Responses 上游")
	}
	return jsonRecord{
		"type": "message",
		"role": "developer",
		"content": []any{
			jsonRecord{"type": "input_text", "text": summary},
		},
	}, nil
}

func decodeInlineCodexCompactionSummary(value string) string {
	encoded := strings.TrimPrefix(value, codexInlineCompactionSummaryPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		return ""
	}
	record, isObject := parsed.(jsonRecord)
	if !isObject {
		return ""
	}
	return normalizedOptionalText(record["summary"])
}

func encodeInlineCodexCompactionSummary(summary string) string {
	encoded, err := json.Marshal(struct {
		Summary string `json:"summary"`
	}{Summary: summary})
	if err != nil {
		return codexInlineCompactionSummaryPrefix
	}
	return codexInlineCompactionSummaryPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func isInternalCodexBridgeResponseID(value string) bool {
	return internalBridgeResponseIDPattern.MatchString(value)
}

func codexContextBoundary(systemAccountID, apiKeyID, groupID string, groupAccess gatewayruntimecache.GroupUsageAccessMetadata) CodexContextStateBoundary {
	return CodexContextStateBoundary{
		SystemAccountID: systemAccountID,
		APIKeyID:        apiKeyID,
		GroupID:         groupID,
		ProviderCode:    groupAccess.ProviderCode,
	}
}

// ParseGatewayJSONObjectPublic exposes parseGatewayJsonObject for the
// compact preflight in the same package family.
func (s *ChatBridgeStateService) ParseGatewayJSONObjectPublic(req *gatewaypreauth.GatewayRequest) (jsonRecord, error) {
	return s.parseGatewayJSONObject(req)
}

// parseGatewayJSONObject mirrors parseGatewayJsonObject: parsed bodies win,
// then the raw body is decoded; invalid json and empty bodies read as {}.
func (s *ChatBridgeStateService) parseGatewayJSONObject(req *gatewaypreauth.GatewayRequest) (jsonRecord, error) {
	if parsed := req.ParsedJSONObjectBody(); parsed != nil {
		return cloneJSONMapShallow(parsed), nil
	}
	var rawBody []byte
	if req.Body != nil {
		rawBody = req.Body.RawBody
	}
	if len(rawBody) == 0 {
		return jsonRecord{}, nil
	}
	bodyState := req.BodyState()
	if bodyState != nil && bodyState.JSONParseStatus == gatewaybody.JSONParseStatusInvalidJSON {
		return jsonRecord{}, nil
	}
	var parsed any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return jsonRecord{}, nil
	}
	if record, isObject := parsed.(jsonRecord); isObject {
		return cloneJSONMapShallow(record), nil
	}
	return jsonRecord{}, nil
}

// ---------------------------------------------------------------------------
// payload codecs + restore helpers
// ---------------------------------------------------------------------------

type chatBridgeStateRequestSection struct {
	Model        *string `json:"model,omitempty"`
	Instructions any     `json:"instructions"`
	Input        any     `json:"input"`
}

type chatBridgeStatePayloadV2 struct {
	SchemaVersion      int64                         `json:"schemaVersion"`
	ResponseID         string                        `json:"responseId"`
	SessionID          string                        `json:"sessionId"`
	PreviousResponseID *string                       `json:"previousResponseId,omitempty"`
	CreatedAt          string                        `json:"createdAt"`
	Boundary           CodexContextStateBoundary     `json:"boundary"`
	Request            chatBridgeStateRequestSection `json:"request"`
	OutputItems        []any                         `json:"outputItems"`
}

type compactSnapshotPayloadV2 struct {
	SchemaVersion    int64                     `json:"schemaVersion"`
	CompactID        string                    `json:"compactId"`
	SessionID        string                    `json:"sessionId"`
	SourceResponseID *string                   `json:"sourceResponseId,omitempty"`
	CreatedAt        string                    `json:"createdAt"`
	Boundary         CodexContextStateBoundary `json:"boundary"`
	Summary          string                    `json:"summary"`
}

func decodeStatePayload(raw json.RawMessage) (chatBridgeStatePayloadV2, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return chatBridgeStatePayloadV2{}, err
	}
	record, isObject := value.(jsonRecord)
	if !isObject {
		return chatBridgeStatePayloadV2{}, fmt.Errorf("Codex Responses Chat bridge 状态文件结构无效")
	}
	if numberAsInt64(record["schemaVersion"]) != 2 {
		return chatBridgeStatePayloadV2{}, fmt.Errorf("Codex Responses Chat bridge 状态文件结构无效")
	}
	_, responseIDIsString := record["responseId"].(string)
	_, sessionIDIsString := record["sessionId"].(string)
	_, boundaryIsObject := record["boundary"].(jsonRecord)
	_, requestIsObject := record["request"].(jsonRecord)
	_, outputItemsIsArray := record["outputItems"].([]any)
	if !responseIDIsString || !sessionIDIsString || !boundaryIsObject || !requestIsObject || !outputItemsIsArray {
		return chatBridgeStatePayloadV2{}, fmt.Errorf("Codex Responses Chat bridge 状态文件结构无效")
	}
	var payload chatBridgeStatePayloadV2
	if err := json.Unmarshal(raw, &payload); err != nil {
		return chatBridgeStatePayloadV2{}, err
	}
	return payload, nil
}

func decodeCompactSnapshotPayload(raw json.RawMessage) (compactSnapshotPayloadV2, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return compactSnapshotPayloadV2{}, err
	}
	record, isObject := value.(jsonRecord)
	if !isObject {
		return compactSnapshotPayloadV2{}, fmt.Errorf("Codex Responses Chat bridge compact snapshot 文件结构无效")
	}
	if numberAsInt64(record["schemaVersion"]) != 2 {
		return compactSnapshotPayloadV2{}, fmt.Errorf("Codex Responses Chat bridge compact snapshot 文件结构无效")
	}
	_, compactIDIsString := record["compactId"].(string)
	_, sessionIDIsString := record["sessionId"].(string)
	_, boundaryIsObject := record["boundary"].(jsonRecord)
	_, summaryIsString := record["summary"].(string)
	if !compactIDIsString || !sessionIDIsString || !boundaryIsObject || !summaryIsString {
		return compactSnapshotPayloadV2{}, fmt.Errorf("Codex Responses Chat bridge compact snapshot 文件结构无效")
	}
	var payload compactSnapshotPayloadV2
	if err := json.Unmarshal(raw, &payload); err != nil {
		return compactSnapshotPayloadV2{}, err
	}
	return payload, nil
}

func restoreResponsesInputFromPayloads(payloads []chatBridgeStatePayloadV2, currentInput any) []any {
	restored := make([]any, 0, 16)
	for _, payload := range payloads {
		appendInstructionAsMessage(&restored, payload.Request.Instructions)
		restored = append(restored, responsesInputAsItems(payload.Request.Input)...)
		restored = append(restored, cloneJSONSlice(payload.OutputItems)...)
	}
	restored = append(restored, responsesInputAsItems(currentInput)...)
	return restored
}

func appendInstructionAsMessage(output *[]any, instructions any) {
	text := normalizedOptionalText(instructions)
	if text == "" {
		return
	}
	*output = append(*output, jsonRecord{
		"type": "message",
		"role": "system",
		"content": []any{
			jsonRecord{"type": "input_text", "text": text},
		},
	})
}

func responsesInputAsItems(input any) []any {
	switch typed := input.(type) {
	case string:
		return []any{
			jsonRecord{
				"type": "message",
				"role": "user",
				"content": []any{
					jsonRecord{"type": "input_text", "text": typed},
				},
			},
		}
	case []any:
		return cloneJSONSlice(typed)
	case jsonRecord:
		return []any{cloneJSONMap(typed)}
	default:
		return []any{}
	}
}

func assertRestoredInputSize(input any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(encoded) > maxRestoredInputBytes {
		return fmt.Errorf("Codex Responses Chat bridge 恢复后的上下文超过 %d 字节上限", maxRestoredInputBytes)
	}
	return nil
}

func isCodexCompactionInputItem(item jsonRecord) bool {
	itemType, _ := item["type"].(string)
	return itemType == "compaction" || itemType == "compaction_summary"
}

type compactionReference struct {
	compactID string
	digest    string
}

func parseCodexCompactionReference(value string) (compactionReference, bool) {
	rest := strings.TrimPrefix(value, codexCompactionReferencePrefix)
	separatorIndex := strings.IndexByte(rest, '.')
	if separatorIndex <= 0 {
		return compactionReference{}, false
	}
	compactID := strings.TrimSpace(rest[:separatorIndex])
	digest := strings.TrimSpace(rest[separatorIndex+1:])
	if compactID == "" || !isSHA256Hex(digest) {
		return compactionReference{}, false
	}
	return compactionReference{compactID: compactID, digest: strings.ToLower(digest)}, true
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// failure copy + plumbing
// ---------------------------------------------------------------------------

type gatewayFailure struct {
	statusCode int
	_type      string
	code       string
	message    string
}

func stateRestoreFailure(outcome string) gatewayFailure {
	if outcome == RestoreOutcomeBoundaryMismatch {
		return gatewayFailure{
			statusCode: 403,
			_type:      "invalid_request_error",
			code:       "codex_bridge_previous_response_boundary_mismatch",
			message:    "previous_response_id 不属于当前 API Key、分组或供应商边界",
		}
	}
	if outcome == RestoreOutcomeChainTooDeep {
		return gatewayFailure{
			statusCode: 413,
			_type:      "invalid_request_error",
			code:       "codex_bridge_previous_response_chain_too_deep",
			message:    "previous_response_id 上下文链过长，请先压缩上下文后继续",
		}
	}
	if outcome == RestoreOutcomeChainBroken {
		return gatewayFailure{
			statusCode: 404,
			_type:      "invalid_request_error",
			code:       "codex_bridge_previous_response_chain_broken",
			message:    "previous_response_id 上下文链不完整或已被清理",
		}
	}
	return gatewayFailure{
		statusCode: 404,
		_type:      "invalid_request_error",
		code:       "codex_bridge_previous_response_not_found",
		message:    "previous_response_id 对应的服务端上下文不存在或已过期",
	}
}

func compactReferenceFailure(outcome string) gatewayFailure {
	if outcome == RestoreOutcomeBoundaryMismatch {
		return gatewayFailure{
			statusCode: 403,
			_type:      "invalid_request_error",
			code:       "codex_bridge_compact_boundary_mismatch",
			message:    "compact snapshot 不属于当前 API Key、分组或供应商边界",
		}
	}
	return gatewayFailure{
		statusCode: 404,
		_type:      "invalid_request_error",
		code:       "codex_bridge_compact_snapshot_not_found",
		message:    "compact snapshot 不存在、已过期或校验失败",
	}
}

func (s *ChatBridgeStateService) sendCodexBridgeStateFailure(input ContextStatePreflightInput, failure gatewayFailure) {
	if s.Sink == nil {
		return
	}
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf(failure.message, failure._type, failure.code)
	s.Sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             input.Req,
		Res:             input.Res,
		AuditCapture:    input.AuditCapture,
		UsageContext:    input.UsageContext,
		StartedAt:       input.StartedAt,
		StatusCode:      failure.statusCode,
		ResponsePayload: responsePayload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "request_validation",
			ErrorCode:    failure.code,
			ErrorMessage: failure.message,
		},
	})
}

func (s *ChatBridgeStateService) warn(event string, fields map[string]any, err error, message string) {
	if s.Logger == nil {
		return
	}
	merged := map[string]any{"event": event}
	for key, value := range fields {
		merged[key] = value
	}
	if err != nil {
		merged["err"] = err.Error()
	}
	s.Logger.Warn(event, merged, message)
}

func (s *ChatBridgeStateService) info(event string, fields map[string]any, message string) {
	// The Node info log has no failure semantics; the sink seam keeps it
	// observable through the same Logger when one is wired.
	if s.Logger == nil {
		return
	}
	s.Logger.Warn(event, fields, message)
}

// ---------------------------------------------------------------------------
// small json helpers
// ---------------------------------------------------------------------------

func normalizedOptionalText(value any) string {
	text, isString := value.(string)
	if !isString {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) == 0 {
		return ""
	}
	return trimmed
}

func optionalJSONString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func orElse(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func numberAsInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return 0
}

func cloneJSONMapShallow(input jsonRecord) jsonRecord {
	output := make(jsonRecord, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneJSONMapShallowWithInput(input jsonRecord, materializedInput any) jsonRecord {
	output := cloneJSONMapShallow(input)
	output["input"] = materializedInput
	return output
}

func cloneJSONSlice(input []any) []any {
	output := make([]any, len(input))
	for index, item := range input {
		output[index] = cloneJSONValue(item)
	}
	return output
}

func mapHasKey(record jsonRecord, key string) bool {
	if record == nil {
		return false
	}
	_, ok := record[key]
	return ok
}

func inputOf(record jsonRecord) any {
	if record == nil {
		return nil
	}
	return record["input"]
}

// jsonRefEqual / jsonRefNotEqual mirror the JS reference equality (===/!==)
// for body/input values: scalars compare by value, maps and slices by
// identity.
func jsonRefEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	switch left.(type) {
	case jsonRecord, []any:
		return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
	}
	switch right.(type) {
	case jsonRecord, []any:
		return false
	}
	return left == right
}

func jsonRefNotEqual(left, right any) bool {
	return !jsonRefEqual(left, right)
}

// resolveResponsesModelMapping mirrors resolveOpenAIAccountModelMapping on
// the OPENAI_RESPONSES_FAMILY source family.
func resolveResponsesModelMapping(account gatewayruntimecache.OpenAIAccountSecret, requestedModel string) *gatewayprotoResolvedMapping {
	runtime := projectRuntimeAccount(account)
	resolved := gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, gatewayopenai.FamilyResponses)
	if resolved == nil {
		return nil
	}
	return &gatewayprotoResolvedMapping{
		SourceModel:            resolved.SourceModel,
		SourceEndpointFamily:   resolved.SourceEndpointFamily,
		UpstreamModel:          resolved.UpstreamModel,
		UpstreamEndpointFamily: resolved.UpstreamEndpointFamily,
	}
}

// gatewayprotoResolvedMapping mirrors the resolved mapping projection.
type gatewayprotoResolvedMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
}

func isOpenAIResponsesToChatCompletionsModelMapping(mapping *gatewayprotoResolvedMapping) bool {
	return mapping != nil &&
		mapping.SourceEndpointFamily == gatewayopenai.FamilyResponses &&
		mapping.UpstreamEndpointFamily == gatewayopenai.FamilyChatCompletions
}

// projectRuntimeAccount mirrors the runtime-cache secret onto the
// gatewayopenai mapping account (same projection as
// gatewaypreauth.projectRuntimeAccount, which is unexported there).
func projectRuntimeAccount(account gatewayruntimecache.OpenAIAccountSecret) *gatewayopenai.RuntimeAccount {
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(account.ModelMappings))
	for _, mapping := range account.ModelMappings {
		enabled := mapping.Enabled
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          derefString(mapping.RuntimeSource),
			RuntimeRouteRuleID:     derefString(mapping.RuntimeRouteRuleID),
		})
	}
	return &gatewayopenai.RuntimeAccount{
		ModelMappings:             mappings,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
