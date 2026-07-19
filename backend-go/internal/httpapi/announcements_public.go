package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/announcements"
)

type announcementPublicService interface {
	ListPublic(ctx context.Context, input announcements.PublicListInput) ([]announcements.Announcement, error)
	MarkPublicRead(ctx context.Context, input announcements.PublicReadInput) (announcements.PublicReadResult, error)
}

type announcementPublicServiceAdapter struct{ service *announcements.Service }

func (s announcementPublicServiceAdapter) ListPublic(ctx context.Context, input announcements.PublicListInput) ([]announcements.Announcement, error) {
	return s.service.ListPublic(ctx, input)
}

func (s announcementPublicServiceAdapter) MarkPublicRead(ctx context.Context, input announcements.PublicReadInput) (announcements.PublicReadResult, error) {
	return s.service.MarkPublicRead(ctx, input)
}

func NewAnnouncementPublicListHandler(service *announcements.Service) http.Handler {
	return newAnnouncementPublicListHandler(announcementPublicServiceAdapter{service: service})
}

func NewAnnouncementPublicReadHandler(service *announcements.Service) http.Handler {
	return newAnnouncementPublicReadHandler(announcementPublicServiceAdapter{service: service})
}

func newAnnouncementPublicListHandler(service announcementPublicService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		limit, err := announcementPublicLimitQuery(r)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, "公告查询参数无效")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		items, err := service.ListPublic(r.Context(), announcements.PublicListInput{SystemAccountID: authContext.SystemAccountID, Limit: limit})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeAnnouncementNoStore(w)
		writeData(w, http.StatusOK, items)
	})
}

func newAnnouncementPublicReadHandler(service announcementPublicService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var body announcementPublicReadBody
		if err := decodeAnnouncementPublicReadBody(r, &body); err != nil {
			writeMessageError(w, http.StatusBadRequest, "公告已读参数无效")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.MarkPublicRead(r.Context(), announcements.PublicReadInput{
			SystemAccountID: authContext.SystemAccountID,
			AnnouncementIDs: *body.AnnouncementIDs,
		})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeAnnouncementNoStore(w)
		writeData(w, http.StatusOK, result)
	})
}

type announcementPublicReadBody struct {
	AnnouncementIDs *[]string `json:"announcementIds"`
}

func announcementPublicLimitQuery(r *http.Request) (int, error) {
	values, exists := r.URL.Query()["limit"]
	if !exists {
		return 0, nil
	}
	if len(values) != 1 {
		return 0, errors.New("invalid announcement limit")
	}
	limit, err := announcementCoercedInteger(values[0])
	if err != nil || limit < 1 || limit > 30 {
		return 0, errors.New("invalid announcement limit")
	}
	return limit, nil
}

func announcementCoercedInteger(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty number")
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		integer, integerErr := strconv.ParseInt(value, 0, 64)
		if integerErr != nil {
			return 0, err
		}
		number = float64(integer)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
		return 0, errors.New("number must be a finite integer")
	}
	return int(number), nil
}

func decodeAnnouncementPublicReadBody(r *http.Request, body *announcementPublicReadBody) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("announcement read body must contain one JSON value")
	}
	if body.AnnouncementIDs == nil || len(*body.AnnouncementIDs) > 30 {
		return errors.New("announcement IDs must contain at most 30 items")
	}
	for _, id := range *body.AnnouncementIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("announcement ID must not be blank")
		}
	}
	return nil
}

func writeAnnouncementNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
