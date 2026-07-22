package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementtablemonitor"
	"juhe-ai/backend-go/internal/store/port"
)

const managementTableMonitorRequestTimeout = 120 * time.Second

type managementTableMonitorService interface {
	Overview(r *http.Request, input managementtablemonitor.OverviewInput) (managementtablemonitor.Overview, error)
	TableHistory(r *http.Request, input managementtablemonitor.TableHistoryInput) ([]managementtablemonitor.TableStorageSnapshot, error)
	DatabaseHistory(r *http.Request, input managementtablemonitor.DatabaseHistoryInput) ([]managementtablemonitor.DatabaseStorageSnapshot, error)
}

type managementTableMonitorServiceAdapter struct {
	service *managementtablemonitor.Service
}

func (s managementTableMonitorServiceAdapter) Overview(r *http.Request, input managementtablemonitor.OverviewInput) (managementtablemonitor.Overview, error) {
	return s.service.Overview(r.Context(), input)
}

func (s managementTableMonitorServiceAdapter) TableHistory(r *http.Request, input managementtablemonitor.TableHistoryInput) ([]managementtablemonitor.TableStorageSnapshot, error) {
	return s.service.TableHistory(r.Context(), input)
}

func (s managementTableMonitorServiceAdapter) DatabaseHistory(r *http.Request, input managementtablemonitor.DatabaseHistoryInput) ([]managementtablemonitor.DatabaseStorageSnapshot, error) {
	return s.service.DatabaseHistory(r.Context(), input)
}

func NewManagementTableMonitorHandler(service *managementtablemonitor.Service) http.Handler {
	return newManagementTableMonitorHandler(managementTableMonitorServiceAdapter{service: service})
}

func newManagementTableMonitorHandler(service managementTableMonitorService) http.Handler {
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

		ctx, cancel := context.WithTimeout(r.Context(), managementTableMonitorRequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)

		path := strings.TrimRight(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(path, "/table-monitor/overview"):
			input, err := parseManagementTableMonitorOverviewQuery(r.URL.Query())
			if err != nil {
				writeMessageError(w, http.StatusBadRequest, "表监控参数无效")
				return
			}
			result, err := service.Overview(r, input)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			writeData(w, http.StatusOK, result)
		case strings.HasSuffix(path, "/table-monitor/history"):
			input, err := parseManagementTableMonitorTableHistoryQuery(r.URL.Query())
			if err != nil {
				writeMessageError(w, http.StatusBadRequest, "表监控历史参数无效")
				return
			}
			result, err := service.TableHistory(r, input)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			writeData(w, http.StatusOK, result)
		case strings.HasSuffix(path, "/table-monitor/database-history"):
			input, err := parseManagementTableMonitorDatabaseHistoryQuery(r.URL.Query())
			if err != nil {
				writeMessageError(w, http.StatusBadRequest, "数据库增长历史参数无效")
				return
			}
			result, err := service.DatabaseHistory(r, input)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			writeData(w, http.StatusOK, result)
		default:
			writeError(w, http.StatusNotFound, "接口不存在")
		}
	})
}

func parseManagementTableMonitorOverviewQuery(values url.Values) (managementtablemonitor.OverviewInput, error) {
	limit, err := managementTableMonitorLimit(values, "limit", 1000)
	if err != nil {
		return managementtablemonitor.OverviewInput{}, err
	}
	return managementtablemonitor.OverviewInput{Limit: limit}, nil
}

func parseManagementTableMonitorTableHistoryQuery(values url.Values) (managementtablemonitor.TableHistoryInput, error) {
	roleText, rolePresent, err := managementTableMonitorText(values, "databaseRole")
	if err != nil || !rolePresent {
		return managementtablemonitor.TableHistoryInput{}, fmt.Errorf("databaseRole is required")
	}
	role, ok := managementTableMonitorRole(roleText)
	if !ok {
		return managementtablemonitor.TableHistoryInput{}, fmt.Errorf("databaseRole is invalid")
	}
	tableName, tablePresent, err := managementTableMonitorText(values, "tableName")
	if err != nil || !tablePresent || tableName == "" {
		return managementtablemonitor.TableHistoryInput{}, fmt.Errorf("tableName is required")
	}
	startText, _, err := managementTableMonitorText(values, "startAt")
	if err != nil {
		return managementtablemonitor.TableHistoryInput{}, err
	}
	endText, _, err := managementTableMonitorText(values, "endAt")
	if err != nil {
		return managementtablemonitor.TableHistoryInput{}, err
	}
	limit, err := managementTableMonitorLimit(values, "limit", 10000)
	if err != nil {
		return managementtablemonitor.TableHistoryInput{}, err
	}
	startAt, endAt := managementRuntimeLogDateTimeRangeQueryValue(startText, endText)
	return managementtablemonitor.TableHistoryInput{
		DatabaseRole: role,
		TableName:    tableName,
		StartAt:      startAt,
		EndAt:        endAt,
		Limit:        limit,
	}, nil
}

func parseManagementTableMonitorDatabaseHistoryQuery(values url.Values) (managementtablemonitor.DatabaseHistoryInput, error) {
	startText, _, err := managementTableMonitorText(values, "startAt")
	if err != nil {
		return managementtablemonitor.DatabaseHistoryInput{}, err
	}
	endText, _, err := managementTableMonitorText(values, "endAt")
	if err != nil {
		return managementtablemonitor.DatabaseHistoryInput{}, err
	}
	limit, err := managementTableMonitorLimit(values, "limit", 10000)
	if err != nil {
		return managementtablemonitor.DatabaseHistoryInput{}, err
	}
	startAt, endAt := managementRuntimeLogDateTimeRangeQueryValue(startText, endText)
	return managementtablemonitor.DatabaseHistoryInput{StartAt: startAt, EndAt: endAt, Limit: limit}, nil
}

func managementTableMonitorText(values url.Values, key string) (string, bool, error) {
	items := values[key]
	if len(items) == 0 {
		return "", false, nil
	}
	if len(items) != 1 {
		return "", true, fmt.Errorf("%s must be singular", key)
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace), true, nil
}

func managementTableMonitorLimit(values url.Values, key string, maximum int) (int, error) {
	text, present, err := managementTableMonitorText(values, key)
	if err != nil || !present {
		return 0, err
	}
	value, ok := managementGroupListNumber(text)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value < 1 || value > float64(maximum) {
		return 0, fmt.Errorf("%s is invalid", key)
	}
	return int(value), nil
}

func managementTableMonitorRole(value string) (port.MonitoredDatabaseRole, bool) {
	role := port.MonitoredDatabaseRole(value)
	switch role {
	case port.MonitoredDatabaseRoleBusiness,
		port.MonitoredDatabaseRoleDataset,
		port.MonitoredDatabaseRoleUsageCatalog,
		port.MonitoredDatabaseRoleStats,
		port.MonitoredDatabaseRoleCodexContextState:
		return role, true
	default:
		return "", false
	}
}
