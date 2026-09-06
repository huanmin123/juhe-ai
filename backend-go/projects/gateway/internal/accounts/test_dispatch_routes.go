package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Manual account test diagnostic routes: the port of
// account-test-dispatch.routes.ts (test-options pair + POST /{id}/test),
// account-test-session.routes.ts (session lifecycle + cancel pair) and
// account-test-status.routes.ts (task/session status reads). Node registers
// every router on the shared accounts router, so both the admin surface and
// the my-* self surface expose the family.

// accountTestEndpointModeValues mirrors accountTestEndpointModeSchema.
var accountTestEndpointModeValues = []string{
	"images_json",
	"chat_json", "chat_sse",
	"responses_json", "responses_sse",
	"messages_json", "messages_sse",
	"generate_content_json", "generate_content_sse",
	"interactions_json", "interactions_sse",
}

// unsupportedGatewayProtocolTestMessage mirrors the route copy.
const unsupportedGatewayProtocolTestMessage = "当前仅支持测试 OpenAI、Anthropic 或 Gemini 协议账户"

// testWorkerUnavailableMessage mirrors the dispatch-failure copy.
const testWorkerUnavailableMessage = "后台 worker 暂不可用，账号测试任务未能投递"

// mountTestRoutes wires the diagnostic family on both surfaces. The status
// and cancel namespaces use literal-first multi-segment paths
// (/accounts/test-tasks/{taskId}, /accounts/test-sessions/{sessionId}/...),
// which http.ServeMux cannot register alongside the /accounts/{id}/... family
// (neither pattern is more specific than the other). They therefore ride the
// least-specific /accounts/ and /my-accounts/ subtree handlers, which dispatch
// on the exact Node path shapes and fall through to the 404 JSON contract;
// the /{id}/test-options and /{id}/test routes carry no ambiguity and
// register directly.
func (d *Deps) mountTestRoutes(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := d.Auth.RequireAdmin
	self := d.Auth.RequireSession(true)

	// Dispatch family (account-test-dispatch.routes.ts).
	k.Register("GET "+prefix+"/accounts/{id}/test-options", admin(d.scoped(d.testOptions)))
	k.Register("GET "+prefix+"/accounts/{id}/test-options/models/{modelId}", admin(d.scoped(d.testOptionsModel)))
	k.Register("POST "+prefix+"/accounts/{id}/test", admin(d.scoped(d.testAccount)))
	k.Register("GET "+prefix+"/my-accounts/{id}/test-options", self(d.scoped(d.testOptions)))
	k.Register("GET "+prefix+"/my-accounts/{id}/test-options/models/{modelId}", self(d.scoped(d.testOptionsModel)))
	k.Register("POST "+prefix+"/my-accounts/{id}/test", self(d.scoped(d.testAccount)))

	// Session/task namespaces (account-test-session.routes.ts +
	// account-test-status.routes.ts). Single-segment literals register
	// directly (more specific than /accounts/{id}); the multi-segment shapes
	// ride the least-specific /accounts/ and /my-accounts/ subtree handlers,
	// which dispatch on the exact Node path shapes and fall through to the
	// 404 JSON contract.
	k.Register("POST "+prefix+"/accounts/test-sessions", admin(d.scoped(func(w http.ResponseWriter, r *http.Request) {
		d.createTestSession(w, r, requestScope(r))
	})))
	k.Register("POST "+prefix+"/my-accounts/test-sessions", self(d.scoped(func(w http.ResponseWriter, r *http.Request) {
		d.createTestSession(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/accounts/test-tasks", admin(d.scoped(func(w http.ResponseWriter, r *http.Request) {
		d.listTestTasks(w, r, requestScope(r))
	})))
	k.Register("GET "+prefix+"/my-accounts/test-tasks", self(d.scoped(func(w http.ResponseWriter, r *http.Request) {
		d.listTestTasks(w, r, selfScope(r))
	})))
	k.Register("GET "+prefix+"/accounts/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.testTreeGET(w, r, false)
	})))
	k.Register("POST "+prefix+"/accounts/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.testTreePOST(w, r, false)
	})))
	k.Register("GET "+prefix+"/my-accounts/", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.testTreeGET(w, r, true)
	})))
	k.Register("POST "+prefix+"/my-accounts/", self(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.testTreePOST(w, r, true)
	})))
}

// testTreeSegments splits the request path below the surface prefix
// ("/accounts" or "/my-accounts"); ok is false for paths outside it.
func testTreeSegments(r *http.Request, surface string) ([]string, bool) {
	prefix := "/__aisys__/api" + surface
	if r.URL.Path == prefix {
		return nil, true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, prefix+"/")
	if !ok {
		return nil, false
	}
	return strings.Split(strings.Trim(rest, "/"), "/"), true
}

func (d *Deps) testTreeGET(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	surface := "/accounts"
	if selfOnly {
		surface = "/my-accounts"
	}
	segs, ok := testTreeSegments(r, surface)
	if !ok {
		kernel.WriteAPINotFound(w)
		return
	}
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	access := requestScope(r)
	if selfOnly {
		access = selfScope(r)
	}
	switch {
	case len(segs) == 2 && segs[0] == "test-tasks":
		d.getTestTask(w, r, access, segs[1])
	case len(segs) == 2 && segs[0] == "test-sessions":
		d.getTestSession(w, r, access, segs[1])
	case len(segs) == 3 && segs[0] == "test-sessions" && segs[2] == "tasks":
		d.getTestSessionTasks(w, r, access, segs[1])
	default:
		kernel.WriteAPINotFound(w)
	}
}

