package httpapi

import (
	"log/slog"
	"net/http"

	"juhe-ai/backend-go/internal/modules/publicsettings"
)

type PublicSettingsHandler struct {
	service publicsettings.Service
	logger  *slog.Logger
}

func NewPublicSettingsHandler(service publicsettings.Service, logger *slog.Logger) PublicSettingsHandler {
	return PublicSettingsHandler{service: service, logger: logger}
}

func (h PublicSettingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	settings, err := h.service.Get(r.Context())
	if err != nil {
		if h.logger != nil {
			h.logger.Error("读取公开设置失败",
				slog.String("path", r.URL.Path),
				slog.String("request_id", requestIDFromContext(r.Context())),
				slog.Any("error", err),
			)
		}
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}

	writeData(w, http.StatusOK, settings)
}
