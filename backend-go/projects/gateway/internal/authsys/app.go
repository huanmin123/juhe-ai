package authsys

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// OperationLogChange mirrors OperationLogChange (operation-log-types.ts).
type OperationLogChange struct {
	Field     string `json:"field"`
	Label     string `json:"label"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// OperationLogEntry mirrors the subset of OperationLogRecordInput the auth
// slice emits. K4's producer sink consumes these.
type OperationLogEntry struct {
	ActorSystemAccountID          string
	ActorUsername                 string
	ActorDisplayName              string
	ActorRole                     string
	OperationScopeSystemAccountID string
	Mode                          string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	Summary                       string
	Changes                       []OperationLogChange
	Viewers                       []OperationLogViewer
}

// OperationLogSink receives operation log entries; K4 binds the F4 producer.
type OperationLogSink interface {
	Record(entry OperationLogEntry, r *http.Request)
}

type noopSink struct{}

func (noopSink) Record(OperationLogEntry, *http.Request) {}

func (d *Deps) recordOperationLog(r *http.Request, entry OperationLogEntry) {
	if entry.ActorSystemAccountID == "" {
		if auth := AuthContextFrom(r); auth != nil {
			entry.ActorSystemAccountID = auth.SystemAccountID
			entry.ActorUsername = auth.Username
			entry.ActorDisplayName = auth.DisplayName
			entry.ActorRole = auth.Role
		}
	}
	if d.Sink == nil {
		return
	}
	d.Sink.Record(entry, r)
}

// NewDeps wires the auth slice collaborators. Profile needs the canonical
// system-settings reader to preserve its Node response contract. The reader
// is appended as an optional argument to keep existing callers compatible.
func NewDeps(port businessauth.Port, accounts *AccountStore, captcha *modelcheckauth.CaptchaService, guard *modelcheckauth.LoginGuard, now func() time.Time, settings ...SystemSettingReader) *Deps {
	var systemSettings SystemSettingReader
	if len(settings) > 0 {
		systemSettings = settings[0]
	}
	return &Deps{
		Port: port, Accounts: accounts, Settings: systemSettings, Captcha: captcha, LoginGuard: guard, Now: now,
	}
}

// MountSystemAccounts registers the admin CRUD family
// (prefix /__aisys__/api/system-accounts) mirroring system-accounts.routes.ts.
func (d *Deps) MountSystemAccounts(k *kernel.Kernel, cookieSameSite string, cookieSecure bool) {
	_ = cookieSameSite
	_ = cookieSecure
	prefix := "/__aisys__/api/system-accounts"
	k.Register("GET "+prefix, d.RequireAdmin(http.HandlerFunc(d.listAccounts)))
	k.Register("GET "+prefix+"/options", d.RequireAdmin(http.HandlerFunc(d.listAccountOptions)))
	k.Register("POST "+prefix, d.RequireSuperAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "system_accounts.create",
			Fingerprint: func(r *http.Request) (any, error) {
				return map[string]any{
					"username":    kernel.TextField(kernel.BodyField(r, "username")),
					"displayName": kernel.TextField(kernel.BodyField(r, "displayName")),
				}, nil
			},
		})(http.HandlerFunc(d.createAccount)),
	))
	k.Register("PATCH "+prefix+"/{id}", d.RequireSuperAdmin(http.HandlerFunc(d.patchAccount)))
}

func (d *Deps) listAccounts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page := parseIntOrDefault(query.Get("page"), 1)
	pageSize := parseIntOrDefault(query.Get("pageSize"), 20)
	items, total, hasMore, err := d.Accounts.ListPage(r.Context(), query.Get("keyword"), page, pageSize)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": total, "hasMore": hasMore, "page": page, "pageSize": pageSize,
	}, "")
}

func (d *Deps) listAccountOptions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var ids []string
	if raw := query.Get("ids"); raw != "" {
		for _, piece := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(piece); trimmed != "" {
				ids = append(ids, trimmed)
			}
		}
	}
	limit := parseIntOrDefault(query.Get("limit"), 50)
	options, err := d.Accounts.ListOptions(r.Context(), ids, query.Get("keyword"), limit)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, options, "")
}

func (d *Deps) createAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username               *string            `json:"username"`
		DisplayName            *string            `json:"displayName"`
		Description            *string            `json:"description"`
		Password               *string            `json:"password"`
		Role                   *string            `json:"role"`
		Status                 *string            `json:"status"`
		MustChangePassword     *bool              `json:"mustChangePassword"`
		ImageGenerationEnabled *bool              `json:"imageGenerationEnabled"`
		AIAccountLimit         *int               `json:"aiAccountLimit"`
		RequestLimits          *UserRequestLimits `json:"requestLimits"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if body.Username == nil || body.DisplayName == nil || body.Password == nil ||
		len(*body.Username) < 2 || len(*body.DisplayName) < 1 || len(*body.Password) < 4 {
		kernel.WriteBadRequest(w, "系统账户参数无效")
		return
	}
	item, err := d.Accounts.Create(r.Context(), CreateInput{
		Username: *body.Username, DisplayName: *body.DisplayName, Description: body.Description,
		Password: *body.Password, Role: valueOr(body.Role, ""), Status: valueOr(body.Status, ""),
		MustChangePassword: body.MustChangePassword, ImageGenerationEnabled: body.ImageGenerationEnabled,
		AIAccountLimit: body.AIAccountLimit, RequestLimits: body.RequestLimits,
	})
	if err != nil {
		writeAccountError(w, err, "创建系统账户失败")
		return
	}
	auth := AuthContextFrom(r)
	d.recordOperationLog(r, OperationLogEntry{
		OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
		Module: "system_accounts", Action: "create", OperationKey: "system_accounts.create",
		ResourceType: "system_account", ResourceID: item.ID, ResourceName: item.DisplayName,
		Summary: "创建系统账户：" + item.DisplayName,
		Viewers: []OperationLogViewer{{SystemAccountID: item.ID, Reason: "admin_managed_my_resource"}},
	})
	w.WriteHeader(http.StatusCreated)
	kernel.WriteOK(w, accountListItemPayload(item), "")
}