func (d *Deps) testTreePOST(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	surface := "/accounts"
	if selfOnly {
		surface = "/my-accounts"
	}
	segs, ok := testTreeSegments(r, surface)
	if !ok {
		kernel.WriteAPINotFound(w)
		return
	}
	if !scopeQueryOK(r) {
		kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
		return
	}
	access := requestScope(r)
	if selfOnly {
		access = selfScope(r)
	}
	switch {
	case len(segs) == 3 && segs[0] == "test-sessions" && segs[2] == "heartbeat":
		d.heartbeatTestSession(w, r, access, segs[1])
	case len(segs) == 3 && segs[0] == "test-sessions" && segs[2] == "complete":
		d.completeTestSession(w, r, access, segs[1])
	case len(segs) == 3 && segs[0] == "test-sessions" && segs[2] == "cancel":
		d.cancelTestSession(w, r, access, segs[1])
	case len(segs) == 3 && segs[0] == "test-tasks" && segs[2] == "cancel":
		d.cancelTestTask(w, r, access, segs[1])
	default:
		kernel.WriteAPINotFound(w)
	}
}

// testAccess resolves the AccessScope for the surface (requestScope mirrors
// getRequestAccessScope: admins carry the query filter, the self surface pins
// the caller).
func (d *Deps) testAccess(r *http.Request) AccessScope {
	return requestScope(r)
}

// ---- dispatch routes ----

func (d *Deps) testOptions(w http.ResponseWriter, r *http.Request) {
	access := d.testAccess(r)
	ctx := ensureCtx(r.Context())
	context, err := d.Store.FindManualTestOptionsContext(ctx, r.PathValue("id"), &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if context == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	query, message := NormalizeManualTestOptionsQuery(r.URL.Query())
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	options, err := d.Store.AccountManualTestOptions(ctx, context, query)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, options, "")
}

func (d *Deps) testOptionsModel(w http.ResponseWriter, r *http.Request) {
	access := d.testAccess(r)
	ctx := ensureCtx(r.Context())
	context, err := d.Store.FindManualTestCapabilitiesContext(ctx, r.PathValue("id"), r.PathValue("modelId"), &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if context == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	capabilities, err := d.Store.AccountManualTestModelCapabilities(ctx, context, r.PathValue("modelId"))
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, capabilities, "")
}

// testAccount mirrors POST /:id/test: visibility + protocol + availability
// gates, optional draft snapshot, model/endpoint-mode selection, task row and
// the worker dispatch.
func (d *Deps) testAccount(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	access := d.testAccess(r)
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, message := parseTestRequestBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	ctx := ensureCtx(r.Context())
	view, err := d.Store.FindAccountForTestView(ctx, access, r.PathValue("id"))
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if view == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户不存在")
		return
	}
	if !isGatewaySupportedTestProtocol(view.item) {
		kernel.WriteBadRequest(w, unsupportedGatewayProtocolTestMessage)
		return
	}
	if unavailable := accountTestUnavailableMessage(view.item, d.Store.now()); unavailable != "" {
		kernel.WriteBadRequest(w, unavailable)
		return
	}

	diagnostics := "full"
	if !access.IsAdmin && view.item.AccessType == "authorized" {
		diagnostics = "limited"
	}

	var draft *TestDraftSnapshot
	if parsed.Account != nil {
		draft, err = d.Store.savedAccountDraftTestSnapshot(ctx, view, parsed.Account, access)
		if err != nil {
			d.writeTestError(w, err)
			return
		}
	}

	var model, testEndpointMode string
	if draft != nil {
		model, testEndpointMode, err = d.Store.ResolveAccountManualTestSelection(ctx,
			manualTestCapabilitiesContextFromDraft(draft),
			draft.HealthCheckModel,
			firstNonEmptyTextValue(parsed.TestEndpointMode, draft.HealthCheckEndpointMode))
	} else {
		model, testEndpointMode, err = d.Store.ResolveAccountManualTestSelection(ctx,
			view.manualContext(),
			parsed.Model,
			parsed.TestEndpointMode)
	}
	if err != nil {
		d.writeTestError(w, err)
		return
	}

	task, err := d.Store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID:                 view.item.ID,
		AccountName:               view.item.Name,
		ProviderCode:              view.item.ProviderCode,
		ProviderProtocolProfileID: view.item.ProviderProtocolProfileID,
		ProtocolCode:              view.item.ProtocolCode,
		ProtocolVersion:           view.item.ProtocolVersion,
		AccountType:               view.item.Type,
		Access:                    access,
		Diagnostics:               diagnostics,
		SessionID:                 parsed.TestSessionID,
		Model:                     model,
		TestEndpointMode:          testEndpointMode,
		Draft:                     draft,
	})
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if effects := d.Store.testEffectsOrNil(); effects != nil && effects.DispatchAccountTestTasks(r.Context(), []string{task.ID}) {
		setNoStoreHeaders(w)
		kernel.WriteJSON(w, http.StatusAccepted, map[string]any{"data": task, "message": ""})
		return
	}
	_ = d.Store.FailTestTask(r.Context(), task.ID, testWorkerUnavailableMessage)
	kernel.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"message": testWorkerUnavailableMessage})
}

func firstNonEmptyTextValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ---- session routes ----

func (d *Deps) createTestSession(w http.ResponseWriter, r *http.Request, access AccessScope) {
	session, err := d.Store.CreateTestSession(ensureCtx(r.Context()), access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteJSON(w, http.StatusCreated, map[string]any{"data": session, "message": ""})
}

func (d *Deps) heartbeatTestSession(w http.ResponseWriter, r *http.Request, access AccessScope, sessionID string) {
	session, err := d.Store.HeartbeatTestSession(ensureCtx(r.Context()), sessionID, &access)
	d.writeSessionResponse(w, session, err)
}

func (d *Deps) completeTestSession(w http.ResponseWriter, r *http.Request, access AccessScope, sessionID string) {
	session, err := d.Store.CompleteTestSession(ensureCtx(r.Context()), sessionID, &access)
	d.writeSessionResponse(w, session, err)
}

func (d *Deps) writeSessionResponse(w http.ResponseWriter, session *AccountTestSession, err error) {
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if session == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户测试会话不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, session, "")
}

func (d *Deps) cancelTestSession(w http.ResponseWriter, r *http.Request, access AccessScope, sessionID string) {
	result, err := d.Store.CancelTestSession(ensureCtx(r.Context()), sessionID, &access, "已停止测试")
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if result == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户测试会话不存在")
		return
	}
	if effects := d.Store.testEffectsOrNil(); effects != nil {
		for _, taskID := range result.TaskIDs {
			effects.DispatchAccountTestCancel(taskID)
		}
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, &result.Session, "")
}

func (d *Deps) cancelTestTask(w http.ResponseWriter, r *http.Request, access AccessScope, taskID string) {
	task, err := d.Store.CancelTestTask(ensureCtx(r.Context()), taskID, &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if task == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户测试任务不存在")
		return
	}
	if effects := d.Store.testEffectsOrNil(); effects != nil {
		effects.DispatchAccountTestCancel(task.ID)
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, task, "")
}

// ---- status routes ----

func (d *Deps) listTestTasks(w http.ResponseWriter, r *http.Request, access AccessScope) {
	tasks, err := d.Store.ListTestTasks(ensureCtx(r.Context()), textListQuery(r.URL.Query()["ids"]), &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, tasks, "")
}

func (d *Deps) getTestTask(w http.ResponseWriter, r *http.Request, access AccessScope, taskID string) {
	task, err := d.Store.GetTestTask(ensureCtx(r.Context()), taskID, &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if task == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户测试任务不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, task, "")
}

func (d *Deps) getTestSession(w http.ResponseWriter, r *http.Request, access AccessScope, sessionID string) {
	session, err := d.Store.GetTestSession(ensureCtx(r.Context()), sessionID, &access)
	d.writeSessionResponse(w, session, err)
}

func (d *Deps) getTestSessionTasks(w http.ResponseWriter, r *http.Request, access AccessScope, sessionID string) {
	_, tasks, err := d.Store.GetTestSessionDetail(ensureCtx(r.Context()), sessionID, &access)
	if err != nil {
		d.writeTestError(w, err)
		return
	}
	if tasks == nil {
		kernel.WriteError(w, http.StatusNotFound, "账户测试会话不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, tasks, "")
}

// writeTestError maps store errors onto the Node route family contract:
// validation messages render 400 with the store copy, everything else is an
// opaque 500.
func (d *Deps) writeTestError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	println("accounts test slice internal error: " + err.Error())
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// ---- request body (accountTestSchema) ----

// TestDraftAccountInput mirrors accountDraftTestAccountSchema.parse output.
type TestDraftAccountInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	Type                      string
	Credentials               Credentials
	SupportedModels           []string
	HealthCheckModel          string
	HealthCheckEndpointMode   string
	ModelMappings             []ModelMapping
	ConcurrencyLimit          *int
	Priority                  *int
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	ProxyProfileID            *string
	GroupID                   string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
}

// TestRequestBody mirrors accountTestSchema.parse output (the prompt field
// parses but is discarded, exactly like the Node destructure).
type TestRequestBody struct {
	Model            string
	TestEndpointMode string
	TestSessionID    string
	Account          *TestDraftAccountInput
}

func isAccountTestEndpointMode(value string) bool {
	for _, mode := range accountTestEndpointModeValues {
		if mode == value {
			return true
		}
	}
	return false
}

// parseTestRequestBody mirrors accountTestSchema.safeParse (strict key sets;
// any failure renders the route's 账户测试参数无效 message).
func parseTestRequestBody(body map[string]any) (TestRequestBody, string) {
	parsed := TestRequestBody{}
	for key := range body {
		switch key {
		case "model", "testEndpointMode", "prompt", "testSessionId", "account":
		default:
			return parsed, "账户测试参数无效"
		}
	}
	if value, exists := body["model"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return parsed, "账户测试参数无效"
		}
		parsed.Model = strings.TrimSpace(text)
	}
	if value, exists := body["testEndpointMode"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || !isAccountTestEndpointMode(strings.TrimSpace(text)) {
			return parsed, "账户测试参数无效"
		}
		parsed.TestEndpointMode = strings.TrimSpace(text)
	}
	if value, exists := body["prompt"]; exists && value != nil {
		if _, ok := value.(string); !ok {
			return parsed, "账户测试参数无效"
		}
	}
	if value, exists := body["testSessionId"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return parsed, "账户测试参数无效"
		}
		parsed.TestSessionID = strings.TrimSpace(text)
	}
	if value, exists := body["account"]; exists && value != nil {
		record, ok := value.(map[string]any)
		if !ok {
			return parsed, "账户测试参数无效"
		}
		account, message := parseTestDraftAccountInput(record)
		if message != "" {
			return parsed, "账户测试参数无效"
		}
		parsed.Account = account
	}
	return parsed, ""
}

