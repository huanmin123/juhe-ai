package chat

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Route layer for the my-chat module (Node chat.routes.ts mounted at
// ${systemApiPrefix}/my-chat). Envelopes, validation order, status codes,
// revision semantics and Chinese error strings mirror Node byte for byte.
//
// The composition root wires Deps.RequireSession (Node requireAuth +
// forceSelfAccessScope run at the mount). The generation-backed routes
// (POST .../stream, POST .../context/compactions, models listing, asset
// upload/object serving) stay Node-owned until the gateway runtime wave
// provides their ports; every route expressible with the chat store alone is
// live here.

// chatImageInputPolicy mirrors chatImageInputPolicy.
type chatImageInputPolicy struct {
	MimeType string `json:"mimeType"`
	MaxEdge  int    `json:"maxEdge"`
	Quality  int    `json:"quality"`
	MaxBytes int64  `json:"maxBytes"`
}

var defaultChatImageInputPolicy = chatImageInputPolicy{
	MimeType: "image/webp",
	MaxEdge:  1024,
	Quality:  82,
	MaxBytes: 3 * 1024 * 1024,
}

// GenerationSnapshot mirrors the registry.snapshot shape used by the
// submissions route.
type GenerationSnapshot struct {
	State                  string // e.g. "running" | "missing" | ...
	AssistantMessageID     string
	EventVersion           *int64
	LastSemanticActivityAt *string
}

// GenerationRunner is the abort handle exposed by an active generation.
type GenerationRunner interface {
	Abort() bool
}

// GenerationIdentity mirrors the registry identity triple.
type GenerationIdentity struct {
	OwnerID        string
	ConversationID string
	TurnID         string
}

// GenerationRegistry is the in-process generation registry port (Node
// chatGenerationRegistry). Optional until the generation wave lands.
type GenerationRegistry interface {
	Snapshot(ownerID, conversationID, turnID string) GenerationSnapshot
	Get(ownerID, conversationID, turnID string) (GenerationRunner, bool)
}

// AttachStreamHandler streams subscribed runner events to the response (Node
// responseSubscriber + heartbeat). Provided by the generation wave together
// with GenerationRegistry; when either is nil the streams route resolves
// through the store-only interrupted-turn paths.
type AttachStreamHandler func(w http.ResponseWriter, r *http.Request, identity GenerationIdentity) bool

// ToolCapabilitiesResolver produces GET /conversations/{id} toolCapabilities
// (Node loadChatConversationToolCapabilities). Provided by the model-catalog
// wave; when nil the route renders the Node catch-branch fallback shape.
type ToolCapabilitiesResolver func(conversation *Conversation, ownerID string) any

// Deps carries the route collaborators.
type Deps struct {
	Store *Store
	// RequireSession mirrors the my-chat mount middleware (requireAuth +
	// forceSelfAccessScope). Optional in tests that install the context
	// directly.
	RequireSession func(next http.Handler) http.Handler
	// MaxTurnsPerConversation mirrors runtimeConfig.chat.maxTurnsPerConversation.
	MaxTurnsPerConversation int64
	// Now overrides the wall clock (tests); time.Now by default.
	Now func() time.Time
	// Generations and AttachStream are the generation-wave ports.
	Generations   GenerationRegistry
	AttachStream  AttachStreamHandler
	ToolCapabilit ToolCapabilitiesResolver

	// --- generation-wave ports (frozen for the G20 composition root) ---
	// Hub is the in-process generation registry (NewGenerationHub).
	Hub *GenerationHub
	// Executor dispatches model/image/compaction requests to the internal
	// gateway (Node dispatchChatGatewayRequest).
	Executor GenerationExecutor
	// ModelCatalog lists group accounts and provider model catalogs.
	ModelCatalog ModelCatalog
	// ChatKeys provisions and resolves the chat-scoped API key.
	ChatKeys ChatAPIKeyProvider
	// GatewayKeys validates gateway API keys (group bindings + image flag).
	GatewayKeys GatewayKeyValidator
	// ObjectStore persists chat asset objects (local chat assets root).
	ObjectStore ObjectStore
	// ImageProcessor decodes/encodes uploads and previews (sharp port).
	ImageProcessor ImageProcessor
	// ImageObservation schedules and awaits image semantic observations.
	ImageObservation ImageObservations
	// Compactions runs the context compaction service loop.
	Compactions *CompactionService
	// TokenCount counts text tokens (gpt-tokenizer port).
	TokenCount TokenCountFunc
	// TraceID extracts the request trace id (Node getTraceId()).
	TraceID func(r *http.Request) string
	// RetentionDays mirrors runtimeConfig.chat.retentionDays.
	RetentionDays int
	// MaxConversationsPerUserInt mirrors runtimeConfig.chat.maxConversationsPerUser.
	MaxConversationsPerUserInt func() int
	// DiagnosticToolEnabled mirrors runtimeConfig.chat.diagnosticToolEnabled.
	DiagnosticToolEnabled bool
	// ToolEnvironment mirrors chatToolRuntimeEnvironment().
	ToolEnvironment string
}

// chatSystemAPIJSONBodyLimit mirrors chatSystemApiJsonBodyLimit ('24mb').
const chatSystemAPIJSONBodyLimit = 24 * 1024 * 1024

// activeConversationAction mirrors ActiveChatConversationAction.
type activeConversationAction struct {
	token   int64
	ownerID string
	kind    string // "compacting" | "clearing"
}

// activePreparation mirrors ActiveChatPreparation (the controller collapses
// to a canceled flag).
type activePreparation struct {
	token           int64
	ownerID         string
	clientMessageID string
	phase           string // "preparing" | "accepting"
	mu              sync.Mutex
	canceled        bool
}

func (p *activePreparation) abort() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.canceled {
		return false
	}
	p.canceled = true
	return true
}