func (d *Deps) patchAccount(w http.ResponseWriter, r *http.Request) {
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	id := r.PathValue("id")
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if _, exists := body["username"]; exists {
		kernel.WriteBadRequest(w, "用户账户创建后不能修改")
		return
	}
	input, err := parsePatchInput(body)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	before, err := d.Accounts.FindByID(r.Context(), id)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if before.ID == "" {
		kernel.WriteError(w, http.StatusNotFound, "系统账户不存在")
		return
	}
	result, err := d.Accounts.Patch(r.Context(), id, input)
	if err != nil {
		var conflict *ConflictError
		var validation *ValidationError
		if errors.As(err, &conflict) {
			kernel.WriteError(w, http.StatusConflict, conflict.Message)
			return
		}
		if errors.As(err, &validation) {
			kernel.WriteError(w, http.StatusConflict, validation.Message)
			return
		}
		kernel.WriteError(w, http.StatusConflict, "更新系统账户失败")
		return
	}
	action, operationKey, resourceName := "update", "system_accounts.update", before.DisplayName
	if input.Password != nil {
		action, operationKey = "reset_password", "system_accounts.reset_password"
	}
	d.recordOperationLog(r, OperationLogEntry{
		OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
		Module: "system_accounts", Action: action, OperationKey: operationKey,
		ResourceType: "system_account", ResourceID: id, ResourceName: resourceName,
		Summary: summaryFor(action, resourceName),
		Changes: buildChanges(before, input, result),
	})
	kernel.WriteOK(w, result, "")
}

func writeAccountError(w http.ResponseWriter, err error, fallback string) {
	var conflict *ConflictError
	var validation *ValidationError
	switch {
	case errors.As(err, &conflict):
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
	case errors.As(err, &validation):
		kernel.WriteError(w, http.StatusConflict, validation.Message)
	default:
		kernel.WriteError(w, http.StatusInternalServerError, fallback)
	}
}

func parseIntOrDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func accountListItemPayload(item AccountListItem) AccountListItem {
	return item
}

type OperationLogViewer struct {
	SystemAccountID string `json:"systemAccountId"`
	Reason          string `json:"reason"`
}

func summaryFor(action, resourceName string) string {
	if action == "reset_password" {
		return "重置系统账户密码：" + resourceName
	}
	return "更新系统账户：" + resourceName
}

