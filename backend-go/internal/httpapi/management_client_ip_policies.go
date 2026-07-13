package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientippolicies"
	"juhe-ai/backend-go/internal/store/port"
)

const managementClientIPPolicyMaxBodyBytes = 256 << 10

const (
	managementClientIPPolicyActionAllowlist   = "allowlist"
	managementClientIPPolicyActionUnallowlist = "unallowlist"
)

type managementClientIPPolicyHTTPService interface {
	Allowlist(
		r *http.Request,
		input managementclientippolicies.AllowlistInput,
	) (managementclientippolicies.PolicySummary, error)
	Unallowlist(
		r *http.Request,
		input managementclientippolicies.UnallowlistInput,
	) (managementclientippolicies.UnallowlistResult, error)
}

type managementClientIPPolicyServiceAdapter struct {
	service *managementclientippolicies.Service
}

func (a managementClientIPPolicyServiceAdapter) Allowlist(
	r *http.Request,
	input managementclientippolicies.AllowlistInput,
) (managementclientippolicies.PolicySummary, error) {
	return a.service.Allowlist(r.Context(), input)
}

func (a managementClientIPPolicyServiceAdapter) Unallowlist(
	r *http.Request,
	input managementclientippolicies.UnallowlistInput,
) (managementclientippolicies.UnallowlistResult, error) {
	return a.service.Unallowlist(r.Context(), input)
}

func NewManagementClientIPAllowlistHandlerWithOperationLog(
	service *managementclientippolicies.Service,
	options ManagementOperationLogOptions,
) http.Handler {
	return newManagementClientIPPolicyHandler(
		managementClientIPPolicyServiceAdapter{service: service},
		managementClientIPPolicyActionAllowlist,
		newManagementOperationLogOptions(options),
	)
}

func NewManagementClientIPUnallowlistHandlerWithOperationLog(
	service *managementclientippolicies.Service,
	options ManagementOperationLogOptions,
) http.Handler {
	return newManagementClientIPPolicyHandler(
		managementClientIPPolicyServiceAdapter{service: service},
		managementClientIPPolicyActionUnallowlist,
		newManagementOperationLogOptions(options),
	)
}

func newManagementClientIPPolicyHandler(
	service managementClientIPPolicyHTTPService,
	action string,
	operationLogOptions managementOperationLogOptions,
) http.Handler {
	if action != managementClientIPPolicyActionAllowlist &&
		action != managementClientIPPolicyActionUnallowlist {
		panic("unsupported management client IP policy action")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ipHash := strings.TrimFunc(chi.URLParam(r, "ipHash"), managementGroupListECMAScriptWhitespace)
		if !validManagementClientIPHash(ipHash) {
			writeMessageError(w, http.StatusBadRequest, "IP 标识无效")
			return
		}
		reason, ok := decodeManagementClientIPPolicyBody(w, r)
		if !ok {
			return
		}
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		switch action {
		case managementClientIPPolicyActionAllowlist:
			result, err := service.Allowlist(r, managementclientippolicies.AllowlistInput{
				IPHash:               ipHash,
				ActorSystemAccountID: authContext.SystemAccountID,
				Reason:               reason,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				reason,
				action,
				result,
				managementclientippolicies.UnallowlistResult{},
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		case managementClientIPPolicyActionUnallowlist:
			result, err := service.Unallowlist(r, managementclientippolicies.UnallowlistInput{
				IPHash:               ipHash,
				ActorSystemAccountID: authContext.SystemAccountID,
				Reason:               reason,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				reason,
				action,
				managementclientippolicies.PolicySummary{},
				result,
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		}
	})
}

func decodeManagementClientIPPolicyBody(
	w http.ResponseWriter,
	r *http.Request,
) (*string, bool) {
	limited := http.MaxBytesReader(w, r.Body, managementClientIPPolicyMaxBodyBytes)
	body, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
		}
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return nil, false
	}
	for field := range payload {
		if field != "reason" {
			writeMessageError(w, http.StatusBadRequest, "IP 策略参数包含未知字段")
			return nil, false
		}
	}
	rawReason, exists := payload["reason"]
	if !exists {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(rawReason), []byte("null")) {
		writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
		return nil, false
	}
	var reason string
	if err := json.Unmarshal(rawReason, &reason); err != nil {
		writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
		return nil, false
	}
	reason = strings.TrimFunc(reason, managementGroupListECMAScriptWhitespace)
	if managementClientIPPolicyUTF16Length(reason) > 500 {
		writeMessageError(w, http.StatusBadRequest, "原因不能超过 500 个字符")
		return nil, false
	}
	return &reason, true
}

func validManagementClientIPHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func managementClientIPPolicyUTF16Length(value string) int {
	length := 0
	for _, character := range value {
		if character > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}

func writeManagementClientIPPolicyServiceError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "IP 策略保存失败"
	}
	writeMessageError(w, http.StatusBadRequest, message)
	return false
}

func recordManagementClientIPPolicyOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	ipHash string,
	reason *string,
	action string,
	allowlistResult managementclientippolicies.PolicySummary,
	unallowlistResult managementclientippolicies.UnallowlistResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.newLogID
	if newLogID == nil {
		newLogID = defaultManagementOperationLogID
	}
	statusCode := http.StatusOK
	resourceName := ipHash[:12]
	input := port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "client_ip_stats",
		Action:               action,
		OperationKey:         "client_ip_stats." + action,
		ResourceType:         "client_ip",
		ResourceID:           ipHash,
		ResourceName:         resourceName,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Method:               r.Method,
		Path:                 r.URL.Path,
		StatusCode:           &statusCode,
		ClientIP:             opts.clientIP.FromRequest(r),
		UserAgent:            r.UserAgent(),
		CreatedAt:            now().UTC(),
	}
	if action == managementClientIPPolicyActionAllowlist {
		input.Summary = "加入 IP 白名单：" + resourceName
		input.Changes = []port.OperationLogChange{
			{Field: "reason", Label: "原因", After: managementClientIPPolicyReasonValue(reason)},
			{Field: "policyType", Label: "策略类型", After: "allowlist"},
			{Field: "duration", Label: "白名单时长", After: "永久"},
			{Field: "expiresAt", Label: "过期时间", After: nil},
		}
		input.Metadata = map[string]any{
			"ipHash":        ipHash,
			"policyId":      allowlistResult.ID,
			"policyType":    "allowlist",
			"durationLabel": "永久",
		}
	} else {
		input.Summary = "移出 IP 白名单：" + resourceName
		input.Changes = []port.OperationLogChange{
			{Field: "disabledCount", Label: "停用策略数", After: unallowlistResult.DisabledCount},
			{Field: "policyType", Label: "策略类型", Before: "allowlist", After: nil},
			{Field: "reason", Label: "原因", After: managementClientIPPolicyReasonValue(reason)},
		}
		input.Metadata = map[string]any{
			"ipHash":        ipHash,
			"policyType":    "allowlist",
			"disabledCount": unallowlistResult.DisabledCount,
		}
	}
	if reason != nil {
		input.Metadata["reason"] = *reason
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementClientIPPolicyReasonValue(reason *string) any {
	if reason == nil {
		return nil
	}
	return *reason
}
