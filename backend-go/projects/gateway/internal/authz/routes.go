package authz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

const authorizationsProcessingTTL = 120 * time.Second

// Deps bundles the authz slice collaborators.
type Deps struct {
	Store *Store
	Sink  authsys.OperationLogSink
	Auth  *authsys.Deps
}

func (d *Deps) RequireAdmin(next http.Handler) http.Handler { return d.Auth.RequireAdmin(next) }

func (d *Deps) RequireSession(touch bool) func(http.Handler) http.Handler {
	return d.Auth.RequireSession(touch)
}

// createFingerprint mirrors the mutation-guard fingerprint of
// authorizations.routes.ts:221-231; the owner component comes from the
// ?systemAccountId scope query, which the my-* surface pins to the empty
// string because forceSelfAccessScope deletes the parameter before the router
// runs.
func createFingerprint(r *http.Request, owner string) (any, error) {
	return map[string]any{
		"owner":                  owner,
		"resourceType":           kernel.TextField(kernel.BodyField(r, "resourceType")),
		"resourceId":             kernel.TextField(kernel.BodyField(r, "resourceId")),
		"granteeType":            kernel.TextField(kernel.BodyField(r, "granteeType")),
		"granteeUsername":        kernel.TextField(kernel.BodyField(r, "granteeUsername")),
		"granteeSystemAccountId": kernel.TextField(kernel.BodyField(r, "granteeSystemAccountId")),
		"teamName":               kernel.TextField(kernel.BodyField(r, "teamName")),
		"teamId":                 kernel.TextField(kernel.BodyField(r, "teamId")),
		"targetGroupId":          kernel.TextField(kernel.BodyField(r, "targetGroupId")),
		"remark":                 kernel.TextField(kernel.BodyField(r, "remark")),
		"expiresAt":              kernel.TextField(kernel.BodyField(r, "expiresAt")),
		"limits":                 kernel.BodyField(r, "limits"),
	}, nil
}

// writeScopeQueryBadRequest mirrors parseRequestScopeQuery
// (request-scope-query.ts): ?systemAccountId is optional, but a
// present-and-blank value fails min(1, '系统账号 ID 不能为空') and renders as
// 400 '查询参数不合法' (D-216 present-but-empty semantics). Every write route
// in authorizations.routes.ts runs this validation before its body schema.
func writeScopeQueryBadRequest(w http.ResponseWriter, r *http.Request) bool {
	parser := newQueryParser(r)
	parser.text("systemAccountId", "系统账号 ID 不能为空")
	return parser.writeBadRequest(w, "查询参数不合法")
}

// normalizeMutationVersion mirrors rfc3339InstantSchema('授权配置版本格式不正确')
// (authorizations.routes.ts:33-35, :123-124, :136): the optimistic-lock version
// must be an absolute RFC3339 instant and is canonicalized to UTC milliseconds
// before the store-level comparison.
func normalizeMutationVersion(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", false
	}
	canonical := canonicalizeAuthorizationInstant(text)
	if canonical == "" {
		return "", false
	}
	return canonical, true
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func rawJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// parseStatusInput mirrors the update schema status enum
// (authorizations.routes.ts:125): only 'active'|'paused'; JSON null and other
// values are rejected by the contract layer.
func parseStatusInput(raw json.RawMessage) (*string, bool) {
	if !rawJSONPresent(raw) {
		return nil, rawJSONNull(raw)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value != StatusActive && value != StatusPaused {
		return nil, false
	}
	return &value, true
}

// parseExpiresAtInput mirrors the update schema expiresAt union
// (authorizations.routes.ts:126-129): absent keeps the stored value, JSON null
// clears it, and strings must be canonicalizable RFC3339 instants
// ('过期时间格式不正确').
func parseExpiresAtInput(raw json.RawMessage) (*string, bool, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, true
	}
	if rawJSONNull(raw) {
		return nil, true, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, false
	}
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, false, false
	}
	canonical := canonicalizeAuthorizationInstant(text)
	if canonical == "" {
		return nil, false, false
	}
	return &canonical, true, true
}

// accessFor resolves the request access scope: admin with an explicit
// ?systemAccountId= filter narrows; my-* requests force self.
type accessInfo struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