var changeLabels = map[string]string{
	"displayName": "用户名称", "description": "说明", "password": "登录密码", "role": "角色",
	"status": "状态", "mustChangePassword": "下次登录改密", "imageGenerationEnabled": "支持图像生成",
	"aiAccountLimit": "AI 账户数量限制", "requestLimits": "用户限制",
}

func buildChanges(before AccountSummary, input PatchInput, result AccountMutationResult) []OperationLogChange {
	changes := []OperationLogChange{}
	appendChange := func(field string, beforeValue, afterValue string) {
		changes = append(changes, OperationLogChange{Field: field, Label: changeLabels[field], Before: beforeValue, After: afterValue})
	}
	if result.DisplayName != nil {
		appendChange("displayName", before.DisplayName, *result.DisplayName)
	}
	if result.Description != nil {
		beforeDescription := ""
		if before.Description != nil {
			beforeDescription = *before.Description
		}
		appendChange("description", beforeDescription, *result.Description)
	}
	if result.Role != nil {
		appendChange("role", before.Role, *result.Role)
	}
	if result.Status != nil {
		appendChange("status", before.Status, *result.Status)
	}
	if result.MustChangePassword != nil {
		appendChange("mustChangePassword", boolText(before.MustChangePassword), boolText(*result.MustChangePassword))
	}
	if result.ImageGenerationEnabled != nil {
		appendChange("imageGenerationEnabled", boolText(before.ImageGenerationEnabled), boolText(*result.ImageGenerationEnabled))
	}
	if result.AIAccountLimit != nil {
		appendChange("aiAccountLimit", intPtrText(before.AIAccountLimit), strconv.Itoa(*result.AIAccountLimit))
	}
	if result.RequestLimits != nil {
		appendChange("requestLimits", "已设置", "已设置")
	}
	if input.Password != nil {
		changes = append(changes, OperationLogChange{Field: "password", Label: "登录密码", Before: "已设置", After: "已变更", Sensitive: true})
	}
	return changes
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func intPtrText(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func parsePatchInput(body map[string]any) (PatchInput, error) {
	input := PatchInput{}
	expected, ok := body["expectedUpdatedAt"].(string)
	if !ok || expected == "" {
		return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
	}
	normalized, err := normalizeRFC3339(expected)
	if err != nil {
		return PatchInput{}, err
	}
	input.ExpectedUpdatedAt = normalized
	mutating := 0
	if value, exists := body["displayName"]; exists {
		text, isString := value.(string)
		if !isString || text == "" {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.DisplayName = &text
		mutating++
	}
	if value, exists := body["description"]; exists {
		if value == nil {
			empty := ""
			input.Description = &empty
		} else if text, isString := value.(string); isString {
			input.Description = &text
		} else {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		mutating++
	}
	if value, exists := body["password"]; exists {
		text, isString := value.(string)
		if !isString {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.Password = &text
		mutating++
	}
	if value, exists := body["role"]; exists {
		text, isString := value.(string)
		if !isString {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.Role = &text
		mutating++
	}
	if value, exists := body["status"]; exists {
		text, isString := value.(string)
		if !isString {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.Status = &text
		mutating++
	}
	if value, exists := body["mustChangePassword"]; exists {
		flag, isBool := value.(bool)
		if !isBool {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.MustChangePassword = &flag
		mutating++
	}
	if value, exists := body["imageGenerationEnabled"]; exists {
		flag, isBool := value.(bool)
		if !isBool {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.ImageGenerationEnabled = &flag
		mutating++
	}
	if value, exists := body["aiAccountLimit"]; exists {
		if value == nil {
			zero := 0
			input.AIAccountLimit = &zero
		} else if number, isFloat := value.(float64); isFloat && number == float64(int(number)) {
			limit := int(number)
			input.AIAccountLimit = &limit
		} else {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		mutating++
	}
	if value, exists := body["requestLimits"]; exists {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		var limits UserRequestLimits
		if unmarshalErr := json.Unmarshal(encoded, &limits); unmarshalErr != nil {
			return PatchInput{}, &ValidationError{Message: "系统账户参数无效"}
		}
		input.RequestLimits = &limits
		mutating++
	}
	if mutating == 0 {
		return PatchInput{}, &ValidationError{Message: "至少提交一个修改字段"}
	}
	return input, nil
}
