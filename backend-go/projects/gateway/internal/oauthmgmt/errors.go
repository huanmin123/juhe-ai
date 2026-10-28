package oauthmgmt

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// writeCreated mirrors res.status(201).json(ok(data)): the {data} envelope at
// 201.
func writeCreated(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Data any `json:"data"`
	}{Data: data})
}

// CloneSourceError marks the B5 managed-create clone precheck failures that
// carry their own HTTP status（403 授权实例不能克隆 / 404 账户不存在 / 409
// 源账户读取不稳定），语义照搬 accounts/routes.go create 的克隆分支。
type CloneSourceError struct {
	Status  int
	Message string
}

func (e *CloneSourceError) Error() string { return e.Message }

// writeRotationReceipt mirrors oauthRotationReceipt: {id, configRevision,
// updatedAt}.
func writeRotationReceipt(w http.ResponseWriter, result *RotationResult) {
	kernel.WriteOK(w, map[string]any{
		"id":             result.ID,
		"configRevision": result.ConfigRevision,
		"updatedAt":      result.UpdatedAt,
	}, "")
}

// writeProfileError maps resolveProviderProfile failures: the profile/provider
// messages render as 400, storage failures as 500.
func (d *Deps) writeProfileError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
	return
}

// writeStoreError maps pre-try store reads (Node renders them through the
// express error handler as 500).
func (d *Deps) writeStoreError(w http.ResponseWriter, err error) {
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// writeCreateError maps the create-from-* catch block: the clone-source
// precheck failures render their own status (403/404/409), business
// validation errors（写面域校验「能力域无效」、注册表组合拒绝 connection_
// capability_unsupported、克隆域不一致等 B5 收键后的确定性 400 面）render
// verbatim at 400, duplicate names → 409, everything else → 502 with the route
// fallback copy.
func (d *Deps) writeCreateError(w http.ResponseWriter, err error, fallback string) {
	var cloneSource *CloneSourceError
	if errors.As(err, &cloneSource) {
		kernel.WriteError(w, cloneSource.Status, cloneSource.Message)
		return
	}
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	// 兜底文案会掩盖建户真实失败原因（此时上游 code 已消费），必须落日志。
	slog.Error("OAuth 建户失败",
		"event", "oauth_provision_failed",
		"fallback", fallback, "error", err)
	d.writeOAuthError(w, err, fallback, "")
}

// writeOAuthError mirrors handleOAuthCreateError / handleOAuthAccountUpdateError:
//   - upstream token failures render the upstream message verbatim at 502
//     (403 for the grok entitlement denials) and mark the response upstream,
//   - grokOAuthError renders its own status and message（本地会话校验失败的
//     具体原因，如 state 无效/会话过期，必须透传给前端用于排查）,
//   - business conflicts (revision CAS, duplicate names) render 409,
//   - geminiOnboardingError（T65-② 建号链 Code Assist 探测/onboarding 失败）
//     renders 400 with its actionable copy：一次性交互路径的上游失败必须让
//     用户能看到原因并自救（手填 projectId 重试），不落 502 兜底,
//   - everything else renders 502 with the fallback.
func (d *Deps) writeOAuthError(w http.ResponseWriter, err error, fallback, revisionMessage string) {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		kernel.MarkUpstreamError(w)
		status := http.StatusBadGateway
		if upstream.StatusCode == http.StatusForbidden {
			status = http.StatusForbidden
		}
		kernel.WriteError(w, status, upstream.Message)
		return
	}
	var grokErr *grokOAuthError
	if errors.As(err, &grokErr) {
		kernel.WriteError(w, grokErr.StatusCode, grokErr.Message)
		return
	}
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	var revision *RevisionConflictError
	if errors.As(err, &revision) {
		message := revisionMessage
		if message == "" {
			message = fallback
		}
		kernel.WriteError(w, http.StatusConflict, message)
		return
	}
	var onboarding *geminiOnboardingError
	if errors.As(err, &onboarding) {
		kernel.WriteBadRequest(w, onboarding.Error())
		return
	}
	// 未分类错误：把原始错误落日志，否则前端只见兜底文案无法排障。
	slog.Error("OAuth 未分类错误",
		"event", "oauth_unclassified_error",
		"fallback", fallback, "error", err)
	kernel.WriteError(w, http.StatusBadGateway, fallback)
}

// oauthErrorText mirrors oauthErrorMessage for the grok SSO item loop: upstream
// failures surface their message, everything else the fallback.
func oauthErrorText(err error, fallback string) string {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Message
	}
	return fallback
}

// ssoCreateErrorText 在 sso-to-oauth 批量条目里透出确定性业务失败的具体文案：
// ValidationError（克隆域不一致等 400 面）与 CloneSourceError（克隆预检
// 403/404/409，自带状态语义）直写自带消息，不再折叠进「转换失败」兜底；
// 其余错误（真正的上游转换失败等）仍走 oauthErrorText 兜底。分类判别用
// errors.As。
func ssoCreateErrorText(err error, fallback string) string {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.Message
	}
	var cloneSource *CloneSourceError
	if errors.As(err, &cloneSource) {
		return cloneSource.Message
	}
	return oauthErrorText(err, fallback)
}
