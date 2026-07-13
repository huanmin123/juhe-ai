package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
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
	managementClientIPPolicyActionBlacklist   = "blacklist"
	managementClientIPPolicyActionUnblock     = "unblock"
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
	Blacklist(
		r *http.Request,
		input managementclientippolicies.BlacklistInput,
	) (managementclientippolicies.PolicySummary, error)
	Unblock(
		r *http.Request,
		input managementclientippolicies.UnblockInput,
	) (managementclientippolicies.UnblockResult, error)
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

func (a managementClientIPPolicyServiceAdapter) Blacklist(
	r *http.Request,
	input managementclientippolicies.BlacklistInput,
) (managementclientippolicies.PolicySummary, error) {
	return a.service.Blacklist(r.Context(), input)
}

func (a managementClientIPPolicyServiceAdapter) Unblock(
	r *http.Request,
	input managementclientippolicies.UnblockInput,
) (managementclientippolicies.UnblockResult, error) {
	return a.service.Unblock(r.Context(), input)
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

func NewManagementClientIPBlacklistHandlerWithOperationLog(
	service *managementclientippolicies.Service,
	options ManagementOperationLogOptions,
) http.Handler {
	return newManagementClientIPPolicyHandler(
		managementClientIPPolicyServiceAdapter{service: service},
		managementClientIPPolicyActionBlacklist,
		newManagementOperationLogOptions(options),
	)
}

func NewManagementClientIPUnblockHandlerWithOperationLog(
	service *managementclientippolicies.Service,
	options ManagementOperationLogOptions,
) http.Handler {
	return newManagementClientIPPolicyHandler(
		managementClientIPPolicyServiceAdapter{service: service},
		managementClientIPPolicyActionUnblock,
		newManagementOperationLogOptions(options),
	)
}

func newManagementClientIPPolicyHandler(
	service managementClientIPPolicyHTTPService,
	action string,
	operationLogOptions managementOperationLogOptions,
) http.Handler {
	if action != managementClientIPPolicyActionAllowlist &&
		action != managementClientIPPolicyActionUnallowlist &&
		action != managementClientIPPolicyActionBlacklist &&
		action != managementClientIPPolicyActionUnblock {
		panic("unsupported management client IP policy action")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ipHash := strings.TrimFunc(chi.URLParam(r, "ipHash"), managementGroupListECMAScriptWhitespace)
		if !validManagementClientIPHash(ipHash) {
			writeMessageError(w, http.StatusBadRequest, "IP 标识无效")
			return
		}
		body, ok := decodeManagementClientIPPolicyBody(w, r, action)
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
				Reason:               body.reason,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				body,
				action,
				result,
				0,
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		case managementClientIPPolicyActionUnallowlist:
			result, err := service.Unallowlist(r, managementclientippolicies.UnallowlistInput{
				IPHash:               ipHash,
				ActorSystemAccountID: authContext.SystemAccountID,
				Reason:               body.reason,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				body,
				action,
				managementclientippolicies.PolicySummary{},
				result.DisabledCount,
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		case managementClientIPPolicyActionBlacklist:
			result, err := service.Blacklist(r, managementclientippolicies.BlacklistInput{
				IPHash:               ipHash,
				ActorSystemAccountID: authContext.SystemAccountID,
				Reason:               body.reason,
				DurationMinutes:      body.durationMinutes,
				DurationDays:         body.durationDays,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				body,
				action,
				result,
				0,
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		case managementClientIPPolicyActionUnblock:
			result, err := service.Unblock(r, managementclientippolicies.UnblockInput{
				IPHash:               ipHash,
				ActorSystemAccountID: authContext.SystemAccountID,
				Reason:               body.reason,
			})
			if !writeManagementClientIPPolicyServiceError(w, err) {
				return
			}
			recordManagementClientIPPolicyOperationLog(
				r,
				authContext,
				ipHash,
				body,
				action,
				managementclientippolicies.PolicySummary{},
				result.DisabledCount,
				operationLogOptions,
			)
			writeData(w, http.StatusOK, result)
		}
	})
}

type managementClientIPPolicyBody struct {
	reason          *string
	durationMinutes *int
	durationDays    *int
}

func decodeManagementClientIPPolicyBody(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) (managementClientIPPolicyBody, bool) {
	var result managementClientIPPolicyBody
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
		return result, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
		return result, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return result, false
	}
	for field := range payload {
		if field != "reason" &&
			(action != managementClientIPPolicyActionBlacklist ||
				(field != "durationMinutes" && field != "durationDays")) {
			writeMessageError(w, http.StatusBadRequest, "IP 策略参数包含未知字段")
			return result, false
		}
	}
	rawReason, exists := payload["reason"]
	if exists {
		if bytes.Equal(bytes.TrimSpace(rawReason), []byte("null")) {
			writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
			return result, false
		}
		var reason string
		if err := json.Unmarshal(rawReason, &reason); err != nil {
			writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
			return result, false
		}
		reason = strings.TrimFunc(reason, managementGroupListECMAScriptWhitespace)
		if managementClientIPPolicyUTF16Length(reason) > 500 {
			writeMessageError(w, http.StatusBadRequest, "原因不能超过 500 个字符")
			return result, false
		}
		result.reason = &reason
	}
	if action != managementClientIPPolicyActionBlacklist {
		return result, true
	}
	for _, field := range []struct {
		name   string
		target **int
	}{
		{name: "durationMinutes", target: &result.durationMinutes},
		{name: "durationDays", target: &result.durationDays},
	} {
		raw, exists := payload[field.name]
		if !exists {
			continue
		}
		value, valid := managementClientIPPolicyJSONInteger(raw)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "IP 策略参数无效")
			return managementClientIPPolicyBody{}, false
		}
		*field.target = &value
	}
	return result, true
}

