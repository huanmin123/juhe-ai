package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type announcementManagementService interface {
	ListManagement(ctx context.Context, page int, pageSize int) (announcements.Page, error)
	FindManagement(ctx context.Context, id string) (announcements.Announcement, bool, error)
}

type announcementManagementServiceAdapter struct{ service *announcements.Service }

type announcementManagementListItemResponse struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	ContentPreview string     `json:"contentPreview"`
	Level          string     `json:"level"`
	Status         string     `json:"status"`
	CreatedBy      string     `json:"createdBy,omitempty"`
	CreatedByName  string     `json:"createdByName,omitempty"`
	UpdatedBy      *string    `json:"updatedBy,omitempty"`
	UpdatedByName  *string    `json:"updatedByName,omitempty"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type announcementManagementPageResponse struct {
	Items    []announcementManagementListItemResponse `json:"items"`
	Total    int                                      `json:"total"`
	HasMore  bool                                     `json:"hasMore"`
	Page     int                                      `json:"page"`
	PageSize int                                      `json:"pageSize"`
}

func (s announcementManagementServiceAdapter) ListManagement(ctx context.Context, page int, pageSize int) (announcements.Page, error) {
	return s.service.ListManagement(ctx, page, pageSize)
}

func (s announcementManagementServiceAdapter) FindManagement(ctx context.Context, id string) (announcements.Announcement, bool, error) {
	return s.service.FindManagement(ctx, id)
}

func NewAnnouncementManagementHandler(service *announcements.Service) http.Handler {
	return newAnnouncementManagementHandler(announcementManagementServiceAdapter{service: service})
}

func newAnnouncementManagementHandler(service announcementManagementService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		if id := chi.URLParam(r, "id"); id != "" {
			announcement, found, err := service.FindManagement(r.Context(), id)
			if errors.Is(err, announcements.ErrAnnouncementInputInvalid) {
				writeMessageError(w, http.StatusBadRequest, "公告参数无效")
				return
			}
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "公告不存在")
				return
			}
			writeAnnouncementNoStore(w)
			writeData(w, http.StatusOK, announcement)
			return
		}

		page, err := announcementManagementIntegerQuery(r, "page", 0)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, "公告查询参数无效")
			return
		}
		pageSize, err := announcementManagementIntegerQuery(r, "pageSize", 100)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, "公告查询参数无效")
			return
		}
		result, err := service.ListManagement(r.Context(), page, pageSize)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		items := make([]announcementManagementListItemResponse, 0, len(result.Items))
		for _, item := range result.Items {
			items = append(items, announcementManagementListItemResponse{
				ID: item.ID, Title: item.Title, ContentPreview: item.Content, Level: item.Level, Status: item.Status,
				CreatedBy: item.CreatedBy, CreatedByName: item.CreatedByName,
				UpdatedBy: item.UpdatedBy, UpdatedByName: item.UpdatedByName,
				PublishedAt: item.PublishedAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
			})
		}
		writeAnnouncementNoStore(w)
		writeData(w, http.StatusOK, announcementManagementPageResponse{
			Items: items, Total: result.PageUpperBound, HasMore: result.HasMore,
			Page: result.Page, PageSize: result.PageSize,
		})
	})
}

func announcementManagementIntegerQuery(r *http.Request, key string, maximum int) (int, error) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return 0, nil
	}
	if len(values) != 1 {
		return 0, errors.New("invalid announcement query")
	}
	value, err := announcementCoercedInteger(values[0])
	if err != nil || value < 1 || maximum > 0 && value > maximum {
		return 0, errors.New("invalid announcement query")
	}
	return value, nil
}
