package httpapi

import (
	"context"
	"errors"
	"net/http"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type ManagementCaptchaIssuer interface {
	IssueChallenge(ctx context.Context, clientIP string) (managementauth.CaptchaChallenge, error)
}

func NewManagementCaptchaHandler(issuer ManagementCaptchaIssuer, cfg config.Config) http.Handler {
	clientIPs := newClientIPResolver(cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if issuer == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		challenge, err := issuer.IssueChallenge(r.Context(), clientIPs.FromRequest(r))
		if err != nil {
			var limitErr *managementauth.CaptchaIssueLimitError
			if errors.As(err, &limitErr) {
				if limitErr.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", intString(limitErr.RetryAfterSeconds))
				}
				writeMessageError(w, http.StatusTooManyRequests, "验证码请求过于频繁，请稍后再试")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, challenge)
	})
}