func managementClientIPPolicyJSONInteger(raw json.RawMessage) (int, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, false
	}
	number, ok := decoded.(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || math.Trunc(value) != value {
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if value >= float64(maxInt) {
		return maxInt, true
	}
	if value <= float64(minInt) {
		return minInt, true
	}
	return int(value), true
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
	body managementClientIPPolicyBody,
	action string,
	policyResult managementclientippolicies.PolicySummary,
	disabledCount int64,
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
	switch action {
	case managementClientIPPolicyActionAllowlist, managementClientIPPolicyActionBlacklist:
		policyType := "allowlist"
		summary := "加入 IP 白名单：" + resourceName
		durationLabel := "永久"
		durationChangeLabel := "白名单时长"
		var expiresAt any
		if action == managementClientIPPolicyActionBlacklist {
			policyType = "blacklist"
			summary = "封禁 IP：" + resourceName
			durationLabel = managementClientIPPolicyDurationLabel(body)
			durationChangeLabel = "封禁时长"
			expiresAt = managementClientIPPolicyOptionalStringValue(policyResult.ExpiresAt)
		}
		input.Summary = summary
		input.Changes = []port.OperationLogChange{
			{Field: "reason", Label: "原因", After: managementClientIPPolicyReasonValue(body.reason)},
			{Field: "policyType", Label: "策略类型", After: policyType},
			{Field: "duration", Label: durationChangeLabel, After: durationLabel},
			{Field: "expiresAt", Label: "过期时间", After: expiresAt},
		}
		input.Metadata = map[string]any{
			"ipHash":        ipHash,
			"policyId":      policyResult.ID,
			"policyType":    policyType,
			"durationLabel": durationLabel,
		}
		if policyResult.ExpiresAt != nil {
			input.Metadata["expiresAt"] = *policyResult.ExpiresAt
		}
		if body.durationMinutes != nil {
			input.Metadata["durationMinutes"] = *body.durationMinutes
		}
		if body.durationDays != nil {
			input.Metadata["durationDays"] = *body.durationDays
		}
	case managementClientIPPolicyActionUnallowlist, managementClientIPPolicyActionUnblock:
		policyType := "allowlist"
		summary := "移出 IP 白名单：" + resourceName
		if action == managementClientIPPolicyActionUnblock {
			policyType = "blacklist"
			summary = "解除 IP 封禁：" + resourceName
		}
		input.Summary = summary
		input.Changes = []port.OperationLogChange{
			{Field: "disabledCount", Label: "停用策略数", After: disabledCount},
			{Field: "policyType", Label: "策略类型", Before: policyType, After: nil},
			{Field: "reason", Label: "原因", After: managementClientIPPolicyReasonValue(body.reason)},
		}
		input.Metadata = map[string]any{
			"ipHash":        ipHash,
			"policyType":    policyType,
			"disabledCount": disabledCount,
		}
	}
	if body.reason != nil {
		input.Metadata["reason"] = *body.reason
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementClientIPPolicyDurationLabel(body managementClientIPPolicyBody) string {
	if body.durationMinutes != nil {
		return strconv.Itoa(*body.durationMinutes) + " 分钟"
	}
	if body.durationDays != nil {
		return strconv.Itoa(*body.durationDays) + " 天"
	}
	return "永久"
}

func managementClientIPPolicyOptionalStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func managementClientIPPolicyReasonValue(reason *string) any {
	if reason == nil {
		return nil
	}
	return *reason
}