func (d *Deps) accessFor(r *http.Request, selfOnly bool) accessInfo {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return accessInfo{}
	}
	if !selfOnly && authsys.IsAdminRole(auth.Role) {
		filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		if filter == "all" {
			filter = ""
		}
		return accessInfo{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
	}
	return accessInfo{ViewerID: auth.SystemAccountID}
}

// Mount wires the authorizations route family onto both prefixes.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/my-authorizations", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, true)
	})))
	k.Register("GET "+prefix+"/my-authorizations/{id}", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, true)
	})))
	k.Register("DELETE "+prefix+"/my-authorizations/{id}/return", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.returnValue(w, r, true)
	})))
	// D-215: the user-side write surface. Node mounts the same router under
	// `/my-authorizations` + forceSelfAccessScope (scope-boundary regression
	// mount :68), so POST create / PATCH update / PATCH expire / DELETE revoke
	// all serve the my-* prefix with the viewer pinned as owner scope.
	k.Register("POST "+prefix+"/my-authorizations", d.RequireSelf(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "authorizations.create",
			// Node succeededTtlMs: 0 (authorizations.routes.ts:219) = success is
			// immediately retryable; failures keep the default 10s window.
			SucceededTTL:  kernel.DedupNoRetention,
			FailedTTL:     0,
			ProcessingTTL: authorizationsProcessingTTL,
			// forceSelfAccessScope deletes ?systemAccountId before the router
			// runs, so the dedupe scope and owner fingerprint stay empty on the
			// my-* surface no matter what the client sent.
			Scope:       func(*http.Request) (any, error) { return "", nil },
			Fingerprint: func(r *http.Request) (any, error) { return createFingerprint(r, "") },
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.create(w, r)
		})),
	))
	k.Register("PATCH "+prefix+"/my-authorizations/{id}", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patchSelf(w, r, false)
	})))
	k.Register("PATCH "+prefix+"/my-authorizations/{id}/expire", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patchSelf(w, r, true)
	})))
	k.Register("DELETE "+prefix+"/my-authorizations/{id}", d.RequireSelf(http.HandlerFunc(d.revokeSelf)))

	k.Register("GET "+prefix+"/authorizations", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, false)
	})))
	k.Register("GET "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, false)
	})))
	k.Register("POST "+prefix+"/authorizations", d.RequireAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "authorizations.create",
			// Node succeededTtlMs: 0 (authorizations.routes.ts:219) = success is
			// immediately retryable; failures keep the default 10s window.
			SucceededTTL:  kernel.DedupNoRetention,
			FailedTTL:     0,
			ProcessingTTL: authorizationsProcessingTTL,
			Scope: func(r *http.Request) (any, error) {
				return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
			},
			Fingerprint: func(r *http.Request) (any, error) {
				return createFingerprint(r, strings.TrimSpace(r.URL.Query().Get("systemAccountId")))
			},
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.create(w, r)
		})),
	))
	k.Register("DELETE "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.revoke(w, r)
	})))
	k.Register("PATCH "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, false)
	})))
	k.Register("PATCH "+prefix+"/authorizations/{id}/expire", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, true)
	})))

	// Recipient-consent governance surface (design §6).
	d.mountGovernanceRoutes(k, prefix)

	// Usage read family: 8 aggregate endpoints + the per-id usage detail and
	// the admin return surface (usage_routes.go).
	d.mountUsageRoutes(k, prefix+"/authorizations", prefix+"/my-authorizations")
}

// RequireAdminAuthz combines session + admin role (authsys.RequireAdmin).
func (d *Deps) RequireAdminAuthz(next http.Handler) http.Handler {
	return d.RequireAdmin(next)
}

