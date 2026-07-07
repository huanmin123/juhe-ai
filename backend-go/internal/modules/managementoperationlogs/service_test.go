package managementoperationlogs

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListUsesPagedUpperBoundAndAdminFields(t *testing.T) {
	status := 200
	store := &operationLogReaderStub{
		listResult: port.OperationLogListResult{
			Items:   []port.OperationLogSummary{operationLogSummaryFixture("oplog_1", "full", "targeted", "full", &status)},
			HasMore: true,
		},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{Page: 2, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Page != 2 || result.PageSize != 20 || result.Total != 22 || !result.HasMore {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].ClientIP != "127.0.0.1" || result.Items[0].StatusCode == nil {
		t.Fatalf("items = %+v", result.Items)
	}
	if store.listInput.Limit != 21 || store.listInput.Offset != 20 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
}

func TestListForViewerSanitizesSummaryLevelFields(t *testing.T) {
	status := 200
	store := &operationLogReaderStub{
		visibleResult: port.OperationLogListResult{
			Items: []port.OperationLogSummary{operationLogSummaryFixture("oplog_1", "full", "targeted", "summary", &status)},
		},
	}
	service := NewService(store)

	result, err := service.List(context.Background(), ListInput{ViewerSystemAccountID: "sys_user"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !store.visibleCalled || store.visibleInput.ViewerSystemAccountID != "sys_user" {
		t.Fatalf("visible input = %+v", store.visibleInput)
	}
	item := result.Items[0]
	if item.ClientIP != "" || item.Method != "" || item.Path != "" || item.StatusCode != nil || item.DetailLevel != "summary" {
		t.Fatalf("sanitized item = %+v", item)
	}
}

func TestDetailForViewerHidesViewersAndSummaryPayload(t *testing.T) {
	status := 200
	store := &operationLogReaderStub{
		detail: port.OperationLogDetail{
			Summary: operationLogSummaryFixture("oplog_1", "full", "all_users", "", &status),
			Targets: []port.OperationLogTargetSummary{{
				ID:         "target_1",
				TargetType: "account",
				Relation:   "primary",
				CreatedAt:  time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			}},
			Viewers: []port.OperationLogViewerSummary{{
				SystemAccountID:  "sys_user",
				VisibilityReason: "actor_self",
				DetailLevel:      "full",
				CreatedAt:        time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			}},
		},
		detailFound: true,
	}
	service := NewService(store)

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: "oplog_1", ViewerSystemAccountID: "sys_user"})
	if err != nil || !found {
		t.Fatalf("Detail() = found %v err %v", found, err)
	}
	if detail.ClientIP != "" || detail.UserAgent != "" || detail.Method != "" || detail.StatusCode != nil || detail.DetailLevel != "summary" {
		t.Fatalf("detail summary = %+v", detail.Summary)
	}
	if len(detail.Changes) != 0 || len(detail.Metadata) != 0 || len(detail.Targets) != 0 || len(detail.Viewers) != 0 {
		t.Fatalf("detail payload not sanitized: %+v", detail)
	}
}

func TestDetailForAdminIncludesUserAgent(t *testing.T) {
	status := 200
	store := &operationLogReaderStub{
		detail: port.OperationLogDetail{
			Summary: operationLogSummaryFixture("oplog_1", "full", "targeted", "", &status),
		},
		detailFound: true,
	}
	service := NewService(store)

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: "oplog_1"})
	if err != nil || !found {
		t.Fatalf("Detail() = found %v err %v", found, err)
	}
	if detail.UserAgent != "unit-test" {
		t.Fatalf("UserAgent = %q, want unit-test", detail.UserAgent)
	}
}

func operationLogSummaryFixture(id string, detailLevel string, visibilityScope string, viewerDetailLevel string, statusCode *int) port.OperationLogSummary {
	return port.OperationLogSummary{
		ID:                            id,
		TraceID:                       "req_1",
		ActorSystemAccountID:          "sys_admin",
		ActorSystemAccountName:        "管理员",
		ActorRole:                     "admin",
		OperationScopeSystemAccountID: "sys_user",
		Mode:                          "admin",
		Module:                        "accounts",
		Action:                        "update_tags",
		OperationKey:                  "accounts.update_tags",
		ResourceType:                  "account",
		Summary:                       "更新账户标签：主账号",
		DetailLevel:                   detailLevel,
		VisibilityScope:               visibilityScope,
		Changes:                       []port.OperationLogChange{{Field: "tags", Label: "标签"}},
		Metadata:                      map[string]any{"source": "test"},
		Method:                        "PATCH",
		Path:                          "/__aisys__/api/accounts/acct_main/tags",
		StatusCode:                    statusCode,
		ClientIP:                      "127.0.0.1",
		UserAgent:                     "unit-test",
		CreatedAt:                     time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		ViewerDetailLevel:             viewerDetailLevel,
	}
}

type operationLogReaderStub struct {
	listInput     port.OperationLogListInput
	listResult    port.OperationLogListResult
	visibleCalled bool
	visibleInput  port.OperationLogVisibleListInput
	visibleResult port.OperationLogListResult
	detailInput   port.OperationLogDetailInput
	detail        port.OperationLogDetail
	detailFound   bool
}

func (s *operationLogReaderStub) ListOperationLogs(_ context.Context, input port.OperationLogListInput) (port.OperationLogListResult, error) {
	s.listInput = input
	return s.listResult, nil
}

func (s *operationLogReaderStub) ListVisibleOperationLogs(_ context.Context, input port.OperationLogVisibleListInput) (port.OperationLogListResult, error) {
	s.visibleCalled = true
	s.visibleInput = input
	return s.visibleResult, nil
}

func (s *operationLogReaderStub) GetOperationLogDetail(_ context.Context, input port.OperationLogDetailInput) (port.OperationLogDetail, bool, error) {
	s.detailInput = input
	return s.detail, s.detailFound, nil
}
