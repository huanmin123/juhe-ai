package oauthmgmt

import (
	"encoding/json"
	"errors"
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

// writeCreateError maps the create-from-* catch block: business conflicts
// (duplicate account name) → 409 with the store message, everything else → 502
// with the route fallback copy.
func (d *Deps) writeCreateError(w http.ResponseWriter, err error, fallback string) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	d.writeOAuthError(w, err, fallback, "")
}

// writeOAuthError mirrors handleOAuthCreateError / handleOAuthAccountUpdateError:
//   - upstream token failures render the upstream message verbatim at 502
//     (403 for the grok entitlement denials) and mark the response upstream,
//   - grokOAuthError renders its status with the route fallback copy,
//   - business conflicts (revision CAS, duplicate names) render 409,
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
		kernel.WriteError(w, grokErr.StatusCode, fallback)
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