// RequireSelf mirrors forceSelfAccessScope + session.
func (d *Deps) RequireSelf(next http.Handler) http.Handler {
	return d.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// my-* semantics: drop any systemAccountId query (self enforced).
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if !selfOnly {
		// The admin list is the full business-record view (design §6.4): the
		// legacy ?systemAccountId / ?direction scoping belongs to the retired
		// single-grant semantics and must not narrow the governance inventory.
		items, err := d.Store.ListScopedOffers(r.Context(), "",
			"", strings.TrimSpace(r.URL.Query().Get("status")))
		if err != nil {
			writeGovernanceError(r, w, err)
			return
		}
		d.writeData(w, map[string]any{"items": items})
		return
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction == "" || direction == "inbound" {
		items, err := d.Store.ListInboundSources(r.Context(), auth.SystemAccountID)
		if err != nil {
			writeGovernanceError(r, w, err)
			return
		}
		d.writeData(w, map[string]any{"items": items, "direction": "inbound"})
		return
	}
	var items any
	var err error
	switch direction {
	case "outbound":
		items, err = d.Store.ListOutboundOffers(r.Context(), auth.SystemAccountID)
	case "owned":
		items, err = d.Store.ListOwnedOffers(r.Context(), auth.SystemAccountID)
	case "owner_pending":
		items, err = d.Store.ListOwnerPendingOffers(r.Context(), auth.SystemAccountID)
	default:
		kernel.WriteBadRequest(w, "direction 参数无效")
		return
	}
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	d.writeData(w, map[string]any{"items": items, "direction": direction})
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	// authorizations.routes.ts:286-291: the detail route validates the scope
	// query before the params schema.
	if !writeScopeQueryBadRequest(w, r) {
		return
	}
	access := d.accessFor(r, selfOnly)
	summary, err := d.Store.Find(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if summary == nil {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	if selfOnly && !d.visibleTo(summary, access.ViewerID) {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	// Admin scope parity with the {id}/usage read (findScopedUsageGrant): when
	// ?systemAccountId selects a scope account, the grant must belong to that
	// owner; anything else reads as not found.
	if !selfOnly && access.FilterID != "" && summary.OwnerID != access.FilterID {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	kernel.WriteOK(w, summary, "")
}

func (d *Deps) visibleTo(summary *Summary, viewerID string) bool {
	if summary.OwnerID == viewerID {
		return true
	}
	if summary.GranteeUserID != nil && *summary.GranteeUserID == viewerID {
		return true
	}
	return false
}

func (d *Deps) create(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if !writeScopeQueryBadRequest(w, r) {
		return
	}
	selfOnly := strings.HasSuffix(r.URL.Path, "/my-authorizations")
	scopeAccount := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	isAdmin := !selfOnly && authsys.IsAdminRole(auth.Role)
	if isAdmin && scopeAccount == "" {
		kernel.WriteBadRequest(w, "管理员新增授权时必须指定授权人")
		return
	}
	ownerScope := scopeAccount
	if selfOnly {
		ownerScope = auth.SystemAccountID
	}
	var body struct {
		ResourceType           *string         `json:"resourceType"`
		ResourceID             *string         `json:"resourceId"`
		GranteeType            *string         `json:"granteeType"`
		GranteeUsername        *string         `json:"granteeUsername"`
		GranteeSystemAccountID *string         `json:"granteeSystemAccountId"`
		TeamName               *string         `json:"teamName"`
		TeamID                 *string         `json:"teamId"`
		TargetGroupID          *string         `json:"targetGroupId"`
		Remark                 *string         `json:"remark"`
		ExpiresAt              *string         `json:"expiresAt"`
		Limits                 json.RawMessage `json:"limits"`
	}
	if !decodeStrictJSON(w, r, &body, map[string]bool{
		"resourceType": true, "resourceId": true, "granteeType": true,
		"granteeUsername": true, "granteeSystemAccountId": true,
		"teamName": true, "teamId": true, "targetGroupId": true,
		"remark": true, "expiresAt": true, "limits": true,
	}) {
		return
	}
	if body.TargetGroupID != nil && strings.TrimSpace(*body.TargetGroupID) != "" {
		kernel.WriteBadRequest(w, "授权不再支持 targetGroupId，请由接收方接受后自行绑定")
		return
	}
	if body.ResourceType == nil || body.ResourceID == nil || body.GranteeType == nil ||
		strings.TrimSpace(*body.ResourceID) == "" {
		kernel.WriteBadRequest(w, "授权参数不合法")
		return
	}
	resourceType := strings.TrimSpace(*body.ResourceType)
	granteeType := strings.TrimSpace(*body.GranteeType)
	if resourceType != "account" && resourceType != "group" {
		kernel.WriteBadRequest(w, "授权参数不合法")
		return
	}
	if granteeType != "system_account" && granteeType != "team" {
		kernel.WriteBadRequest(w, "授权参数不合法")
		return
	}
	if !isAdmin {
		if body.GranteeSystemAccountID != nil && strings.TrimSpace(*body.GranteeSystemAccountID) != "" {
			kernel.WriteBadRequest(w, "普通用户请使用精确用户名发起授权")
			return
		}
		if body.TeamID != nil && strings.TrimSpace(*body.TeamID) != "" {
			kernel.WriteBadRequest(w, "普通用户请使用精确团队名发起授权")
			return
		}
	}
	input := OfferInput{
		ResourceType:           resourceType,
		ResourceID:             strings.TrimSpace(*body.ResourceID),
		GranteeType:            granteeType,
		GranteeUsername:        strings.TrimSpace(stringOrEmpty(body.GranteeUsername)),
		GranteeSystemAccountID: strings.TrimSpace(stringOrEmpty(body.GranteeSystemAccountID)),
		TeamName:               strings.TrimSpace(stringOrEmpty(body.TeamName)),
		TeamID:                 strings.TrimSpace(stringOrEmpty(body.TeamID)),
		Remark:                 body.Remark,
		ExpiresAt:              body.ExpiresAt,
	}
	if rawJSONPresent(body.Limits) && !rawJSONNull(body.Limits) {
		text := string(bytes.TrimSpace(body.Limits))
		input.LimitsJSON = &text
	}
	result, err := d.Store.CreateOffer(r.Context(), input, auth.SystemAccountID, isAdmin, ownerScope)
	if err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if result.Created {
		if d.Sink != nil {
			mode := "admin"
			if selfOnly {
				mode = "self"
			}
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: ownerScope,
				Mode:                          mode,
				Module:                        "authorizations",
				Action:                        "create_offer",
				OperationKey:                  "authorizations.create",
				ResourceType:                  "authorization",
				ResourceID:                    result.GrantID,
				Summary:                       "发起资源授权：" + input.ResourceID,
				Changes: []authsys.OperationLogChange{
					{Field: "resourceType", Label: "资源类型", After: input.ResourceType},
					{Field: "resourceId", Label: "授权资源", After: input.ResourceID},
					{Field: "granteeType", Label: "接收方类型", After: input.GranteeType},
					{Field: "status", Label: "状态", After: result.Status},
				},
			}, r)
		}
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	kernel.WriteJSON(w, status, map[string]any{"data": result})
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// revoke keeps the unscoped administrator surface (authorizations route
// family).
func (d *Deps) revoke(w http.ResponseWriter, r *http.Request) {
	d.revokeScoped(w, r, false)
}

// revokeSelf serves DELETE /my-authorizations/{id}: the viewer is pinned as
// the owner scope, so a grantee cannot revoke an inbound grant (scope
// boundary regression :747).
func (d *Deps) revokeSelf(w http.ResponseWriter, r *http.Request) {
	d.revokeScoped(w, r, true)
}

func (d *Deps) revokeScoped(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	// Present-but-empty ?systemAccountId is invalid on every write surface,
	// including revoke (the create/patch/find handlers already enforce this).
	if !writeScopeQueryBadRequest(w, r) {
		return
	}
	isAdmin := !selfOnly && authsys.IsAdminRole(auth.Role)
	if err := d.Store.RevokeOffer(r.Context(), r.PathValue("id"), auth.SystemAccountID, isAdmin, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if d.Sink != nil {
		mode := "admin"
		if selfOnly {
			mode = "self"
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID:          auth.SystemAccountID,
			ActorUsername:                 auth.Username,
			ActorDisplayName:              auth.DisplayName,
			ActorRole:                     auth.Role,
			OperationScopeSystemAccountID: auth.SystemAccountID,
			Mode:                          mode,
			Module:                        "authorizations",
			Action:                        "revoke",
			OperationKey:                  "authorizations.revoke",
			ResourceType:                  "authorization",
			ResourceID:                    r.PathValue("id"),
			Summary:                       "回收资源授权",
		}, r)
	}
	d.writeData(w, map[string]any{"status": StatusRevoked})
}

func (d *Deps) returnValue(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	_ = selfOnly
	kernel.WriteJSON(w, http.StatusConflict, map[string]any{
		"code":    "state_conflict",
		"message": "请前往授权中心按来源归还；团队授权不能归还整个业务记录",
	})
}

func (d *Deps) patchSelf(w http.ResponseWriter, r *http.Request, expireOnly bool) {
	d.patchScoped(w, r, expireOnly, true)
}

func (d *Deps) patch(w http.ResponseWriter, r *http.Request, expireOnly bool) {
	d.patchScoped(w, r, expireOnly, false)
}

func (d *Deps) patchScoped(w http.ResponseWriter, r *http.Request, expireOnly, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if !writeScopeQueryBadRequest(w, r) {
		return
	}
	_ = selfOnly
	var body struct {
		ExpectedUpdatedAt string          `json:"expectedUpdatedAt"`
		Remark            *string         `json:"remark"`
		ExpiresAt         json.RawMessage `json:"expiresAt"`
		Limits            json.RawMessage `json:"limits"`
	}
	allowed := map[string]bool{"expectedUpdatedAt": true, "remark": true, "expiresAt": true, "limits": true}
	if expireOnly {
		allowed = map[string]bool{"expectedUpdatedAt": true, "expiresAt": true}
	}
	if !decodeStrictJSON(w, r, &body, allowed) {
		return
	}
	hasExpiresAt := len(bytes.TrimSpace(body.ExpiresAt)) > 0
	hasLimits := len(bytes.TrimSpace(body.Limits)) > 0
	if !hasExpiresAt && !hasLimits && body.Remark == nil {
		kernel.WriteBadRequest(w, "请提供要修改的授权内容")
		return
	}
	input := UpdateOfferInput{Remark: body.Remark, ExpiresAtSet: hasExpiresAt}
	if hasExpiresAt {
		expiresAt, expiresSet, ok := parseExpiresAtInput(body.ExpiresAt)
		if !ok {
			kernel.WriteBadRequest(w, "过期时间格式不正确")
			return
		}
		input.ExpiresAt = expiresAt
		input.ExpiresAtSet = expiresSet
	}
	if hasLimits {
		input.LimitsSet = true
		if !rawJSONNull(body.Limits) {
			text := string(bytes.TrimSpace(body.Limits))
			input.LimitsJSON = &text
		}
	}
	if err := d.Store.UpdateOffer(r.Context(), auth.SystemAccountID, r.PathValue("id"), input, nowISO(d.Store)); err != nil {
		writeGovernanceError(r, w, err)
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			OperationScopeSystemAccountID: auth.SystemAccountID,
			Mode:                          "self", Module: "authorizations", Action: "update",
			OperationKey: "authorizations.update", ResourceType: "authorization",
			ResourceID: r.PathValue("id"), Summary: "更新授权额度与备注",
		}, r)
	}
	d.writeData(w, map[string]any{"updated": true})
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func orText(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

func errorsAsFail(err error, target **Fail) bool {
	if fail, ok := err.(*Fail); ok {
		*target = fail
		return true
	}
	return false
}

// writeFail 保留既有纯消息 400；带 Code 的领域错误按 {code,message} 渲染
// （超级分组绑定守卫）。两类响应都是 400，前端可继续按 message 兜底。
func writeFail(w http.ResponseWriter, fail *Fail) {
	if fail.Code != "" {
		kernel.WriteJSON(w, http.StatusBadRequest, map[string]any{"code": fail.Code, "message": fail.Message})
		return
	}
	kernel.WriteBadRequest(w, fail.Message)
}

func errorsAsConflict(err error, target **Conflict) bool {
	if conflict, ok := err.(*Conflict); ok {
		*target = conflict
		return true
	}
	return false
}

// MountAuthz is the canonical mount entry used by gateway wiring.
func MountAuthz(k *kernel.Kernel, deps *Deps) { deps.Mount(k) }