func (p *activePreparation) isCanceled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canceled
}

type chatRoutes struct {
	deps    *Deps
	mu      sync.Mutex
	token   int64
	preps   map[string]*activePreparation
	actions map[string]*activeConversationAction
}

func (rt *chatRoutes) now() string {
	if rt.deps.Now != nil {
		return isoMillis(rt.deps.Now())
	}
	return isoMillis(time.Now())
}

func (rt *chatRoutes) nextTokenLocked() int64 {
	rt.token++
	return rt.token
}

func (rt *chatRoutes) getAction(conversationID, ownerID string) *activeConversationAction {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	action := rt.actions[conversationID]
	if action == nil || action.ownerID != ownerID {
		return nil
	}
	return action
}

// claimAction mirrors claimActiveChatConversationAction.
func (rt *chatRoutes) claimAction(conversationID, ownerID, kind string) *activeConversationAction {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, taken := rt.actions[conversationID]; taken {
		return nil
	}
	if _, taken := rt.preps[conversationID]; taken {
		return nil
	}
	action := &activeConversationAction{token: rt.nextTokenLocked(), ownerID: ownerID, kind: kind}
	rt.actions[conversationID] = action
	return action
}

// deleteActionIfMatches mirrors deleteActiveChatConversationActionIfMatches.
func (rt *chatRoutes) deleteActionIfMatches(conversationID string, token int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if action := rt.actions[conversationID]; action != nil && action.token == token {
		delete(rt.actions, conversationID)
	}
}

// claimPreparation mirrors claimActiveChatPreparation.
func (rt *chatRoutes) claimPreparation(conversationID, ownerID, clientMessageID string) *activePreparation {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, taken := rt.preps[conversationID]; taken {
		return nil
	}
	if _, taken := rt.actions[conversationID]; taken {
		return nil
	}
	prep := &activePreparation{token: rt.nextTokenLocked(), ownerID: ownerID, clientMessageID: clientMessageID, phase: "preparing"}
	rt.preps[conversationID] = prep
	return prep
}

// getPreparationForConversation mirrors getActiveChatPreparationForConversation.
func (rt *chatRoutes) getPreparationForConversation(conversationID, ownerID string) *activePreparation {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	prep := rt.preps[conversationID]
	if prep == nil || prep.ownerID != ownerID {
		return nil
	}
	return prep
}

// getPreparation mirrors getActiveChatPreparation.
func (rt *chatRoutes) getPreparation(conversationID, ownerID, clientMessageID string) *activePreparation {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	prep := rt.preps[conversationID]
	if prep == nil || prep.ownerID != ownerID || prep.clientMessageID != clientMessageID {
		return nil
	}
	return prep
}

// cancelPreparation mirrors cancelActiveChatPreparation.
func (rt *chatRoutes) cancelPreparation(conversationID, ownerID, clientMessageID string) (string, bool) {
	rt.mu.Lock()
	prep := rt.preps[conversationID]
	if prep == nil || prep.ownerID != ownerID || prep.clientMessageID != clientMessageID {
		rt.mu.Unlock()
		return "", false
	}
	phase := prep.phase
	rt.mu.Unlock()
	if !prep.abort() {
		return "", false
	}
	return phase, true
}

// deletePreparationIfMatches mirrors deleteActiveChatPreparationIfMatches.
func (rt *chatRoutes) deletePreparationIfMatches(conversationID string, token int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if prep := rt.preps[conversationID]; prep != nil && prep.token == token {
		delete(rt.preps, conversationID)
	}
}

// invalidRequestError maps to Node ZodError → 400 chat_invalid_request.
type invalidRequestError struct{ Message string }

func (e *invalidRequestError) Error() string { return e.Message }