func parseTestDraftAccountInput(record map[string]any) (*TestDraftAccountInput, string) {
	input := &TestDraftAccountInput{}
	for key := range record {
		switch key {
		case "providerCode", "providerProtocolProfileId", "name", "type", "credentials",
			"supportedModels", "healthCheckModel", "healthCheckEndpointMode", "modelMappings",
			"concurrencyLimit", "priority", "superPriorityEnabled", "fallbackEnabled",
			"proxyProfileId", "groupId", "accountExpiresAt", "availabilitySchedule", "notes":
		default:
			return nil, "账户测试参数无效"
		}
	}
	text := func(key string) (string, bool) {
		value, exists := record[key]
		if !exists || value == nil {
			return "", false
		}
		read, ok := value.(string)
		if !ok {
			return "", true
		}
		return strings.TrimSpace(read), true
	}
	input.ProviderCode, _ = text("providerCode")
	input.ProviderProtocolProfileID, _ = text("providerProtocolProfileId")
	input.Name, _ = text("name")
	input.Type, _ = text("type")
	input.HealthCheckModel, _ = text("healthCheckModel")
	if value, exists := record["healthCheckEndpointMode"]; exists && value != nil {
		mode, ok := value.(string)
		if !ok || !isAccountHealthCheckEndpointMode(strings.TrimSpace(mode)) {
			return nil, "账户测试参数无效"
		}
		input.HealthCheckEndpointMode = strings.TrimSpace(mode)
	}
	if input.ProviderCode == "" || input.ProviderProtocolProfileID == "" || input.Name == "" ||
		input.Type == "" || input.HealthCheckModel == "" || input.HealthCheckEndpointMode == "" {
		return nil, "账户测试参数无效"
	}
	if value, exists := record["groupId"]; exists && value != nil {
		groupID, ok := value.(string)
		if !ok || strings.TrimSpace(groupID) == "" {
			return nil, "账户测试参数无效"
		}
		input.GroupID = strings.TrimSpace(groupID)
	}
	if input.GroupID == "" {
		return nil, "账户测试参数无效"
	}
	if value, exists := record["credentials"]; exists && value != nil {
		credentials, ok := value.(map[string]any)
		if !ok {
			return nil, "账户测试参数无效"
		}
		input.Credentials = Credentials(credentials)
	}
	if value, exists := record["supportedModels"]; exists && value != nil {
		list, ok := value.([]any)
		if !ok || len(list) < 1 || len(list) > 500 {
			return nil, "账户测试参数无效"
		}
		models := []string{}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, "账户测试参数无效"
			}
			models = append(models, strings.TrimSpace(text))
		}
		input.SupportedModels = models
	}
	if value, exists := record["modelMappings"]; exists && value != nil {
		list, ok := value.([]any)
		if !ok || len(list) > 500 {
			return nil, "账户测试参数无效"
		}
		for _, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, "账户测试参数无效"
			}
			mapping, ok := normalizeModelMappingBody(object)
			if !ok {
				return nil, "账户测试参数无效"
			}
			input.ModelMappings = append(input.ModelMappings, mapping)
		}
	}
	if value, exists := record["concurrencyLimit"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 1 {
			return nil, "账户测试参数无效"
		}
		limit := int(number)
		input.ConcurrencyLimit = &limit
	}
	if value, exists := record["priority"]; exists && value != nil {
		number, ok := value.(float64)
		if !ok || number != float64(int(number)) || number < 0 {
			return nil, "账户测试参数无效"
		}
		priority := int(number)
		input.Priority = &priority
	}
	if value, exists := record["superPriorityEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return nil, "账户测试参数无效"
		}
		input.SuperPriorityEnabled = &enabled
	}
	if value, exists := record["fallbackEnabled"]; exists && value != nil {
		enabled, ok := value.(bool)
		if !ok {
			return nil, "账户测试参数无效"
		}
		input.FallbackEnabled = &enabled
	}
	if value, exists := record["proxyProfileId"]; exists {
		if value == nil {
			input.ProxyProfileID = nil
		} else if text, ok := value.(string); ok {
			trimmed := strings.TrimSpace(text)
			input.ProxyProfileID = &trimmed
		} else {
			return nil, "账户测试参数无效"
		}
	}
	if value, exists := record["accountExpiresAt"]; exists {
		if value == nil {
			input.AccountExpiresAt = nil
		} else if text, ok := value.(string); ok {
			trimmed := strings.TrimSpace(text)
			input.AccountExpiresAt = &trimmed
		} else {
			return nil, "账户测试参数无效"
		}
	}
	if value, exists := record["availabilitySchedule"]; exists && value != nil {
		if _, ok := value.(map[string]any); !ok {
			return nil, "账户测试参数无效"
		}
		input.AvailabilitySchedule = value
	}
	if value, exists := record["notes"]; exists && value != nil {
		note, ok := value.(string)
		if !ok {
			return nil, "账户测试参数无效"
		}
		input.Notes = &note
	}
	return input, ""
}