// Register mounts the my-chat routes on the kernel mux.
func (d *Deps) Register(k *kernel.Kernel, prefix string) {
	if prefix == "" {
		prefix = "/__aisys__/api/my-chat"
	}
	rt := &chatRoutes{deps: d, preps: map[string]*activePreparation{}, actions: map[string]*activeConversationAction{}}
	mount := func(method, pattern string, handler http.HandlerFunc) {
		k.Register(method+" "+prefix+pattern, rt.wrap(handler))
	}
	mount("GET", "/image-policy", rt.imagePolicy)
	mount("GET", "/conversations", rt.listConversations)
	mount("POST", "/conversations", rt.createConversationHandler)
	mount("GET", "/conversations/{conversationId}", rt.getConversation)
	mount("PATCH", "/conversations/{conversationId}", rt.patchConversation)
	mount("DELETE", "/conversations/{conversationId}", rt.deleteConversation)
	mount("POST", "/conversations/{conversationId}/clear", rt.clearConversation)
	mount("GET", "/conversations/{conversationId}/messages", rt.listMessages)
	mount("GET", "/conversations/{conversationId}/sync", rt.syncHead)
	mount("GET", "/conversations/{conversationId}/submissions/{clientMessageId}", rt.submissionStatus)
	mount("POST", "/conversations/{conversationId}/context/compactions", rt.compactionTrigger)
	mount("GET", "/conversations/{conversationId}/context-status", rt.contextStatus)
	mount("POST", "/conversations/{conversationId}/stop", rt.stopTurn)
	mount("POST", "/conversations/{conversationId}/assets", rt.uploadAsset)
	mount("GET", "/conversations/{conversationId}/assets/{assetId}/content", rt.assetContent)
	mount("DELETE", "/conversations/{conversationId}/assets/{assetId}", rt.deleteAsset)
	mount("GET", "/conversations/{conversationId}/models", rt.listConversationModels)
	mount("GET", "/conversations/{conversationId}/models/{modelId}", rt.getConversationModel)
	mount("POST", "/conversations/{conversationId}/stream", rt.streamTurn)
	mount("GET", "/conversations/{conversationId}/streams/{turnId}", rt.attachStream)
}

func (rt *chatRoutes) wrap(next http.HandlerFunc) http.Handler {
	handler := http.HandlerFunc(next)
	if rt.deps.RequireSession != nil {
		return rt.deps.RequireSession(handler)
	}
	return handler
}

// requireChatAuth mirrors requireChatAuth (belt-and-suspenders behind the
// mount middleware).
func (rt *chatRoutes) requireChatAuth(r *http.Request) (string, error) {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID, nil
	}
	return "", errors.New("请先登录")
}

type messageCodePayload struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// writeChatRouteError mirrors handleChatRouteError.
func writeChatRouteError(w http.ResponseWriter, err error) {
	var invalid *invalidRequestError
	if errors.As(err, &invalid) {
		kernel.WriteJSON(w, http.StatusBadRequest, messageCodePayload{Message: invalid.Message, Code: "chat_invalid_request"})
		return
	}
	var upload *AssetUploadError
	if errors.As(err, &upload) {
		kernel.WriteJSON(w, upload.StatusCode, messageCodePayload{Message: upload.Message, Code: string(upload.Code)})
		return
	}
	var notFound *ConversationNotFoundError
	if errors.As(err, &notFound) {
		kernel.WriteJSON(w, http.StatusNotFound, messageCodePayload{Message: notFound.Error(), Code: "chat_conversation_not_found"})
		return
	}
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteJSON(w, http.StatusConflict, messageCodePayload{Message: conflict.Error(), Code: string(conflict.Code)})
		return
	}
	var capability *ModelCapabilityError
	if errors.As(err, &capability) {
		kernel.WriteJSON(w, http.StatusUnprocessableEntity, messageCodePayload{Message: capability.Message, Code: "chat_model_capability_unavailable"})
		return
	}
	public := ClassifyUnknownChatGenerationError(err)
	kernel.WriteJSON(w, http.StatusInternalServerError, messageCodePayload{Message: public.Message, Code: string(public.Code)})
}

func writeMessageCode(w http.ResponseWriter, status int, message, code string) {
	kernel.WriteJSON(w, status, messageCodePayload{Message: message, Code: code})
}

// okEnvelope mirrors shared/http.ts ok(data) (message omitted).
type okEnvelope struct {
	Data any `json:"data"`
}

func writeOK(w http.ResponseWriter, data any) {
	kernel.WriteJSON(w, http.StatusOK, okEnvelope{Data: data})
}

func writeOKStatus(w http.ResponseWriter, status int, data any) {
	kernel.WriteJSON(w, status, okEnvelope{Data: data})
}

// conversationResponse mirrors chatConversationResponse.
type conversationResponse struct {
	*Conversation
	UserTurnLimit int64 `json:"userTurnLimit"`
}

func (rt *chatRoutes) conversationPayload(conversation *Conversation) conversationResponse {
	return conversationResponse{Conversation: conversation, UserTurnLimit: rt.deps.MaxTurnsPerConversation}
}

// --- raw body/query helpers ---

// readJSONBody reads the request body within the 24 MiB chat limit. An empty
// body renders as an empty object (Express json() default).
func readJSONBody(r *http.Request) (json.RawMessage, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, chatSystemAPIJSONBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > chatSystemAPIJSONBodyLimit {
		return nil, errors.New("body too large")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, &invalidRequestError{Message: "Unexpected token in JSON"}
	}
	return json.RawMessage(trimmed), nil
}

func decodeObjectBody(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return nil, &invalidRequestError{Message: "Expected object, received " + jsonValueTypeName(raw)}
	}
	return parsed, nil
}

func jsonValueTypeName(value json.RawMessage) string {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return "undefined"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func utf8RuneLen(value string) int { return len([]rune(value)) }

// boundedTrimmedString mirrors z.string().trim().min(1).max(n).
func boundedTrimmedString(value json.RawMessage, max int) (*string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return nil, errors.New("Expected string, received " + jsonValueTypeName(value))
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, errors.New("String must contain at least 1 character(s)")
	}
	if utf8RuneLen(trimmed) > max {
		return nil, errors.New("String must contain at most " + strconv.Itoa(max) + " character(s)")
	}
	return &trimmed, nil
}

func ensureStrictQueryKeys(query map[string][]string, allowed ...string) error {
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range query {
		if !allowedSet[key] {
			return &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""}
		}
	}
	return nil
}

// queryScalarInteger mirrors queryScalar + z.coerce.number().int(): only the
// first value of a repeated key participates and "" reads as absent.
func queryScalarInteger(query map[string][]string, key string) (int64, bool, error) {
	values, ok := query[key]
	if !ok || len(values) == 0 {
		return 0, false, nil
	}
	raw := strings.TrimSpace(values[0])
	if raw == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, errors.New("Expected number, received nan")
	}
	return parsed, true, nil
}