// ---- availability gate (accountTestUnavailableMessage) ----

// accountTestUnavailableMessage mirrors the same-named helper: authorized
// instances only, with the runtime-scope and recoverable instance failure
// exceptions.
func accountTestUnavailableMessage(item ListItem, now time.Time) string {
	if item.AccessType != "authorized" {
		return ""
	}
	if item.EffectiveAvailability.Available {
		return ""
	}
	blocker := ""
	if item.EffectiveAvailability.BlockerScope != nil {
		blocker = *item.EffectiveAvailability.BlockerScope
	}
	if blocker == "runtime" {
		return ""
	}
	if blocker == "authorized_instance" {
		switch item.EffectiveAvailability.Status {
		case "instance_disabled":
			return ""
		case "instance_error", "instance_pending_test", "instance_rate_limited",
			"instance_temporary_unavailable", "instance_cooldown":
			if canTestAuthorizedInstanceFailureState(item, now) {
				return ""
			}
		}
	}
	if item.EffectiveAvailability.Reason != nil && *item.EffectiveAvailability.Reason != "" {
		return *item.EffectiveAvailability.Reason
	}
	return item.EffectiveAvailability.Label
}

// canTestAuthorizedInstanceFailureState mirrors the same-named helper.
func canTestAuthorizedInstanceFailureState(item ListItem, now time.Time) bool {
	if item.AccessType != "authorized" || item.BoundGroupID == nil {
		return false
	}
	if item.Status == "active" || item.Status == "disabled" {
		return false
	}
	// isAuthorizedInstanceAvailable: the only unavailability input is account
	// expiry (authorizedAccountInstanceUnavailableMessage).
	if item.AccountExpiresAt != nil && isAccountExpired(*item.AccountExpiresAt, now) {
		return false
	}
	return true
}

// isGatewaySupportedTestProtocol mirrors isGatewaySupportedProtocolProfile on
// the account summary fields.
func isGatewaySupportedTestProtocol(item ListItem) bool {
	profile := protocolPredicateInput{
		providerCode:              item.ProviderCode,
		protocolCode:              item.ProtocolCode,
		protocolVersion:           item.ProtocolVersion,
		providerProtocolProfileID: item.ProviderProtocolProfileID,
	}
	return isOpenAIProtocolProfileOf(profile) ||
		isAnthropicProtocolProfileOf(profile) ||
		isGeminiProtocolProfileOf(profile)
}

// ---- POST /test account view ----

// AccountForTestView mirrors the findAccountForTestAsync projection: the
// visible management summary plus the fact-account credential modes and
// mappings the selection step consumes.
type AccountForTestView struct {
	item          ListItem
	factAccountID string
	modeSource    manualTestModeSource
}

func (v *AccountForTestView) manualContext() *ManualTestContext {
	return &ManualTestContext{
		ID:                        v.item.ID,
		FactAccountID:             v.factAccountID,
		OwnerSystemAccountID:      v.item.OwnerSystemAccountID,
		ProviderCode:              v.item.ProviderCode,
		ProviderProtocolProfileID: v.item.ProviderProtocolProfileID,
		ProtocolCode:              v.item.ProtocolCode,
		ProtocolVersion:           v.item.ProtocolVersion,
		Type:                      v.item.Type,
		ClientCompatibility:       v.item.ClientCompatibility,
		HealthCheckModel:          v.item.HealthCheckModel,
		HealthCheckEndpointMode:   v.item.HealthCheckEndpointMode,
		SupportedEndpointModes:    v.modeSource.supportedEndpointModes,
		ModelMappings:             v.modeSource.modelMappings,
	}
}

// FindAccountForTestView resolves the POST /test account projection
// (findAccountSummary visibility + findAccountForTest credentials merge).
func (s *Store) FindAccountForTestView(ctx context.Context, access AccessScope, accountID string) (*AccountForTestView, error) {
	ctx = ensureCtx(ctx)
	page, err := s.ListPage(ctx, access, ListOptions{IDs: []string{accountID}, PageSize: 1})
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.Items) == 0 || !page.Items[0].Permissions.CanUse {
		return nil, nil
	}
	item := page.Items[0]
	// Fact-account credential modes + mappings (the authorized-instance
	// source account carries the credentials).
	context, err := s.FindManualTestOptionsContext(ctx, accountID, &access)
	if err != nil {
		return nil, err
	}
	view := &AccountForTestView{item: item}
	if context != nil {
		view.factAccountID = context.FactAccountID
		view.modeSource = context.modeSource()
	} else {
		view.factAccountID = item.ID
		view.modeSource = manualTestModeSource{
			providerCode:              item.ProviderCode,
			providerProtocolProfileID: item.ProviderProtocolProfileID,
			protocolCode:              item.ProtocolCode,
			protocolVersion:           item.ProtocolVersion,
			accountType:               item.Type,
			clientCompatibility:       item.ClientCompatibility,
			healthCheckEndpointMode:   item.HealthCheckEndpointMode,
		}
	}
	return view, nil
}