func textQuery(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func optionalBooleanQuery(raw string) *bool {
	text := textQuery(raw)
	if text == nil {
		return nil
	}
	switch strings.ToLower(*text) {
	case "true", "1":
		return boolPtr(true)
	case "false", "0":
		return boolPtr(false)
	}
	return nil
}

func boolPtr(value bool) *bool { return &value }

// integerQuery mirrors integerQuery(value, fallback, min, max): invalid or
// non-positive input falls back, then clamps.
func integerQuery(raw string, fallback, min, max int) int {
	text := textQuery(raw)
	if text == nil {
		return fallback
	}
	parsed, err := strconv.Atoi(*text)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

// --- handlers ---

func (rt *chatRoutes) imagePolicy(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.requireChatAuth(r); err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOK(w, struct {
		Input chatImageInputPolicy `json:"input"`
	}{Input: defaultChatImageInputPolicy})
}

func (rt *chatRoutes) listConversations(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	query := r.URL.Query()
	conversations, err := rt.deps.Store.ListConversations(ListConversationsInput{
		SystemAccountID:     ownerID,
		BeforeIsPinned:      optionalBooleanQuery(query.Get("beforeIsPinned")),
		BeforeLastMessageAt: textQuery(query.Get("beforeLastMessageAt")),
		BeforeID:            textQuery(query.Get("beforeId")),
		Limit:               integerQuery(query.Get("limit"), 30, 1, 50),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	payload := make([]conversationResponse, 0, len(conversations))
	for _, conversation := range conversations {
		payload = append(payload, rt.conversationPayload(conversation))
	}
	writeOK(w, payload)
}

func (rt *chatRoutes) getConversation(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		kernel.WriteError(w, http.StatusNotFound, "会话不存在")
		return
	}
	writeOK(w, struct {
		conversationResponse
		ToolCapabilities any `json:"toolCapabilities"`
	}{conversationResponse: rt.conversationPayload(conversation), ToolCapabilities: rt.toolCapabilities(conversation, ownerID)})
}

func (rt *chatRoutes) toolCapabilities(conversation *Conversation, ownerID string) any {
	if rt.deps.ToolCapabilit != nil {
		return rt.deps.ToolCapabilit(conversation, ownerID)
	}
	// Node catch branch: unavailable('工具能力状态暂时无法读取').
	return map[string]any{
		"model": trimmedPointer(conversation.LastModel),
		"tools": []map[string]any{
			{"id": "web_search", "label": "网页搜索", "available": false, "reason": "工具能力状态暂时无法读取"},
			{"id": "generate_image", "label": "图片生成", "available": false, "reason": "工具能力状态暂时无法读取"},
		},
	}
}

func trimmedPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

type updateConversationFields struct {
	title             *string
	isPinned          *bool
	defaultImageModel *string
}

// parseUpdateConversationBody mirrors updateConversationSchema (strict +
// refine, zod issue order).
func parseUpdateConversationBody(raw map[string]json.RawMessage) (updateConversationFields, error) {
	fields := updateConversationFields{}
	for _, key := range []string{"title", "isPinned", "defaultImageModel"} {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch key {
		case "title":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return fields, &invalidRequestError{Message: "Expected string, received " + jsonValueTypeName(value)}
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return fields, &invalidRequestError{Message: "请输入会话标题"}
			}
			if utf8RuneLen(trimmed) > 60 {
				return fields, &invalidRequestError{Message: "会话标题最多 60 个字符"}
			}
			fields.title = &trimmed
		case "isPinned":
			var flag bool
			if err := json.Unmarshal(value, &flag); err != nil {
				return fields, &invalidRequestError{Message: "Expected boolean, received " + jsonValueTypeName(value)}
			}
			fields.isPinned = &flag
		case "defaultImageModel":
			var model string
			if err := json.Unmarshal(value, &model); err != nil {
				return fields, &invalidRequestError{Message: "Expected string, received " + jsonValueTypeName(value)}
			}
			if model != string(ImageModelGPTImage2) {
				return fields, &invalidRequestError{Message: "Invalid enum value. Expected 'gpt-image-2', received '" + model + "'"}
			}
			fields.defaultImageModel = &model
		}
	}
	for key := range raw {
		switch key {
		case "title", "isPinned", "defaultImageModel":
		default:
			return updateConversationFields{}, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""}
		}
	}
	if fields.title == nil && fields.isPinned == nil && fields.defaultImageModel == nil {
		return fields, &invalidRequestError{Message: "没有可更新的会话字段"}
	}
	return fields, nil
}

func (rt *chatRoutes) patchConversation(w http.ResponseWriter, r *http.Request) {
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	body, err := decodeObjectBody(raw)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	fields, err := parseUpdateConversationBody(body)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.UpdateConversation(UpdateConversationInput{
		ConversationID:    r.PathValue("conversationId"),
		SystemAccountID:   ownerID,
		Title:             fields.title,
		IsPinned:          fields.isPinned,
		DefaultImageModel: fields.defaultImageModel,
		Now:               rt.now(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		kernel.WriteError(w, http.StatusNotFound, "会话不存在")
		return
	}
	writeOK(w, rt.conversationPayload(conversation))
}

func (rt *chatRoutes) deleteConversation(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	deleted, err := rt.deps.Store.DeleteConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if !deleted {
		kernel.WriteError(w, http.StatusNotFound, "会话不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *chatRoutes) clearConversation(w http.ResponseWriter, r *http.Request) {
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if strings.TrimSpace(string(raw)) != "{}" {
		if body, err := decodeObjectBody(raw); err != nil {
			writeChatRouteError(w, err)
			return
		} else if len(body) > 0 {
			for key := range body {
				writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""})
				return
			}
		}
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversationID := r.PathValue("conversationId")
	conversation, err := rt.deps.Store.GetConversation(conversationID, ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	if action := rt.getAction(conversation.ID, ownerID); action != nil {
		if action.kind == "compacting" {
			writeChatRouteError(w, &ConflictError{Code: ConflictContextCompacting})
		} else {
			writeChatRouteError(w, &ConflictError{Code: ConflictConversationClearing})
		}
		return
	}
	if rt.getPreparationForConversation(conversation.ID, ownerID) != nil {
		writeChatRouteError(w, &ConflictError{Code: ConflictMessageInProgress})
		return
	}
	claim := rt.claimAction(conversation.ID, ownerID, "clearing")
	if claim == nil {
		writeChatRouteError(w, &ConflictError{Code: ConflictMessageInProgress})
		return
	}
	defer rt.deleteActionIfMatches(conversation.ID, claim.token)
	cleared, err := rt.deps.Store.ClearConversation(ClearConversationInput{
		ConversationID:  conversation.ID,
		SystemAccountID: ownerID,
		Now:             rt.now(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if cleared == nil {
		writeChatRouteError(w, &ConversationNotFoundError{})
		return
	}
	writeOK(w, rt.conversationPayload(cleared))
}

func (rt *chatRoutes) listMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if err := ensureStrictQueryKeys(query, "beforeSequenceNo", "afterSequenceNo", "fromSequenceNo", "limit"); err != nil {
		writeChatRouteError(w, err)
		return
	}
	input := ListMessagesInput{
		ConversationID:  r.PathValue("conversationId"),
		SystemAccountID: "",
		Now:             rt.now(),
	}
	cursorCount := 0
	cursorOrder := []struct {
		key   string
		field **int64
	}{
		{"beforeSequenceNo", &input.BeforeSequenceNo},
		{"afterSequenceNo", &input.AfterSequenceNo},
		{"fromSequenceNo", &input.FromSequenceNo},
	}
	for _, cursor := range cursorOrder {
		value, ok, err := queryScalarInteger(query, cursor.key)
		if err != nil {
			writeChatRouteError(w, &invalidRequestError{Message: err.Error()})
			return
		}
		if !ok {
			continue
		}
		if value < 1 {
			writeChatRouteError(w, &invalidRequestError{Message: "Too small: expected number to be >=1"})
			return
		}
		if value > 2147483647 {
			writeChatRouteError(w, &invalidRequestError{Message: "Too big: expected number to be <=2147483647"})
			return
		}
		normalized := value
		*cursor.field = &normalized
		cursorCount++
	}
	if cursorCount > 1 {
		writeChatRouteError(w, &invalidRequestError{Message: "消息游标只能指定一个"})
		return
	}
	limitValue, ok, err := queryScalarInteger(query, "limit")
	if err != nil {
		writeChatRouteError(w, &invalidRequestError{Message: err.Error()})
		return
	}
	limit := 100
	if ok {
		if limitValue < 1 {
			writeChatRouteError(w, &invalidRequestError{Message: "Too small: expected number to be >=1"})
			return
		}
		if limitValue > 100 {
			writeChatRouteError(w, &invalidRequestError{Message: "Too big: expected number to be <=100"})
			return
		}
		limit = int(limitValue)
	}
	input.Limit = limit
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	input.SystemAccountID = ownerID
	messages, err := rt.deps.Store.ListMessages(input)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	writeOK(w, messages)
}

func (rt *chatRoutes) syncHead(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if err := ensureStrictQueryKeys(query, "knownRevision"); err != nil {
		writeChatRouteError(w, err)
		return
	}
	knownRevision, ok, err := queryScalarInteger(query, "knownRevision")
	if err != nil {
		writeChatRouteError(w, &invalidRequestError{Message: err.Error()})
		return
	}
	if !ok {
		writeChatRouteError(w, &invalidRequestError{Message: "Required"})
		return
	}
	if knownRevision < 0 {
		writeChatRouteError(w, &invalidRequestError{Message: "Too small: expected number to be >=0"})
		return
	}
	if knownRevision > 9007199254740991 {
		writeChatRouteError(w, &invalidRequestError{Message: "Too big: expected number to be <=9007199254740991"})
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	serverTime := rt.now()
	head, err := rt.deps.Store.GetConversationSyncHead(r.PathValue("conversationId"), ownerID, serverTime)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if head == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	var activeTurn *activeTurnPayload
	if head.ActiveTurnID != nil && head.ActiveAssistantMessageID != nil {
		if head.ActiveStartedAt == nil {
			writeChatRouteError(w, &DomainError{Message: "聊天会话 active_started_at必须是带 Z 或数值 offset 的 RFC3339 时间"})
			return
		}
		activeTurn = &activeTurnPayload{
			TurnID:             *head.ActiveTurnID,
			AssistantMessageID: *head.ActiveAssistantMessageID,
			StartedAt:          *head.ActiveStartedAt,
		}
	}
	tail := make([]syncTailMessagePayload, 0, len(head.Tail))
	for _, message := range head.Tail {
		tail = append(tail, syncTailMessagePayload{
			ID:          message.ID,
			TurnID:      message.TurnID,
			SequenceNo:  message.SequenceNo,
			Role:        string(message.Role),
			Status:      string(message.Status),
			CompletedAt: message.CompletedAt,
			ExpiresAt:   message.ExpiresAt,
		})
	}
	writeOK(w, syncHeadPayload{
		ServerTime:      serverTime,
		Unchanged:       knownRevision == head.MessageRevision,
		ConversationID:  head.ConversationID,
		MessageRevision: head.MessageRevision,
		LastSequenceNo:  head.LastSequenceNo,
		ActiveTurn:      activeTurn,
		Tail:            tail,
	})
}

type activeTurnPayload struct {
	TurnID             string `json:"turnId"`
	AssistantMessageID string `json:"assistantMessageId"`
	StartedAt          string `json:"startedAt"`
}

type syncTailMessagePayload struct {
	ID          string  `json:"id"`
	TurnID      string  `json:"turnId"`
	SequenceNo  int64   `json:"sequenceNo"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	CompletedAt *string `json:"completedAt,omitempty"`
	ExpiresAt   string  `json:"expiresAt"`
}

type syncHeadPayload struct {
	ServerTime      string                   `json:"serverTime"`
	Unchanged       bool                     `json:"unchanged"`
	ConversationID  string                   `json:"conversationId"`
	MessageRevision int64                    `json:"messageRevision"`
	LastSequenceNo  int64                    `json:"lastSequenceNo"`
	ActiveTurn      *activeTurnPayload       `json:"activeTurn,omitempty"`
	Tail            []syncTailMessagePayload `json:"tail"`
}

func (rt *chatRoutes) submissionStatus(w http.ResponseWriter, r *http.Request) {
	conversationID := strings.TrimSpace(r.PathValue("conversationId"))
	if conversationID == "" || utf8RuneLen(conversationID) > 120 {
		writeChatRouteError(w, &invalidRequestError{Message: "String must contain at most 120 character(s)"})
		return
	}
	clientMessageID := strings.TrimSpace(r.PathValue("clientMessageId"))
	if clientMessageID == "" || utf8RuneLen(clientMessageID) > 100 {
		writeChatRouteError(w, &invalidRequestError{Message: "String must contain at most 100 character(s)"})
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(conversationID, ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	accepted, err := rt.deps.Store.FindTurnByClientMessageID(conversation.ID, ownerID, clientMessageID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if accepted != nil {
		serverTime := rt.now()
		snapshot := GenerationSnapshot{State: "missing"}
		if rt.deps.Generations != nil {
			snapshot = rt.deps.Generations.Snapshot(ownerID, conversation.ID, accepted.TurnID)
		}
		snapshotMatches := snapshot.State != "missing" && snapshot.AssistantMessageID == accepted.AssistantMessageID
		runnerState := "terminal"
		if accepted.AssistantStatus == StatusStreaming {
			runnerState = "missing"
			if snapshotMatches {
				runnerState = snapshot.State
			}
		}
		payload := submissionAcceptedPayload{
			State:              "accepted",
			TurnID:             accepted.TurnID,
			AssistantMessageID: accepted.AssistantMessageID,
			AssistantStatus:    string(accepted.AssistantStatus),
			RunnerState:        runnerState,
			TraceID:            accepted.TraceID,
			CompletedAt:        accepted.CompletedAt,
			ServerTime:         serverTime,
		}
		if snapshotMatches {
			payload.EventVersion = snapshot.EventVersion
			payload.LastSemanticActivityAt = snapshot.LastSemanticActivityAt
		}
		if accepted.ErrorCode != nil {
			public := ClassifyChatGenerationErrorByCode(*accepted.ErrorCode)
			errorCode := string(public.Code)
			payload.ErrorCode = &errorCode
			if accepted.ErrorMessage != nil {
				payload.ErrorMessage = accepted.ErrorMessage
			} else {
				payload.ErrorMessage = &public.Message
			}
		}
		writeOK(w, payload)
		return
	}
	preparation := rt.getPreparation(conversation.ID, ownerID, clientMessageID)
	payload := struct {
		State      string  `json:"state"`
		Phase      *string `json:"phase,omitempty"`
		ServerTime string  `json:"serverTime"`
	}{State: "not_found", ServerTime: rt.now()}
	if preparation != nil {
		payload.State = "preparing"
		payload.Phase = stringPtr(preparation.phase)
	}
	writeOK(w, payload)
}

type submissionAcceptedPayload struct {
	State                  string  `json:"state"`
	TurnID                 string  `json:"turnId"`
	AssistantMessageID     string  `json:"assistantMessageId"`
	AssistantStatus        string  `json:"assistantStatus"`
	RunnerState            string  `json:"runnerState"`
	EventVersion           *int64  `json:"eventVersion,omitempty"`
	LastSemanticActivityAt *string `json:"lastSemanticActivityAt,omitempty"`
	ErrorCode              *string `json:"errorCode,omitempty"`
	ErrorMessage           *string `json:"errorMessage,omitempty"`
	TraceID                *string `json:"traceId,omitempty"`
	CompletedAt            *string `json:"completedAt,omitempty"`
	ServerTime             string  `json:"serverTime"`
}

func (rt *chatRoutes) contextStatus(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	head, err := rt.deps.Store.GetContextHead(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if head == nil {
		kernel.WriteError(w, http.StatusNotFound, "会话不存在")
		return
	}
	usedTokens := int64(0)
	if head.ActiveContextTokens != nil {
		usedTokens = *head.ActiveContextTokens
	}
	ratio := float64(0)
	if head.EffectiveContextLimitTokens != nil && *head.EffectiveContextLimitTokens != 0 {
		ratio = math.Min(1, float64(usedTokens)/float64(*head.EffectiveContextLimitTokens))
	}
	writeOK(w, contextStatusPayload{
		UsedTokens:               usedTokens,
		LimitTokens:              head.EffectiveContextLimitTokens,
		Ratio:                    ratio,
		State:                    string(head.ContextState),
		UsageEstimated:           head.UsageEstimated,
		CompactedThroughSequence: head.CompactedThroughSequence,
		Revision:                 head.ContextRevision,
		ErrorCode:                head.ContextErrorCode,
		RetryAt:                  head.ContextRetryAt,
		AttemptCount:             head.ContextAttemptCount,
	})
}

type contextStatusPayload struct {
	UsedTokens               int64   `json:"usedTokens"`
	LimitTokens              *int64  `json:"limitTokens"`
	Ratio                    float64 `json:"ratio"`
	State                    string  `json:"state"`
	UsageEstimated           bool    `json:"usageEstimated"`
	CompactedThroughSequence int64   `json:"compactedThroughSequence"`
	Revision                 int64   `json:"revision"`
	ErrorCode                *string `json:"errorCode"`
	RetryAt                  *string `json:"retryAt"`
	AttemptCount             int64   `json:"attemptCount"`
}

func (rt *chatRoutes) stopTurn(w http.ResponseWriter, r *http.Request) {
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	body, err := decodeObjectBody(raw)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	var turnID, clientMessageID *string
	for _, key := range []string{"turnId", "clientMessageId"} {
		value, ok := body[key]
		if !ok {
			continue
		}
		text, err := boundedTrimmedString(value, 100)
		if err != nil {
			writeChatRouteError(w, &invalidRequestError{Message: err.Error()})
			return
		}
		if key == "turnId" {
			turnID = text
		} else {
			clientMessageID = text
		}
	}
	for key := range body {
		switch key {
		case "turnId", "clientMessageId":
		default:
			writeChatRouteError(w, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""})
			return
		}
	}
	if turnID == nil && clientMessageID == nil {
		writeChatRouteError(w, &invalidRequestError{Message: "缺少要停止的消息或轮次"})
		return
	}
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	expectedTurnID := turnID
	if clientMessageID != nil {
		accepted, err := rt.deps.Store.FindTurnByClientMessageID(conversation.ID, ownerID, *clientMessageID)
		if err != nil {
			writeChatRouteError(w, err)
			return
		}
		if accepted != nil {
			if expectedTurnID != nil && *expectedTurnID != accepted.TurnID {
				writeMessageCode(w, http.StatusConflict, "要停止的轮次已变化", "chat_turn_mismatch")
				return
			}
			expectedTurnID = stringPtr(accepted.TurnID)
		} else if expectedTurnID == nil {
			if phase, canceled := rt.cancelPreparation(conversation.ID, ownerID, *clientMessageID); canceled {
				writeOKStatus(w, http.StatusAccepted, struct {
					Stopped          bool   `json:"stopped"`
					PreparationPhase string `json:"preparationPhase"`
				}{Stopped: true, PreparationPhase: phase})
				return
			}
			writeMessageCode(w, http.StatusNotFound, "当前没有匹配的准备或生成任务", "chat_generation_not_found")
			return
		}
	}
	if expectedTurnID == nil {
		writeMessageCode(w, http.StatusNotFound, "当前没有匹配的生成任务", "chat_generation_not_found")
		return
	}
	if rt.deps.Generations != nil {
		if runner, active := rt.deps.Generations.Get(ownerID, conversation.ID, *expectedTurnID); active {
			runner.Abort()
			writeOKStatus(w, http.StatusAccepted, struct {
				Stopped bool   `json:"stopped"`
				TurnID  string `json:"turnId"`
			}{Stopped: true, TurnID: *expectedTurnID})
			return
		}
	}
	result, err := rt.deps.Store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID:  conversation.ID,
		SystemAccountID: ownerID,
		ExpectedTurnID:  *expectedTurnID,
		Now:             rt.now(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	switch result.State {
	case CancelStateCanceled, CancelStateAlreadyTerminal:
		writeOKStatus(w, http.StatusAccepted, stopAcceptedPayload{
			Stopped:         true,
			TurnID:          *expectedTurnID,
			State:           string(result.State),
			AssistantStatus: string(result.AssistantStatus),
		})
	case CancelStateTurnMismatch:
		writeMessageCode(w, http.StatusConflict, "要停止的轮次已变化", "chat_turn_mismatch")
	default:
		writeMessageCode(w, http.StatusNotFound, "当前没有匹配的生成任务", "chat_generation_not_found")
	}
}

type stopAcceptedPayload struct {
	Stopped         bool   `json:"stopped"`
	TurnID          string `json:"turnId"`
	State           string `json:"state"`
	AssistantStatus string `json:"assistantStatus"`
}

func (rt *chatRoutes) attachStream(w http.ResponseWriter, r *http.Request) {
	ownerID, err := rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	turnID := r.PathValue("turnId")
	if conversation.ActiveTurnID == nil || *conversation.ActiveTurnID != turnID {
		writeMessageCode(w, http.StatusConflict, "要附着的轮次已结束或已变化", "chat_stream_terminal")
		return
	}
	if rt.deps.Generations != nil && rt.deps.AttachStream != nil {
		if _, active := rt.deps.Generations.Get(ownerID, conversation.ID, turnID); active {
			rt.deps.AttachStream(w, r, GenerationIdentity{
				OwnerID:        ownerID,
				ConversationID: conversation.ID,
				TurnID:         turnID,
			})
			return
		}
	}
	interrupted, err := rt.deps.Store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:  conversation.ID,
		SystemAccountID: ownerID,
		ExpectedTurnID:  turnID,
		Now:             rt.now(),
	})
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if interrupted.State == CancelStateAlreadyTerminal {
		writeMessageCode(w, http.StatusConflict, "要附着的轮次已结束", "chat_stream_terminal")
		return
	}
	writeMessageCode(w, http.StatusConflict, "生成任务已中断，请刷新会话", "chat_stream_runner_missing")
}