// ---- draft snapshot (account-draft-test.service.ts) ----

// manualTestCapabilitiesContextFromDraft mirrors
// accountManualTestCapabilitiesContextFromDraft.
func manualTestCapabilitiesContextFromDraft(draft *TestDraftSnapshot) *ManualTestContext {
	factAccountID := draft.ID
	if draft.StateTargetAccountID != nil && *draft.StateTargetAccountID != "" {
		factAccountID = *draft.StateTargetAccountID
	}
	profileID := ""
	if draft.ProviderProtocolProfileID != nil {
		profileID = *draft.ProviderProtocolProfileID
	}
	protocolCode := ""
	if draft.ProtocolCode != nil {
		protocolCode = *draft.ProtocolCode
	}
	protocolVersion := ""
	if draft.ProtocolVersion != nil {
		protocolVersion = *draft.ProtocolVersion
	}
	return &ManualTestContext{
		ID:                        draft.ID,
		FactAccountID:             factAccountID,
		OwnerSystemAccountID:      draft.OwnerSystemAccountID,
		ProviderCode:              draft.ProviderCode,
		ProviderProtocolProfileID: profileID,
		ProtocolCode:              protocolCode,
		ProtocolVersion:           protocolVersion,
		Type:                      draft.Type,
		ClientCompatibility:       draft.ClientCompatibility,
		HealthCheckModel:          draft.HealthCheckModel,
		HealthCheckEndpointMode:   draft.HealthCheckEndpointMode,
		SupportedEndpointModes:    supportedEndpointModesFromCredentials(Credentials(draft.Credentials)),
		ModelMappings:             draft.ModelMappings,
	}
}

// savedAccountDraftTestSnapshot mirrors savedAccountDraftTestSnapshotAsync:
// the consistency gates, the provider/profile/group preparation chain and the
// stateTargetAccountId stamp.
func (s *Store) savedAccountDraftTestSnapshot(ctx context.Context, view *AccountForTestView, input *TestDraftAccountInput, access AccessScope) (*TestDraftSnapshot, error) {
	ctx = ensureCtx(ctx)
	account := view.item
	if account.AccessType == "authorized" {
		return nil, &ValidationError{Message: "授权账户测试不支持使用未保存表单配置"}
	}
	if input.ProviderCode != account.ProviderCode || input.Type != account.Type {
		return nil, &ValidationError{Message: "账户测试草稿与当前账户不一致"}
	}
	draft, err := s.prepareAccountDraftTestSnapshot(ctx, input, access, account.ID)
	if err != nil {
		return nil, err
	}
	if account.ProviderProtocolProfileID != "" && draft.ProviderProtocolProfileID != nil &&
		*draft.ProviderProtocolProfileID != account.ProviderProtocolProfileID {
		return nil, &ValidationError{Message: "账户测试草稿与当前账户协议档案不一致"}
	}
	stateTarget := account.ID
	draft.StateTargetAccountID = &stateTarget
	return draft, nil
}

type draftTestGroupReference struct {
	id             string
	ownerAccountID string
	providerCode   string
	name           string
}

// prepareAccountDraftTestSnapshot mirrors
// prepareAccountDraftTestSnapshotResolvedAsync (message copy verbatim).
func (s *Store) prepareAccountDraftTestSnapshot(ctx context.Context, input *TestDraftAccountInput, access AccessScope, draftAccountID string) (*TestDraftSnapshot, error) {
	// Group summary (findGroupSummary): the caller must see the group and its
	// provider must match the draft.
	group, err := s.findDraftTestGroup(ctx, access, input.GroupID)
	if err != nil {
		return nil, err
	}
	if group == nil || group.providerCode != input.ProviderCode {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	profileID := strings.TrimSpace(input.ProviderProtocolProfileID)
	if profileID == "" {
		return nil, &ValidationError{Message: "账户 providerProtocolProfileId 不能为空"}
	}
	profile, err := s.draftProviderProfile(ctx, input.ProviderCode, profileID, input.Type)
	if err != nil {
		return nil, err
	}
	ownerSystemAccountID := group.ownerAccountID
	if ownerSystemAccountID == "" {
		ownerSystemAccountID = access.FilterID
	}
	if ownerSystemAccountID == "" {
		ownerSystemAccountID = access.ViewerID
	}
	if ownerSystemAccountID == "" {
		return nil, &ValidationError{Message: "账户分组缺少归属用户，无法测试"}
	}

	profileRef := protocolProfileRef{
		ProviderCode:              input.ProviderCode,
		ProtocolCode:              profile.protocolCode,
		ProtocolVersion:           profile.protocolVersion,
		ProviderProtocolProfileID: profile.id,
	}
	clientCompatibility := "openai_standard"
	if !isAnthropicProtocolProfileOf(protocolPredicateInput{
		providerCode:              input.ProviderCode,
		protocolCode:              profile.protocolCode,
		protocolVersion:           profile.protocolVersion,
		providerProtocolProfileID: profile.id,
	}) {
		normalized, err := normalizeOpenAIAccountClientCompatibility(input.ProviderCode, input.Type, "", profileRef)
		if err != nil {
			return nil, err
		}
		clientCompatibility = normalized
	}
	credentialsInput := Credentials{}
	if input.Credentials != nil {
		credentialsInput = input.Credentials
	}
	if input.Type == "oauth" {
		if _, hasBaseURL := credentialsInput["base_url"]; !hasBaseURL || !credentialTextPresent(credentialsInput["base_url"]) {
			baseURL := profile.baseURL
			if baseURL == "" {
				baseURL = "https://api.openai.com/v1"
			}
			merged := Credentials{}
			for key, value := range credentialsInput {
				merged[key] = value
			}
			merged["base_url"] = baseURL
			credentialsInput = merged
		}
	}
	credentials, err := NormalizeAccountCredentialsForWrite(input.Type, credentialsInput, &EndpointModeDefaultContext{
		ProviderCode:              input.ProviderCode,
		AccountType:               input.Type,
		ClientCompatibility:       clientCompatibility,
		ProviderProtocolProfileID: profile.id,
		ProtocolCode:              profile.protocolCode,
		ProtocolVersion:           profile.protocolVersion,
	})
	if err != nil {
		return nil, err
	}

	var schedule any
	if input.AvailabilitySchedule != nil {
		normalized, err := NormalizeSchedule(input.AvailabilitySchedule)
		if err != nil {
			return nil, err
		}
		schedule = normalized
	} // Supported models: explicit list or the provider defaults.
	supportedModels := normalizeDraftTextList(input.SupportedModels)
	if len(supportedModels) == 0 {
		supportedModels = normalizeDraftTextList(profile.defaultSupportedModels)
	}
	if err := assertSupportedModelsRequired(supportedModels); err != nil {
		return nil, err
	}
	// requiredDraftHealthCheckModel.
	healthCheckModel := strings.TrimSpace(input.HealthCheckModel)
	if healthCheckModel == "" {
		return nil, &ValidationError{Message: "账户检查模型不能为空"}
	}
	if !containsString(supportedModels, healthCheckModel) {
		return nil, &ValidationError{Message: "账户检查模型必须属于账户支持模型"}
	}
	// resolveHealthCheckEndpointMode (images needs catalog evidence).
	enabledModes := supportedEndpointModesFromCredentials(credentials)
	endpointModeValue := input.HealthCheckEndpointMode
	var modelSupportsImages *bool
	if endpointModeValue == "images_json" {
		item, err := s.findTestCatalogItem(ctx, input.ProviderCode, ownerSystemAccountID, healthCheckModel)
		if err != nil {
			return nil, err
		}
		supported := item != nil && containsString(item.supportedAPIProtocols, "images")
		modelSupportsImages = &supported
	}
	healthCheckEndpointMode, err := resolveHealthCheckEndpointMode(&endpointModeValue, input.ProviderCode, profile.id, enabledModes, modelSupportsImages)
	if err != nil {
		return nil, err
	}

	// Model mappings (strict schema + upstream-allowed assert).
	mappings := []ModelMapping{}
	mappings = append(mappings, input.ModelMappings...)
	if err := assertMappingUpstreamsAllowed(mappings, supportedModels); err != nil {
		return nil, err
	}

	// Draft endpoint-mode compatibility asserts (assertDraftEndpointModesCompatible).
	profilePredicate := protocolPredicateInput{
		providerCode:              input.ProviderCode,
		protocolCode:              profile.protocolCode,
		protocolVersion:           profile.protocolVersion,
		providerProtocolProfileID: profile.id,
	}
	if err := assertEndpointModesCompatible(input.ProviderCode, input.Type, clientCompatibility, profilePredicate, enabledModes); err != nil {
		return nil, err
	}

	draftID := draftAccountID
	if draftID == "" {
		draftID = s.newI("acctdraft")
	}
	concurrencyLimit := 20
	if input.ConcurrencyLimit != nil {
		concurrencyLimit = *input.ConcurrencyLimit
	}
	priority := 0
	if input.Priority != nil {
		priority = *input.Priority
	}
	superPriority := false
	if input.SuperPriorityEnabled != nil {
		superPriority = *input.SuperPriorityEnabled
	}
	fallback := false
	if input.FallbackEnabled != nil {
		fallback = *input.FallbackEnabled
	}
	groupName := group.name
	providerProfileID := profile.id
	protocolCode := profile.protocolCode
	protocolVersion := profile.protocolVersion
	availabilityScheduleMap := map[string]any(nil)
	if typed, ok := schedule.(*AvailabilitySchedule); ok && typed != nil {
		availabilityScheduleMap = scheduleToMap(typed)
	}
	scheduleJSON := scheduleToJSONString(schedule)
	accountExpiresAt := input.AccountExpiresAt
	proxyProfileID := input.ProxyProfileID
	notes := input.Notes
	credentialsMap := map[string]any{}
	for key, value := range credentials {
		credentialsMap[key] = value
	}
	return &TestDraftSnapshot{
		ID:                        draftID,
		OwnerSystemAccountID:      ownerSystemAccountID,
		GroupID:                   input.GroupID,
		GroupName:                 &groupName,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: &providerProfileID,
		ProtocolCode:              &protocolCode,
		ProtocolVersion:           &protocolVersion,
		Name:                      input.Name,
		Type:                      input.Type,
		Credentials:               credentialsMap,
		ConcurrencyLimit:          concurrencyLimit,
		Priority:                  priority,
		SuperPriorityEnabled:      superPriority,
		FallbackEnabled:           fallback,
		ClientCompatibility:       clientCompatibility,
		SupportedModels:           supportedModels,
		HealthCheckModel:          healthCheckModel,
		HealthCheckEndpointMode:   healthCheckEndpointMode,
		ModelMappings:             mappings,
		ProxyProfileID:            proxyProfileID,
		AccountExpiresAt:          accountExpiresAt,
		AvailabilitySchedule:      availabilityScheduleMap,
		AvailabilityScheduleJSON:  scheduleJSON,
		Notes:                     notes,
	}, nil
}

// findDraftTestGroup mirrors the findGroupSummary visibility gate for the
// draft path (owner or admin; the authorized-group grant surface is owned by
// the groups slice and stays out of this port).
func (s *Store) findDraftTestGroup(ctx context.Context, access AccessScope, groupID string) (*draftTestGroupReference, error) {
	normalized := strings.TrimSpace(groupID)
	if normalized == "" {
		return nil, nil
	}
	var row struct {
		id              string
		systemAccountID string
		providerCode    string
		name            string
		enabled         int
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code, name, enabled
		FROM `+s.table("groups")+` WHERE id = ? LIMIT 1`), normalized).Scan(
		&row.id, &row.systemAccountID, &row.providerCode, &row.name, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.enabled != 1 {
		return nil, nil
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID {
		return nil, nil
	}
	return &draftTestGroupReference{
		id:             row.id,
		ownerAccountID: row.systemAccountID,
		providerCode:   row.providerCode,
		name:           row.name,
	}, nil
}

type draftTestProviderProfile struct {
	id                     string
	protocolCode           string
	protocolVersion        string
	baseURL                string
	defaultSupportedModels []string
	accountTypes           []string
}

// draftProviderProfile mirrors the provider/profile/type/enabled/
// gateway-protocol gate of the draft preparation chain (message copy
// verbatim).
func (s *Store) draftProviderProfile(ctx context.Context, providerCode, profileID, accountType string) (*draftTestProviderProfile, error) {
	var row struct {
		id               sql.NullString
		enabled          sql.NullInt64
		protocolCode     sql.NullString
		protocolVersion  sql.NullString
		baseURL          sql.NullString
		providerEnabled  sql.NullInt64
		accountTypesJSON sql.NullString
		defaultModels    sql.NullString
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT ppp.id, ppp.enabled, ppp.protocol_code, ppp.protocol_version,
			ppp.base_url, ppp.account_types_json, p.default_supported_models_json, p.enabled AS provider_enabled
		FROM `+s.table("providers")+` p
		LEFT JOIN `+s.table("provider_protocol_profiles")+` ppp
			ON ppp.provider_code = p.code AND ppp.id = ?
		WHERE p.code = ?
		LIMIT 1`), profileID, providerCode).Scan(
		&row.id, &row.enabled, &row.protocolCode, &row.protocolVersion,
		&row.baseURL, &row.accountTypesJSON, &row.defaultModels, &row.providerEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "供应商 " + providerCode + " 不支持账户类型 " + accountType}
	}
	if err != nil {
		return nil, err
	}
	profile := &draftTestProviderProfile{
		id:                     row.id.String,
		protocolCode:           row.protocolCode.String,
		protocolVersion:        row.protocolVersion.String,
		baseURL:                row.baseURL.String,
		defaultSupportedModels: parseStringJSONColumn(row.defaultModels),
		accountTypes:           parseStringJSONColumn(row.accountTypesJSON),
	}
	supportedType := false
	for _, candidate := range profile.accountTypes {
		if candidate == accountType {
			supportedType = true
			break
		}
	}
	if !row.id.Valid || row.id.String == "" || !supportedType {
		return nil, &ValidationError{Message: "供应商 " + providerCode + " 不支持账户类型 " + accountType}
	}
	if !row.providerEnabled.Valid || row.providerEnabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商已停用：" + providerCode}
	}
	predicate := protocolPredicateInput{
		providerCode:              providerCode,
		protocolCode:              profile.protocolCode,
		protocolVersion:           profile.protocolVersion,
		providerProtocolProfileID: profile.id,
	}
	if !isOpenAIProtocolProfileOf(predicate) && !isAnthropicProtocolProfileOf(predicate) && !isGeminiProtocolProfileOf(predicate) {
		return nil, &ValidationError{Message: "当前仅支持测试 OpenAI 或 Anthropic 协议账户"}
	}
	return profile, nil
}

func credentialTextPresent(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func normalizeDraftTextList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

func parseStringJSONColumn(value sql.NullString) []string {
	out := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return out
	}
	var raw []any
	if json.Unmarshal([]byte(value.String), &raw) != nil {
		return out
	}
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// scheduleToMap renders the normalized schedule as the draft JSON map.
func scheduleToMap(schedule *AvailabilitySchedule) map[string]any {
	if schedule == nil {
		return nil
	}
	raw, err := json.Marshal(schedule)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// scheduleToJSONString mirrors accountAvailabilityScheduleJson.
func scheduleToJSONString(schedule any) *string {
	typed, ok := schedule.(*AvailabilitySchedule)
	if !ok || typed == nil {
		return nil
	}
	raw, err := json.Marshal(typed)
	if err != nil {
		return nil
	}
	text := string(raw)
	return &text
}
